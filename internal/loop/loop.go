package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	"github.com/pixxle/codehephaestus/internal/figma"
	"github.com/pixxle/codehephaestus/internal/git"
	ghclient "github.com/pixxle/codehephaestus/internal/github"
	"github.com/pixxle/codehephaestus/internal/planning"
	"github.com/pixxle/codehephaestus/internal/team"
	"github.com/pixxle/codehephaestus/internal/tracker"
	"github.com/pixxle/codehephaestus/internal/worker"
)

type Runner struct {
	cfg        *config.Config
	tracker    tracker.TaskTracker
	github     *ghclient.Client
	stateDB    *db.StateDB
	planner    *planning.Planner
	teamLaunch *team.Launcher
	loopPrev   *LoopPrevention
	dispatcher *PriorityDispatcher
	botUserID  string
}

func NewRunner(
	cfg *config.Config,
	t tracker.TaskTracker,
	gh *ghclient.Client,
	stateDB *db.StateDB,
	figmaClient *figma.Client,
	botUserID string,
) *Runner {
	lp := NewLoopPrevention(stateDB)
	return &Runner{
		cfg:        cfg,
		tracker:    t,
		github:     gh,
		stateDB:    stateDB,
		planner:    planning.NewPlanner(cfg, t, stateDB, figmaClient, botUserID),
		teamLaunch: team.NewLauncher(cfg),
		loopPrev:   lp,
		dispatcher: NewPriorityDispatcher(cfg, t, gh, stateDB, lp),
		botUserID:  botUserID,
	}
}

// Run executes the main polling loop.
func (r *Runner) Run(ctx context.Context) error {
	iteration := 0

	for {
		select {
		case <-ctx.Done():
			log.Info().Msg("shutting down main loop")
			return ctx.Err()
		default:
		}

		iteration++
		log.Info().Int("iteration", iteration).Msg("starting iteration")

		if err := r.runIteration(ctx); err != nil {
			log.Error().Err(err).Msg("iteration error")
		}

		if r.cfg.MaxIterations > 0 && iteration >= r.cfg.MaxIterations {
			log.Info().Int("iterations", iteration).Msg("max iterations reached")
			return nil
		}

		// Periodic cleanup
		if iteration%10 == 0 {
			r.loopPrev.Prune()
		}

		log.Debug().Int("seconds", r.cfg.PollInterval).Msg("sleeping")
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(r.cfg.PollInterval) * time.Second):
		}
	}
}

func (r *Runner) runIteration(ctx context.Context) error {
	// Update main branch
	if err := git.UpdateMain(ctx, r.cfg.TargetRepoPath); err != nil {
		log.Warn().Err(err).Msg("failed to update main branch")
	}

	// Housekeeping
	r.checkMergedPRs(ctx)
	r.checkCIPassed(ctx)

	// Find next work item
	item, err := r.dispatcher.FindWork(ctx)
	if err != nil {
		return fmt.Errorf("finding work: %w", err)
	}
	if item == nil {
		log.Debug().Msg("no work items found")
		return nil
	}

	log.Info().
		Str("issue", item.Issue.Key).
		Int("tier", int(item.Tier)).
		Msg("handling work item")

	r.loopPrev.RecordAttempt(item.Issue.Key)

	switch item.Tier {
	case TierPlanningConversation:
		return r.handlePlanningConversation(ctx, item)
	case TierReviewFeedback:
		return r.handleReviewFeedback(ctx, item)
	case TierCIFailure:
		return r.handleCIFailure(ctx, item)
	case TierNewIssue:
		return r.handleNewIssue(ctx, item)
	default:
		return fmt.Errorf("unknown tier: %d", item.Tier)
	}
}

func (r *Runner) handleNewIssue(ctx context.Context, item *WorkItem) error {
	return r.planner.StartPlanning(ctx, item.Issue)
}

func (r *Runner) handlePlanningConversation(ctx context.Context, item *WorkItem) error {
	ps := item.Context["planning_state"].(*db.PlanningState)

	// Check for ready signal first
	ready, err := r.planner.CheckReadySignal(ctx, item.Issue, ps)
	if err != nil {
		log.Warn().Err(err).Msg("error checking ready signal")
	}

	if ready {
		return r.transitionToImplementation(ctx, item.Issue, ps)
	}

	// Check timeout
	if err := r.planner.CheckTimeout(ctx, item.Issue, ps); err != nil {
		log.Warn().Err(err).Msg("error checking planning timeout")
	}

	// Continue conversation
	return r.planner.ContinuePlanning(ctx, item.Issue, ps)
}

