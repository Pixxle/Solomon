package developer

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/config"
	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/git"
	ghclient "github.com/pixxle/solomon/internal/github"
	"github.com/pixxle/solomon/internal/tracker"
)

const githubBotSuffix = "[bot]"

type PriorityDispatcher struct {
	cfg                *config.Config
	tracker            tracker.TaskTracker
	github             *ghclient.Client
	stateDB            *db.StateDB
	loopPrev           *LoopPrevention
	botUserID          string
	ghUsername         string
	lastDoneCheck      time.Time
	lastReconcileCheck time.Time
}

func NewPriorityDispatcher(cfg *config.Config, t tracker.TaskTracker, gh *ghclient.Client, stateDB *db.StateDB, lp *LoopPrevention, botUserID string, ghUsername string) *PriorityDispatcher {
	return &PriorityDispatcher{
		cfg:        cfg,
		tracker:    t,
		github:     gh,
		stateDB:    stateDB,
		loopPrev:   lp,
		botUserID:  botUserID,
		ghUsername: ghUsername,
	}
}

func (pd *PriorityDispatcher) FindWork(ctx context.Context) (*WorkItem, error) {
	allStates, err := pd.stateDB.GetAllPlanningStates()
	if err != nil {
		log.Warn().Err(err).Msg("error fetching planning states")
	}
	var activePlans []*db.PlanningState
	for _, ps := range allStates {
		if ps.Status == StatusActive {
			activePlans = append(activePlans, ps)
		}
	}
	todoIssues, err := pd.tracker.FetchIssuesByStatus(ctx, pd.cfg.StatusTodo())
	if err != nil {
		log.Warn().Err(err).Msg("error fetching todo issues")
	}
	todoMap := make(map[string]tracker.Issue)
	for _, i := range todoIssues {
		todoMap[i.Key] = i
	}

	if time.Since(pd.lastReconcileCheck) >= 5*time.Minute {
		pd.reconcileTrackerState(ctx, allStates, todoMap)
		pd.adoptUnknownAssignedIssues(ctx)
		pd.lastReconcileCheck = time.Now()
	}

	if time.Since(pd.lastDoneCheck) >= 10*time.Minute {
		pd.recordDoneTickets(ctx)
		pd.lastDoneCheck = time.Now()
	}

	// Priority 1: Active planning with new human comments
	if item, err := pd.checkPlanningConversations(ctx, activePlans, todoMap); err != nil {
		log.Warn().Err(err).Msg("error checking planning conversations")
	} else if item != nil {
		return item, nil
	}

	// Priority 2: PR review feedback
	if item, err := pd.checkReviewFeedback(ctx); err != nil {
		log.Warn().Err(err).Msg("error checking review feedback")
	} else if item != nil {
		return item, nil
	}

	// Priority 3: CI failures
	if item, err := pd.checkCIFailures(ctx); err != nil {
		log.Warn().Err(err).Msg("error checking CI failures")
	} else if item != nil {
		return item, nil
	}

	// Priority 4: Planning ready signal
	if item, err := pd.checkPlanningReady(ctx, activePlans, todoMap); err != nil {
		log.Warn().Err(err).Msg("error checking planning ready")
	} else if item != nil {
		return item, nil
	}

	// Priority 5: New issues
	if item := pd.checkNewIssues(allStates, todoIssues); item != nil {
		return item, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkPlanningConversations(_ context.Context, activePlans []*db.PlanningState, todoMap map[string]tracker.Issue) (*WorkItem, error) {
	for _, ps := range activePlans {
		issue, ok := todoMap[ps.IssueKey]
		if !ok {
			continue
		}
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		if DescriptionChanged(issue.Description, ps.LastSeenDescription) {
			return &WorkItem{
				State: StatePlanning,
				Issue: issue,
				Context: map[string]interface{}{
					"planning_state": ps,
				},
			}, nil
		}
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkReviewFeedback(ctx context.Context) (*WorkItem, error) {
	inReviewIssues, err := pd.tracker.FetchIssuesByStatus(ctx, pd.cfg.StatusInReview())
	if err != nil {
		return nil, err
	}

	for _, issue := range inReviewIssues {
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		branch := pd.tracker.GetIssueBranchName(issue, pd.cfg.BotSlug())
		prNumber, err := pd.github.FindPRForBranch(ctx, branch)
		if err != nil || prNumber == 0 {
			continue
		}

		cutoff := pd.loopPrev.GetFeedbackCutoff(issue.Key)
		var sinceStr *string
		if !cutoff.IsZero() {
			s := cutoff.Format(time.RFC3339)
			sinceStr = &s
		}

		comments, err := pd.github.GetPRComments(ctx, prNumber, sinceStr)
		if err != nil || len(comments) == 0 {
			continue
		}

		var unprocessed []ghclient.PRComment
		for _, c := range comments {
			commentIDStr := strconv.FormatInt(c.ID, 10)
			if pd.loopPrev.IsCommentProcessed(commentIDStr) {
				continue
			}
			if (pd.ghUsername != "" && c.Author == pd.ghUsername) || strings.HasSuffix(c.Author, githubBotSuffix) {
				pd.recordSkippedBot(issue.Key, prNumber, c)
				continue
			}
			unprocessed = append(unprocessed, c)
		}
		if len(unprocessed) == 0 {
			continue
		}

		return &WorkItem{
			State: StateInReview,
			Issue: issue,
			Context: map[string]interface{}{
				"comments":  unprocessed,
				"pr_number": prNumber,
				"branch":    branch,
			},
		}, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkCIFailures(ctx context.Context) (*WorkItem, error) {
	inProgressIssues, err := pd.tracker.FetchIssuesByStatus(ctx, pd.cfg.StatusInProgress())
	if err != nil {
		return nil, err
	}

	for _, issue := range inProgressIssues {
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		branch := pd.tracker.GetIssueBranchName(issue, pd.cfg.BotSlug())
		prNumber, err := pd.github.FindPRForBranch(ctx, branch)
		if err != nil || prNumber == 0 {
			continue
		}

		status, err := pd.github.GetPRCheckStatus(ctx, prNumber)
		if err != nil || status != "failure" {
			continue
		}

		return &WorkItem{
			State: StateCIFailure,
			Issue: issue,
			Context: map[string]interface{}{
				"pr_number": prNumber,
				"branch":    branch,
			},
		}, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkPlanningReady(ctx context.Context, activePlans []*db.PlanningState, todoMap map[string]tracker.Issue) (*WorkItem, error) {
	for _, ps := range activePlans {
		issue, ok := todoMap[ps.IssueKey]
		if !ok {
			continue
		}
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		ready, err := pd.tracker.IsReadySignal(ctx, issue, ps.BotCommentID)
		if err != nil {
			log.Warn().Err(err).Str("issue", issue.Key).Msg("error checking ready signal")
			continue
		}
		if ready {
			if !issue.IsAssignedTo(pd.botUserID) {
				log.Debug().Str("issue", issue.Key).Msg("ready signal detected but issue not assigned to bot, staying in planning")
				continue
			}
			return &WorkItem{
				State: StatePlanningReady,
				Issue: issue,
				Context: map[string]interface{}{
					"planning_state": ps,
				},
			}, nil
		}

		if AutoLaunchReady(pd.cfg.AutoLaunchImplementation, pd.botUserID, issue, ps) {
			log.Info().Str("issue", issue.Key).Msg("auto-launch conditions met: both planning phases complete and ticket assigned")
			return &WorkItem{
				State: StatePlanningReady,
				Issue: issue,
				Context: map[string]interface{}{
					"planning_state": ps,
				},
			}, nil
		}
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkNewIssues(allStates []*db.PlanningState, todoIssues []tracker.Issue) *WorkItem {
	hasPlanning := make(map[string]bool, len(allStates))
	for _, ps := range allStates {
		hasPlanning[ps.IssueKey] = true
	}

	for _, issue := range todoIssues {
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}
		if hasPlanning[issue.Key] {
			continue
		}

		return &WorkItem{
			State: StateTodo,
			Issue: issue,
		}
	}

	return nil
}

func (pd *PriorityDispatcher) recordSkippedBot(issueKey string, prNumber int, c ghclient.PRComment) {
	rec := &db.PRFeedbackRecord{
		IssueKey:    issueKey,
		PRNumber:    prNumber,
		CommentID:   strconv.FormatInt(c.ID, 10),
		CommentType: c.Type,
		ActionTaken: "skipped_bot",
		CreatedAt:   time.Now().UTC(),
	}
	if err := pd.stateDB.InsertPRFeedback(rec); err != nil {
		log.Warn().Err(err).Msg("failed to record skipped bot comment")
	}
}

func (pd *PriorityDispatcher) recordDoneTickets(ctx context.Context) {
	doneIssues, err := pd.tracker.FetchIssuesByStatus(ctx, pd.cfg.StatusDone())
	if err != nil {
		log.Warn().Err(err).Msg("error fetching done issues")
		return
	}

	now := time.Now().UTC()
	for _, issue := range doneIssues {
		if !issue.IsAssignedTo(pd.botUserID) {
			continue
		}
		existing, err := pd.stateDB.GetPlanningState(issue.Key)
		if err != nil {
			log.Warn().Err(err).Str("issue", issue.Key).Msg("error checking planning state for done ticket")
			continue
		}
		if existing != nil {
			continue
		}

		ps := &db.PlanningState{
			IssueKey:         issue.Key,
			ConversationJSON: db.EmptyJSONArray,
			ParticipantsJSON: db.EmptyJSONArray,
			Status:           StatusComplete,
			CreatedAt:        now,
			UpdatedAt:        now,
		}
		if err := pd.stateDB.InsertPlanningState(ps); err != nil {
			log.Warn().Err(err).Str("issue", issue.Key).Msg("failed to record done ticket")
			continue
		}
		log.Info().Str("issue", issue.Key).Msg("recorded pre-existing done ticket as complete")
	}
}

func (pd *PriorityDispatcher) reconcileTrackerState(ctx context.Context, allStates []*db.PlanningState, todoMap map[string]tracker.Issue) {
	for _, ps := range allStates {
		issue, inTodo := todoMap[ps.IssueKey]

		if inTodo && ps.Status != StatusActive {
			pd.cleanupPRForIssue(ctx, issue)
			if ready, _ := pd.tracker.IsReadySignal(ctx, issue, ps.BotCommentID); ready {
				if err := pd.tracker.ClearReadySignal(ctx, ps.IssueKey); err != nil {
					log.Warn().Err(err).Str("issue", ps.IssueKey).Msg("reconcile: failed to clear ready signal (best-effort)")
				}
			}
			if ps.BotCommentID != "" {
				if err := pd.tracker.DeleteComment(ctx, ps.IssueKey, ps.BotCommentID); err != nil {
					log.Warn().Err(err).Str("issue", ps.IssueKey).Msg("reconcile: failed to delete old bot comment (best-effort)")
				}
			}
			if err := pd.stateDB.DeletePlanningState(ps.IssueKey); err != nil {
				log.Error().Err(err).Str("issue", ps.IssueKey).Msg("reconcile: failed to delete planning state")
				continue
			}
			log.Info().Str("issue", ps.IssueKey).Str("old_status", ps.Status).Msg("reconcile: cleared stale state for reopened ticket")
		} else if !inTodo && ps.Status == StatusActive {
			ps.Status = StatusComplete
			if err := pd.stateDB.UpdatePlanningState(ps); err != nil {
				log.Error().Err(err).Str("issue", ps.IssueKey).Msg("reconcile: failed to mark planning complete")
				continue
			}
			log.Info().Str("issue", ps.IssueKey).Msg("reconcile: marked active planning as complete (ticket moved out of TODO)")
		}
	}
}

func (pd *PriorityDispatcher) cleanupPRForIssue(ctx context.Context, issue tracker.Issue) {
	branch := pd.tracker.GetIssueBranchName(issue, pd.cfg.BotSlug())
	prNumber, err := pd.github.FindOpenPRForBranch(ctx, branch)
	if err != nil {
		log.Warn().Err(err).Str("issue", issue.Key).Msg("reconcile: failed to check for open PR")
		return
	}
	if prNumber == 0 {
		return
	}
	if pd.cfg.DryRun {
		log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("[dry-run] reconcile: would close PR and delete branch")
		return
	}
	log.Info().Str("issue", issue.Key).Int("pr", prNumber).Msg("reconcile: closing PR and deleting branch for reopened ticket")
	if err := pd.github.ClosePR(ctx, prNumber, true); err != nil {
		log.Warn().Err(err).Str("issue", issue.Key).Int("pr", prNumber).Msg("reconcile: failed to close PR")
	}
	if err := git.CleanupWorktree(ctx, branch, pd.cfg.TargetRepoPath, pd.cfg.WorktreePath); err != nil {
		log.Warn().Err(err).Str("issue", issue.Key).Msg("reconcile: failed to clean up worktree")
	}
}

func (pd *PriorityDispatcher) adoptUnknownAssignedIssues(ctx context.Context) {
	for _, status := range []string{pd.cfg.StatusInProgress(), pd.cfg.StatusInReview()} {
		issues, err := pd.tracker.FetchIssuesByStatus(ctx, status)
		if err != nil {
			log.Warn().Err(err).Str("status", status).Msg("reconcile: failed to fetch issues for adoption check")
			continue
		}
		for _, issue := range issues {
			if !issue.IsAssignedTo(pd.botUserID) {
				continue
			}
			existing, err := pd.stateDB.GetPlanningState(issue.Key)
			if err != nil {
				log.Warn().Err(err).Str("issue", issue.Key).Msg("reconcile: failed to check planning state for adoption")
				continue
			}
			if existing != nil {
				continue
			}
			if pd.cfg.DryRun {
				log.Info().Str("issue", issue.Key).Str("status", status).Msg("[dry-run] reconcile: would move unknown issue to TODO for planning")
				continue
			}
			log.Info().Str("issue", issue.Key).Str("status", status).Msg("reconcile: unknown issue assigned to bot, moving to TODO for planning")
			if err := pd.tracker.TransitionIssue(ctx, issue.Key, pd.cfg.StatusTodo()); err != nil {
				log.Warn().Err(err).Str("issue", issue.Key).Msg("reconcile: failed to transition issue to TODO")
			}
		}
	}
}
