package statemachine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/db"
	"github.com/pixxle/codehephaestus/internal/git"
	ghclient "github.com/pixxle/codehephaestus/internal/github"
	"github.com/pixxle/codehephaestus/internal/guardrails"
	"github.com/pixxle/codehephaestus/internal/team"
	"github.com/pixxle/codehephaestus/internal/tracker"
	"github.com/pixxle/codehephaestus/internal/worker"
)

// Handlers contains the logic for each state transition.
type Handlers struct {
	m *Machine
}

func NewHandlers(m *Machine) *Handlers {
	return &Handlers{m: m}
}

// HandleNewIssue begins the planning conversation for a fresh To Do issue.
func (h *Handlers) HandleNewIssue(ctx context.Context, item *WorkItem) error {
	return h.m.planner.StartPlanning(ctx, item.Issue)
}

// HandlePlanningConversation continues the planning conversation,
// checking for the ready signal first, then handling phase transitions.
func (h *Handlers) HandlePlanningConversation(ctx context.Context, item *WorkItem) error {
	ps := item.Context["planning_state"].(*db.PlanningState)

	// Check for explicit ready signal (human approval to move to implementation)
	ready, err := h.m.planner.CheckReadySignal(ctx, item.Issue, ps)
	if err != nil {
		log.Warn().Err(err).Msg("error checking ready signal")
	}
	if ready && item.Issue.IsAssignedTo(h.m.botUserID) {
		return h.transitionToImplementation(ctx, item.Issue, ps)
	}
	if ready {
		log.Info().Str("issue", item.Issue.Key).Msg("ready signal detected but issue not assigned to bot, continuing planning only")
	}

	if err := h.m.planner.CheckTimeout(ctx, item.Issue, ps); err != nil {
		log.Warn().Err(err).Msg("error checking planning timeout")
	}

	// ContinuePlanning handles phase transitions internally (product → technical)
	if err := h.m.planner.ContinuePlanning(ctx, item.Issue, ps); err != nil {
		return err
	}

	// After continuing, check if auto-launch conditions are met:
	// ticket assigned + both phases complete + auto-launch enabled.
	// Only re-fetch state when auto-launch is enabled to avoid unnecessary DB reads.
	if h.m.cfg.AutoLaunchImplementation {
		updatedPS, err := h.m.stateDB.GetPlanningState(item.Issue.Key)
		if err != nil || updatedPS == nil {
			return nil
		}
		if h.m.planner.ShouldAutoLaunch(item.Issue, updatedPS) {
			log.Info().Str("issue", item.Issue.Key).Msg("auto-launching implementation (both planning phases complete, ticket assigned)")
			return h.transitionToImplementation(ctx, item.Issue, updatedPS)
		}
	}

	return nil
}

// HandlePlanningReady transitions a planning-complete issue into implementation.
func (h *Handlers) HandlePlanningReady(ctx context.Context, item *WorkItem) error {
	if !item.Issue.IsAssignedTo(h.m.botUserID) {
		log.Info().Str("issue", item.Issue.Key).Msg("HandlePlanningReady called but issue not assigned to bot, skipping")
		return nil
	}
	ps := item.Context["planning_state"].(*db.PlanningState)
	return h.transitionToImplementation(ctx, item.Issue, ps)
}