func (r *Runner) transitionToImplementation(ctx context.Context, issue tracker.Issue, ps *db.PlanningState) error {
	log.Info().Str("issue", issue.Key).Msg("transitioning to implementation")

	// Complete planning and get participants
	participants, err := r.planner.CompletePlanning(ctx, issue, ps)
	if err != nil {
		return fmt.Errorf("completing planning: %w", err)
	}

	// Transition to In Progress
	if !r.cfg.DryRun {
		if err := r.tracker.TransitionIssue(ctx, issue.Key, r.cfg.StatusInProgress()); err != nil {
			return fmt.Errorf("transitioning issue: %w", err)
		}
	}

	// Create worktree
	branch := r.tracker.GetIssueBranchName(issue, r.cfg.BotSlug())
	wtDir, err := git.EnsureWorktree(ctx, branch, r.cfg.TargetRepoPath, r.cfg.WorktreePath)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	defer git.CleanupWorktree(ctx, branch, r.cfg.TargetRepoPath, r.cfg.WorktreePath)

	// Get planning conversation for context
	comments, _ := r.tracker.GetComments(ctx, issue.Key)
	var conversationText string
	for _, c := range comments {
		conversationText += fmt.Sprintf("[%s]:\n%s\n\n", c.Author, c.Body)
	}

	// Get image paths from planning state
	var imagePaths []string
	_ = json.Unmarshal([]byte(ps.ImageRefsJSON), &imagePaths)

	// Build team lead prompt
	teamPrompt, err := team.BuildTeamLeadPrompt(team.TeamLeadContext{
		IssueKey:             issue.Key,
		IssueTitle:           issue.Title,
		Specification:        issue.Description,
		PlanningConversation: conversationText,
		BotDisplayName:       r.cfg.BotDisplayName,
		ImagePaths:           imagePaths,
		MaxReviewRounds:      r.cfg.MaxReviewRounds,
	})
	if err != nil {
		return fmt.Errorf("building team lead prompt: %w", err)
	}

	// Launch agent team
	if r.cfg.DryRun {
		log.Info().Str("issue", issue.Key).Msg("[dry-run] would launch agent team")
		return nil
	}

	_, err = r.teamLaunch.LaunchTeam(ctx, teamPrompt, wtDir)
	if err != nil {
		// Log failure but don't crash - leave issue in In Progress for manual intervention
		log.Error().Err(err).Str("issue", issue.Key).Msg("agent team failed")
		_ = r.tracker.AddComment(ctx, issue.Key,
			fmt.Sprintf("## %s — Implementation Error\n\nThe agent team session failed: %v\n\nLeaving this issue In Progress for manual intervention.",
				r.cfg.BotDisplayName, err))
		return nil
	}

	// Push branch
	if err := r.github.PushBranch(ctx, branch, wtDir); err != nil {
		return fmt.Errorf("pushing branch: %w", err)
	}

	// Generate PR description
	diff, _ := r.getDiff(ctx, wtDir)
	commitLog, _ := r.getCommitLog(ctx, wtDir)
	prDescPrompt, err := team.BuildPRDescriptionPrompt(
		issue.Key, issue.Title, ps.OriginalDescription,
		conversationText, diff, commitLog, r.cfg.BotDisplayName,
	)
	if err != nil {
		return fmt.Errorf("building PR description prompt: %w", err)
	}

	prDescResult, err := worker.RunClaude(ctx, prDescPrompt, wtDir, r.cfg.TeammateModel)
	if err != nil {
		return fmt.Errorf("generating PR description: %w", err)
	}

	// Create draft PR
	prTitle := fmt.Sprintf("%s: %s", issue.Key, issue.Title)
	prNumber, err := r.github.CreatePR(ctx, prTitle, prDescResult.Output, branch, true)
	if err != nil {
		return fmt.Errorf("creating PR: %w", err)
	}
	log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("created draft PR")

	// Add reviewers
	if len(participants) > 0 {
		if err := r.github.AddReviewers(ctx, prNumber, participants); err != nil {
			log.Warn().Err(err).Msg("failed to add reviewers")
		}
	}

	// Mark SHA as processed
	sha, _ := git.GetCurrentSHA(ctx, wtDir)
	if sha != "" {
		r.loopPrev.MarkSHAProcessed(sha)
	}

	return nil
}

