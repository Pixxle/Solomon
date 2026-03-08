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
// checking for the ready signal first.
func (h *Handlers) HandlePlanningConversation(ctx context.Context, item *WorkItem) error {
	ps := item.Context["planning_state"].(*db.PlanningState)

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

	return h.m.planner.ContinuePlanning(ctx, item.Issue, ps)
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

	var codeChangeComments []ghclient.PRComment
	var questionComments []ghclient.PRComment

	for _, c := range comments {
		if c.Reaction == "thumbs_up" {
			classification := classifyComment(ctx, c.Body, h.m.cfg.PlanningModel)
			if classification == "question" {
				questionComments = append(questionComments, c)
			} else {
				codeChangeComments = append(codeChangeComments, c)
			}
		} else if c.Reaction == "eyes" {
			questionComments = append(questionComments, c)
		}
	}

	for _, q := range questionComments {
		if err := h.answerQuestion(ctx, item.Issue, prNumber, q); err != nil {
			log.Warn().Err(err).Int64("comment", q.ID).Msg("failed to answer question")
		}
		h.recordFeedback(item.Issue.Key, prNumber, q, "question_answered", nil)
	}

	if len(codeChangeComments) > 0 {
		if err := h.addressCodeChanges(ctx, item.Issue, prNumber, branch, codeChangeComments); err != nil {
			log.Error().Err(err).Str("issue", item.Issue.Key).Msg("failed to address code changes")
			return err
		}
	}

	return nil
}

// HandleCIFailure fixes a CI failure on an In Progress PR.
func (h *Handlers) HandleCIFailure(ctx context.Context, item *WorkItem) error {
	prNumber := item.Context["pr_number"].(int)
	branch := item.Context["branch"].(string)

	log.Info().Str("issue", item.Issue.Key).Int("pr", prNumber).Msg("handling CI failure")

	ciLogs, err := h.m.github.GetCIFailureLogs(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("getting CI logs: %w", err)
	}

	wtDir, err := git.EnsureWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)
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
			if !h.m.cfg.DryRun {
				_ = h.m.tracker.TransitionIssue(ctx, issue.Key, h.m.cfg.StatusDone())
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

	if err := h.m.planner.CompletePlanning(ctx, issue, ps); err != nil {
		return fmt.Errorf("completing planning: %w", err)
	}

	if !h.m.cfg.DryRun {
		if err := h.m.tracker.TransitionIssue(ctx, issue.Key, h.m.cfg.StatusInProgress()); err != nil {
			return fmt.Errorf("transitioning issue: %w", err)
		}
	}

	branch := h.m.tracker.GetIssueBranchName(issue, h.m.cfg.BotSlug())
	wtDir, err := git.EnsureWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	defer git.CleanupWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)

	var imagePaths []string
	_ = json.Unmarshal([]byte(ps.ImageRefsJSON), &imagePaths)

	// The description IS the spec — no need to build conversation text
	teamPrompt, err := team.BuildTeamLeadPrompt(team.TeamLeadContext{
		IssueKey:        issue.Key,
		IssueTitle:      issue.Title,
		Specification:   issue.Description,
		BotDisplayName:  h.m.cfg.BotDisplayName,
		ImagePaths:      imagePaths,
		MaxReviewRounds: h.m.cfg.MaxReviewRounds,
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

	sha, _ := git.GetCurrentSHA(ctx, wtDir)
	if sha != "" {
		_ = h.m.stateDB.MarkSHAProcessed(sha)
	}

	return nil
}

func (h *Handlers) answerQuestion(ctx context.Context, issue tracker.Issue, prNumber int, comment ghclient.PRComment) error {
	log.Info().Str("issue", issue.Key).Int64("comment", comment.ID).Msg("answering question")

	diff, _ := h.m.github.GetPRDiff(ctx, prNumber)
	prompt, err := team.BuildAnswerQuestionPrompt(comment.Body, diff)
	if err != nil {
		return err
	}

	result, err := worker.RunClaudeText(ctx, prompt, h.m.cfg.TargetRepoPath, h.m.cfg.TeammateModel)
	if err != nil {
		return err
	}

	if !h.m.cfg.DryRun {
		return h.m.github.PostPRComment(ctx, prNumber, result.Output)
	}
	return nil
}

func (h *Handlers) addressCodeChanges(ctx context.Context, issue tracker.Issue, prNumber int, branch string, comments []ghclient.PRComment) error {
	log.Info().Str("issue", issue.Key).Int("changes", len(comments)).Msg("addressing code changes")

	if !h.m.cfg.DryRun {
		_ = h.m.tracker.TransitionIssue(ctx, issue.Key, h.m.cfg.StatusInProgress())
	}

	wtDir, err := git.EnsureWorktree(ctx, branch, h.m.cfg.TargetRepoPath, h.m.cfg.WorktreePath)
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
		CommentType: "review_comment",
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