// HandleReviewFeedback addresses PR review comments (questions and code changes).
func (h *Handlers) HandleReviewFeedback(ctx context.Context, item *WorkItem) error {
	comments := item.Context["comments"].([]ghclient.PRComment)
	prNumber := item.Context["pr_number"].(int)
	branch := item.Context["branch"].(string)

	_ = h.m.notifier.NotifyReviewFeedback(ctx, item.Issue.Key, prNumber)

	var codeChangeComments []ghclient.PRComment
	var questionComments []ghclient.PRComment

	var skippedComments []ghclient.PRComment
	for _, c := range comments {
		// Scan each comment for jailbreak attempts before processing
		commentSource := fmt.Sprintf("PR #%d review comment by %s", prNumber, c.Author)
		scan := guardrails.ScanForJailbreak(ctx, c.Body, commentSource, h.m.cfg.PlanningModel)
		if scan.Blocked {
			log.Warn().Str("issue", item.Issue.Key).Int64("comment", c.ID).Str("author", c.Author).Str("reason", scan.Reason).Msg("jailbreak attempt in review comment, skipping")
			_ = h.m.notifier.NotifyJailbreakDetected(ctx, item.Issue.Key, commentSource, scan.Reason)
			h.recordFeedback(item.Issue.Key, prNumber, c, "blocked_jailbreak", nil)
			continue
		}

		if c.Reaction == "thumbs_up" {
			classification := classifyComment(ctx, c.Body, h.m.cfg.PlanningModel)
			if classification == "question" {
				questionComments = append(questionComments, c)
			} else {
				codeChangeComments = append(codeChangeComments, c)
			}
		} else if c.Reaction == "eyes" {
			questionComments = append(questionComments, c)
		} else {
			// Unreacted comment: classify as question or skip
			if classifyIsQuestion(ctx, c.Body, h.m.cfg.PlanningModel) {
				questionComments = append(questionComments, c)
			} else {
				skippedComments = append(skippedComments, c)
			}
		}
	}

	if len(questionComments) > 0 {
		diff, _ := h.m.github.GetPRDiff(ctx, prNumber)
		for _, q := range questionComments {
			if err := h.answerQuestion(ctx, item.Issue, prNumber, diff, q); err != nil {
				log.Warn().Err(err).Int64("comment", q.ID).Msg("failed to answer question")
			}
			h.recordFeedback(item.Issue.Key, prNumber, q, "question_answered", nil)
		}
	}

	if len(codeChangeComments) > 0 {
		if err := h.addressCodeChanges(ctx, item.Issue, prNumber, branch, codeChangeComments); err != nil {
			log.Error().Err(err).Str("issue", item.Issue.Key).Msg("failed to address code changes")
			return err
		}
	}

	for _, c := range skippedComments {
		h.recordFeedback(item.Issue.Key, prNumber, c, "skipped_not_question", nil)
	}

	return nil
}

// HandleCIFailure fixes a CI failure on an In Progress PR.
func (h *Handlers) HandleCIFailure(ctx context.Context, item *WorkItem) error {
	prNumber := item.Context["pr_number"].(int)
	branch := item.Context["branch"].(string)

	log.Info().Str("issue", item.Issue.Key).Int("pr", prNumber).Msg("handling CI failure")
	_ = h.m.notifier.NotifyCIFailure(ctx, item.Issue.Key, prNumber)

	ciLogs, err := h.m.github.GetCIFailureLogs(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("getting CI logs: %w", err)
	}

	wtDir, err := git.EnsureWorktreeWithIdentity(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath, h.m.cfg.GitUserName, h.m.cfg.GitUserEmail)
	if err != nil {
		return err
	}
	defer git.CleanupWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)

	diff, _ := git.DiffFromMain(ctx, wtDir)
	prompt, err := team.BuildCIFixPrompt(item.Issue.Key, ciLogs, diff, item.Issue.Description)
	if err != nil {
		return err
	}

	if h.m.cfg.DryRun {
		log.Info().Str("issue", item.Issue.Key).Msg("[dry-run] would fix CI")
		return nil
	}

	_, err = worker.RunClaude(ctx, prompt, wtDir, h.m.cfg.TeammateModel)
	if err != nil {
		return fmt.Errorf("running CI fix: %w", err)
	}

	if err := h.m.github.PushBranch(ctx, branch, wtDir); err != nil {
		return fmt.Errorf("pushing CI fix: %w", err)
	}

	sha, _ := git.GetCurrentSHA(ctx, wtDir)
	if sha != "" {
		_ = h.m.stateDB.MarkSHAProcessed(sha)
	}

	return nil
}