func (r *Runner) handleReviewFeedback(ctx context.Context, item *WorkItem) error {
	comments := item.Context["comments"].([]ghclient.PRComment)
	prNumber := item.Context["pr_number"].(int)
	branch := item.Context["branch"].(string)

	var codeChangeComments []ghclient.PRComment
	var questionComments []ghclient.PRComment

	for _, c := range comments {
		if c.Reaction == "thumbs_up" {
			// Use AI classification for inline comments
			classification := classifyComment(ctx, c.Body, r.cfg.PlanningModel)
			if classification == "question" {
				questionComments = append(questionComments, c)
			} else {
				codeChangeComments = append(codeChangeComments, c)
			}
		} else if c.Reaction == "eyes" {
			questionComments = append(questionComments, c)
		}
	}

	// Handle questions first
	for _, q := range questionComments {
		if err := r.answerQuestion(ctx, item.Issue, prNumber, q); err != nil {
			log.Warn().Err(err).Int64("comment", q.ID).Msg("failed to answer question")
		}
		r.recordFeedback(item.Issue.Key, prNumber, q, "question_answered", nil)
	}

	// Handle code changes
	if len(codeChangeComments) > 0 {
		if err := r.addressCodeChanges(ctx, item.Issue, prNumber, branch, codeChangeComments); err != nil {
			log.Error().Err(err).Str("issue", item.Issue.Key).Msg("failed to address code changes")
			return err
		}
	}

	r.loopPrev.MarkFeedbackProcessed(item.Issue.Key)
	return nil
}

func (r *Runner) answerQuestion(ctx context.Context, issue tracker.Issue, prNumber int, comment ghclient.PRComment) error {
	log.Info().Str("issue", issue.Key).Int64("comment", comment.ID).Msg("answering question")

	diff, _ := r.getDiffForPR(ctx, prNumber)
	prompt, err := team.BuildAnswerQuestionPrompt(comment.Body, diff)
	if err != nil {
		return err
	}

	result, err := worker.RunClaude(ctx, prompt, r.cfg.TargetRepoPath, r.cfg.TeammateModel)
	if err != nil {
		return err
	}

	if !r.cfg.DryRun {
		return r.github.PostPRComment(ctx, prNumber, result.Output)
	}
	return nil
}

func (r *Runner) addressCodeChanges(ctx context.Context, issue tracker.Issue, prNumber int, branch string, comments []ghclient.PRComment) error {
	log.Info().Str("issue", issue.Key).Int("changes", len(comments)).Msg("addressing code changes")

	// Transition to In Progress
	if !r.cfg.DryRun {
		_ = r.tracker.TransitionIssue(ctx, issue.Key, r.cfg.StatusInProgress())
	}

	// Create worktree
	wtDir, err := git.EnsureWorktree(ctx, branch, r.cfg.TargetRepoPath, r.cfg.WorktreePath)
	if err != nil {
		return err
	}
	defer git.CleanupWorktree(ctx, branch, r.cfg.TargetRepoPath, r.cfg.WorktreePath)

	var feedback []string
	for _, c := range comments {
		feedback = append(feedback, c.Body)
	}

	diff, _ := r.getDiff(ctx, wtDir)
	prompt, err := team.BuildAddressChangesPrompt(issue.Key, feedback, diff)
	if err != nil {
		return err
	}

	if r.cfg.DryRun {
		log.Info().Str("issue", issue.Key).Msg("[dry-run] would address code changes")
		return nil
	}

	_, err = worker.RunClaude(ctx, prompt, wtDir, r.cfg.TeammateModel)
	if err != nil {
		return err
	}

	// Push
	if err := r.github.PushBranch(ctx, branch, wtDir); err != nil {
		return err
	}

	// Record feedback
	sha, _ := git.GetCurrentSHA(ctx, wtDir)
	for _, c := range comments {
		r.recordFeedback(issue.Key, prNumber, c, "code_change", &sha)
	}

	r.loopPrev.MarkSHAProcessed(sha)
	return nil
}

func (r *Runner) handleCIFailure(ctx context.Context, item *WorkItem) error {
	prNumber := item.Context["pr_number"].(int)
	branch := item.Context["branch"].(string)

	log.Info().Str("issue", item.Issue.Key).Int("pr", prNumber).Msg("handling CI failure")

	// Get CI logs
	ciLogs, err := r.github.GetCIFailureLogs(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("getting CI logs: %w", err)
	}

	// Create worktree
	wtDir, err := git.EnsureWorktree(ctx, branch, r.cfg.TargetRepoPath, r.cfg.WorktreePath)
	if err != nil {
		return err
	}
	defer git.CleanupWorktree(ctx, branch, r.cfg.TargetRepoPath, r.cfg.WorktreePath)

	diff, _ := r.getDiff(ctx, wtDir)
	prompt, err := team.BuildCIFixPrompt(item.Issue.Key, ciLogs, diff, item.Issue.Description)
	if err != nil {
		return err
	}

	if r.cfg.DryRun {
		log.Info().Str("issue", item.Issue.Key).Msg("[dry-run] would fix CI")
		return nil
	}

	_, err = worker.RunClaude(ctx, prompt, wtDir, r.cfg.TeammateModel)
	if err != nil {
		return fmt.Errorf("running CI fix: %w", err)
	}

	if err := r.github.PushBranch(ctx, branch, wtDir); err != nil {
		return fmt.Errorf("pushing CI fix: %w", err)
	}

	sha, _ := git.GetCurrentSHA(ctx, wtDir)
	if sha != "" {
		r.loopPrev.MarkSHAProcessed(sha)
	}

	return nil
}

func (r *Runner) checkMergedPRs(ctx context.Context) {
	for _, status := range []string{r.cfg.StatusInProgress(), r.cfg.StatusInReview()} {
		issues, err := r.tracker.FetchIssuesByStatus(ctx, status)
		if err != nil {
			continue
		}
		for _, issue := range issues {
			branch := r.tracker.GetIssueBranchName(issue, r.cfg.BotSlug())
			prNumber, err := r.github.FindPRForBranch(ctx, branch)
			if err != nil || prNumber == 0 {
				continue
			}
			merged, err := r.github.IsPRMerged(ctx, prNumber)
			if err != nil || !merged {
				continue
			}

			log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("PR merged, transitioning to Done")
			if !r.cfg.DryRun {
				_ = r.tracker.TransitionIssue(ctx, issue.Key, r.cfg.StatusDone())
				_ = git.CleanupWorktree(ctx, branch, r.cfg.TargetRepoPath, r.cfg.WorktreePath)
			}
		}
	}
}

func (r *Runner) checkCIPassed(ctx context.Context) {
	issues, err := r.tracker.FetchIssuesByStatus(ctx, r.cfg.StatusInProgress())
	if err != nil {
		return
	}
	for _, issue := range issues {
		branch := r.tracker.GetIssueBranchName(issue, r.cfg.BotSlug())
		prNumber, err := r.github.FindPRForBranch(ctx, branch)
		if err != nil || prNumber == 0 {
			continue
		}
		status, err := r.github.GetPRCheckStatus(ctx, prNumber)
		if err != nil || status != "success" {
			continue
		}

		log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("CI passed, transitioning to In Review")
		if !r.cfg.DryRun {
			_ = r.tracker.TransitionIssue(ctx, issue.Key, r.cfg.StatusInReview())
			_ = r.github.MarkPRReady(ctx, prNumber)
		}
	}
}

func (r *Runner) recordFeedback(issueKey string, prNumber int, comment ghclient.PRComment, action string, sha *string) {
	rec := &db.PRFeedbackRecord{
		IssueKey:    issueKey,
		PRNumber:    prNumber,
		CommentID:   strconv.FormatInt(comment.ID, 10),
		CommentType: "review_comment",
		ActionTaken: action,
		CommitSHA:   sha,
		CreatedAt:   time.Now().UTC(),
	}
	if err := r.stateDB.InsertPRFeedback(rec); err != nil {
		log.Warn().Err(err).Msg("failed to record PR feedback")
	}
}

func (r *Runner) getDiff(ctx context.Context, wtDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "diff", "main...HEAD")
	cmd.Dir = wtDir
	out, err := cmd.Output()
	return string(out), err
}

func (r *Runner) getCommitLog(ctx context.Context, wtDir string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", "log", "main...HEAD", "--oneline")
	cmd.Dir = wtDir
	out, err := cmd.Output()
	return string(out), err
}

func (r *Runner) getDiffForPR(ctx context.Context, prNumber int) (string, error) {
	cmd := exec.CommandContext(ctx, "gh", "pr", "diff", strconv.Itoa(prNumber))
	cmd.Dir = r.cfg.TargetRepoPath
	out, err := cmd.Output()
	return string(out), err
}

func classifyComment(ctx context.Context, body, model string) string {
	prompt := fmt.Sprintf(`Classify this PR review comment. Is it requesting code changes, or asking a question?

Comment: %q

Respond with ONLY a JSON object: {"type": "code_change"} or {"type": "question"}`, body)

	output, err := worker.RunClaudeQuick(ctx, prompt, model)
	if err != nil {
		return "code_change" // default
	}

	var result struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &result); err != nil {
		return "code_change"
	}
	return result.Type
}