// CheckMergedPRs transitions merged PRs to Done.
func (h *Handlers) CheckMergedPRs(ctx context.Context) {
	for _, status := range []string{h.m.cfg.StatusInProgress(), h.m.cfg.StatusInReview()} {
		issues, err := h.m.tracker.FetchIssuesByStatus(ctx, status)
		if err != nil {
			continue
		}
		for _, issue := range issues {
			branch := h.m.tracker.GetIssueBranchName(issue, h.m.cfg.BotSlug())
			prNumber, err := h.m.github.FindPRForBranch(ctx, branch)
			if err != nil || prNumber == 0 {
				continue
			}
			merged, err := h.m.github.IsPRMerged(ctx, prNumber)
			if err != nil || !merged {
				continue
			}

			log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("PR merged, transitioning to Done")
			_ = h.m.notifier.NotifyPRMerged(ctx, issue.Key, prNumber)
			if !h.m.cfg.DryRun {
				_ = h.m.tracker.TransitionIssue(ctx, issue.Key, h.m.cfg.StatusDone())
				_ = git.CleanupWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)
				ps, psErr := h.m.stateDB.GetPlanningState(issue.Key)
				if psErr != nil {
					log.Warn().Err(psErr).Str("issue", issue.Key).Msg("failed to fetch planning state for comment cleanup")
				} else if ps != nil && ps.BotCommentID != "" {
					if err := h.m.tracker.DeleteComment(ctx, issue.Key, ps.BotCommentID); err != nil {
						log.Warn().Err(err).Str("issue", issue.Key).Msg("failed to delete bot comment on merge")
					}
				}
			}
		}
	}
}

// CheckClosedTickets closes open PRs and deletes branches for tickets moved to Done or Cancelled.
func (h *Handlers) CheckClosedTickets(ctx context.Context) {
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for _, status := range []string{h.m.cfg.StatusDone(), h.m.cfg.StatusCancelled()} {
		issues, err := h.m.tracker.FetchIssuesByStatus(ctx, status)
		if err != nil {
			continue
		}
		for _, issue := range issues {
			if issue.Updated.Before(cutoff) {
				continue
			}
			branch := h.m.tracker.GetIssueBranchName(issue, h.m.cfg.BotSlug())
			prNumber, err := h.m.github.FindOpenPRForBranch(ctx, branch)
			if err != nil || prNumber == 0 {
				continue
			}

			log.Info().Str("issue", issue.Key).Str("status", status).Int("pr", prNumber).Msg("ticket closed, closing PR and deleting branch")
			if !h.m.cfg.DryRun {
				if err := h.m.github.ClosePR(ctx, prNumber, true); err != nil {
					log.Warn().Err(err).Str("issue", issue.Key).Int("pr", prNumber).Msg("failed to close PR")
					continue
				}
				_ = git.CleanupWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)
			}
		}
	}
}

// CheckCIPassed transitions In Progress issues to In Review when CI passes.
func (h *Handlers) CheckCIPassed(ctx context.Context) {
	issues, err := h.m.tracker.FetchIssuesByStatus(ctx, h.m.cfg.StatusInProgress())
	if err != nil {
		return
	}
	for _, issue := range issues {
		branch := h.m.tracker.GetIssueBranchName(issue, h.m.cfg.BotSlug())
		prNumber, err := h.m.github.FindPRForBranch(ctx, branch)
		if err != nil || prNumber == 0 {
			continue
		}
		status, err := h.m.github.GetPRCheckStatus(ctx, prNumber)
		if err != nil || status != "success" {
			continue
		}

		log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("CI passed, transitioning to In Review")
		if !h.m.cfg.DryRun {
			_ = h.m.tracker.TransitionIssue(ctx, issue.Key, h.m.cfg.StatusInReview())
			_ = h.m.github.MarkPRReady(ctx, prNumber)
		}
	}
}

// --- private helpers ---

func (h *Handlers) transitionToImplementation(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	log.Info().Str("issue", issue.Key).Msg("transitioning to implementation")
	_ = h.m.notifier.NotifyImplementationStarted(ctx, issue.Key)

	if err := h.m.planner.CompletePlanning(ctx, issue, ps); err != nil {
		return fmt.Errorf("completing planning: %w", err)
	}

	if !h.m.cfg.DryRun {
		if err := h.m.tracker.TransitionIssue(ctx, issue.Key, h.m.cfg.StatusInProgress()); err != nil {
			return fmt.Errorf("transitioning issue: %w", err)
		}
	}

	branch := h.m.tracker.GetIssueBranchName(issue, h.m.cfg.BotSlug())
	wtDir, err := git.EnsureWorktreeWithIdentity(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath, h.m.cfg.GitUserName, h.m.cfg.GitUserEmail)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	defer git.CleanupWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)

	var imagePaths []string
	_ = json.Unmarshal([]byte(ps.ImageRefsJSON), &imagePaths)

	// The description IS the spec; pass product summary as planning context
	teamPrompt, err := team.BuildTeamLeadPrompt(team.TeamLeadContext{
		IssueKey:             issue.Key,
		IssueTitle:           issue.Title,
		Specification:        issue.Description,
		PlanningConversation: ps.ProductSummary,
		BotDisplayName:       h.m.cfg.BotDisplayName,
		ImagePaths:           imagePaths,
		MaxReviewRounds:      h.m.cfg.MaxReviewRounds,
	})
	if err != nil {
		return fmt.Errorf("building team lead prompt: %w", err)
	}

	if h.m.cfg.DryRun {
		log.Info().Str("issue", issue.Key).Msg("[dry-run] would launch agent team")
		return nil
	}

	_, err = h.m.teamLaunch.LaunchTeam(ctx, teamPrompt, wtDir)
	if err != nil {
		log.Error().Err(err).Str("issue", issue.Key).Msg("agent team failed")
		_ = h.m.tracker.AddComment(ctx, issue.Key,
			fmt.Sprintf("## %s — Implementation Error\n\nThe agent team session failed: %v\n\nLeaving this issue In Progress for manual intervention.",
				h.m.cfg.BotDisplayName, err))
		return nil
	}

	// Run /simplify to clean up the implementation before creating the PR
	if h.m.cfg.SimplifyEnabled {
		log.Info().Str("issue", issue.Key).Msg("running simplify pass")
		simplifyResult, simplifyErr := worker.RunClaude(ctx, "/simplify", wtDir, h.m.cfg.TeammateModel)
		if simplifyErr != nil {
			log.Warn().Err(simplifyErr).Str("issue", issue.Key).Msg("simplify pass failed, continuing with PR")
		} else if simplifyResult.ExitCode != 0 {
			log.Warn().Int("exit_code", simplifyResult.ExitCode).Str("issue", issue.Key).Msg("simplify exited non-zero, continuing with PR")
		}
	}

	hasCommits, err := git.HasCommitsAheadOfMain(ctx, wtDir)
	if err != nil {
		return fmt.Errorf("checking commits: %w", err)
	}
	if !hasCommits {
		log.Warn().Str("issue", issue.Key).Msg("agent team produced no commits, leaving issue In Progress for manual intervention")
		_ = h.m.tracker.AddComment(ctx, issue.Key,
			fmt.Sprintf("## %s — No Changes Produced\n\nThe agent team session completed but produced no commits. Leaving this issue In Progress for manual intervention.",
				h.m.cfg.BotDisplayName))
		return nil
	}

	if err := h.m.github.PushBranch(ctx, branch, wtDir); err != nil {
		return fmt.Errorf("pushing branch: %w", err)
	}

	diff, _ := git.DiffFromMain(ctx, wtDir)
	commitLog, _ := git.CommitLogFromMain(ctx, wtDir)
	prDescPrompt, err := team.BuildPRDescriptionPrompt(
		issue.Key, issue.Title, issue.Description,
		"", diff, commitLog, h.m.cfg.BotDisplayName,
	)
	if err != nil {
		return fmt.Errorf("building PR description prompt: %w", err)
	}

	prDescResult, err := worker.RunClaudeText(ctx, prDescPrompt, wtDir, h.m.cfg.TeammateModel)
	if err != nil {
		return fmt.Errorf("generating PR description: %w", err)
	}

	prTitle := fmt.Sprintf("%s: %s", issue.Key, issue.Title)
	prNumber, err := h.m.github.CreatePR(ctx, prTitle, prDescResult.Output, branch, true)
	if err != nil {
		return fmt.Errorf("creating PR: %w", err)
	}
	log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("created draft PR")
	_ = h.m.notifier.NotifyPRCreated(ctx, issue.Key, prNumber)

	sha, _ := git.GetCurrentSHA(ctx, wtDir)
	if sha != "" {
		_ = h.m.stateDB.MarkSHAProcessed(sha)
	}

	return nil
}

func (h *Handlers) answerQuestion(ctx context.Context, issue tracker.Issue, prNumber int, diff string, comment ghclient.PRComment) error {
	log.Info().Str("issue", issue.Key).Int64("comment", comment.ID).Msg("answering question")

	prompt, err := team.BuildAnswerQuestionPrompt(comment.Body, diff)
	if err != nil {
		return err
	}

	result, err := worker.RunClaude(ctx, prompt, h.m.cfg.TargetRepoPath, h.m.cfg.TeammateModel)
	if err != nil {
		return err
	}

	if !h.m.cfg.DryRun {
		if comment.Type == "review_comment" {
			if err := h.m.github.ReplyToReviewComment(ctx, prNumber, comment.ID, result.Output); err != nil {
				return err
			}
			if err := h.m.github.ResolveReviewThread(ctx, comment.NodeID); err != nil {
				log.Warn().Err(err).Int64("comment", comment.ID).Msg("failed to resolve review thread (non-fatal)")
			}
			return nil
		}
		return h.m.github.PostPRComment(ctx, prNumber, result.Output)
	}
	return nil
}

func (h *Handlers) addressCodeChanges(ctx context.Context, issue tracker.Issue, prNumber int, branch string, comments []ghclient.PRComment) error {
	log.Info().Str("issue", issue.Key).Int("changes", len(comments)).Msg("addressing code changes")

	if !h.m.cfg.DryRun {
		_ = h.m.tracker.TransitionIssue(ctx, issue.Key, h.m.cfg.StatusInProgress())
	}

	wtDir, err := git.EnsureWorktreeWithIdentity(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath, h.m.cfg.GitUserName, h.m.cfg.GitUserEmail)
	if err != nil {
		return err
	}
	defer git.CleanupWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)

	var feedback []string
	for _, c := range comments {
		feedback = append(feedback, c.Body)
	}

	diff, _ := git.DiffFromMain(ctx, wtDir)
	prompt, err := team.BuildAddressChangesPrompt(issue.Key, feedback, diff)
	if err != nil {
		return err
	}

	if h.m.cfg.DryRun {
		log.Info().Str("issue", issue.Key).Msg("[dry-run] would address code changes")
		return nil
	}

	_, err = worker.RunClaude(ctx, prompt, wtDir, h.m.cfg.TeammateModel)
	if err != nil {
		return err
	}

	if err := h.m.github.PushBranch(ctx, branch, wtDir); err != nil {
		return err
	}

	sha, _ := git.GetCurrentSHA(ctx, wtDir)
	for _, c := range comments {
		h.recordFeedback(issue.Key, prNumber, c, "code_change", &sha)
	}

	_ = h.m.stateDB.MarkSHAProcessed(sha)
	return nil
}

func (h *Handlers) recordFeedback(issueKey string, prNumber int, comment ghclient.PRComment, action string, sha *string) {
	rec := &db.PRFeedbackRecord{
		IssueKey:    issueKey,
		PRNumber:    prNumber,
		CommentID:   strconv.FormatInt(comment.ID, 10),
		CommentType: comment.Type,
		ActionTaken: action,
		CommitSHA:   sha,
		CreatedAt:   time.Now().UTC(),
	}
	if err := h.m.stateDB.InsertPRFeedback(rec); err != nil {
		log.Warn().Err(err).Msg("failed to record PR feedback")
	}
}

func classifyComment(ctx context.Context, body, model string) string {
	prompt := fmt.Sprintf(`Classify this PR review comment. Is it requesting code changes, or asking a question?

Comment: %q

Respond with ONLY a JSON object: {"type": "code_change"} or {"type": "question"}`, body)

	output, err := worker.RunClaudeQuick(ctx, prompt, model)
	if err != nil {
		return "code_change"
	}

	var result struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		return "code_change"
	}
	return result.Type
}

func classifyIsQuestion(ctx context.Context, body, model string) bool {
	prompt := fmt.Sprintf(`Is this PR comment asking a question that needs answering? Direct questions, requests for explanation = yes. Praise, acknowledgments, automated summaries, informational statements = no.

Comment: %q

Respond with ONLY a JSON object: {"is_question": true} or {"is_question": false}`, body)

	output, err := worker.RunClaudeQuick(ctx, prompt, model)
	if err != nil {
		return false
	}

	var result struct {
		IsQuestion bool `json:"is_question"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		return false
	}
	return result.IsQuestion
}
