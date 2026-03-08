package loop

import (
	"context"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	ghclient "github.com/pixxle/codehephaestus/internal/github"
	"github.com/pixxle/codehephaestus/internal/planning"
	"github.com/pixxle/codehephaestus/internal/statemachine"
	"github.com/pixxle/codehephaestus/internal/tracker"
)

type PriorityDispatcher struct {
	cfg                *config.Config
	tracker            tracker.TaskTracker
	github             *ghclient.Client
	stateDB            *db.StateDB
	loopPrev           *LoopPrevention
	botUserID          string
	lastDoneCheck      time.Time
	lastReconcileCheck time.Time
}

func NewPriorityDispatcher(cfg *config.Config, t tracker.TaskTracker, gh *ghclient.Client, stateDB *db.StateDB, lp *LoopPrevention, botUserID string) *PriorityDispatcher {
	return &PriorityDispatcher{
		cfg:       cfg,
		tracker:   t,
		github:    gh,
		stateDB:   stateDB,
		loopPrev:  lp,
		botUserID: botUserID,
	}
}

// FindWork returns the highest-priority work item, or nil if there's nothing to do.
func (pd *PriorityDispatcher) FindWork(ctx context.Context) (*statemachine.WorkItem, error) {
	// Pre-fetch shared data to avoid duplicate API calls.
	allStates, err := pd.stateDB.GetAllPlanningStates()
	if err != nil {
		log.Warn().Err(err).Msg("error fetching planning states")
	}
	var activePlans []*db.PlanningState
	for _, ps := range allStates {
		if ps.Status == planning.StatusActive {
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

	// Reconcile tracker state periodically (every 5 minutes, not every poll cycle).
	if time.Since(pd.lastReconcileCheck) >= 5*time.Minute {
		pd.reconcileTrackerState(ctx, allStates, todoMap)
		pd.lastReconcileCheck = time.Now()
	}

	// Record done tickets periodically (every 10 minutes, not every poll cycle).
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
	if item := pd.checkNewIssues(activePlans, todoIssues); item != nil {
		return item, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkPlanningConversations(_ context.Context, activePlans []*db.PlanningState, todoMap map[string]tracker.Issue) (*statemachine.WorkItem, error) {
	for _, ps := range activePlans {
		issue, ok := todoMap[ps.IssueKey]
		if !ok {
			continue
		}
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		// Trigger when the description has changed since last analysis
		if planning.DescriptionChanged(issue.Description, ps.LastSeenDescription) {
			return &statemachine.WorkItem{
				State: statemachine.StatePlanning,
				Issue: issue,
				Context: map[string]interface{}{
					"planning_state": ps,
				},
			}, nil
		}
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkReviewFeedback(ctx context.Context) (*statemachine.WorkItem, error) {
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
			if !pd.loopPrev.IsCommentProcessed(strconv.FormatInt(c.ID, 10)) {
				unprocessed = append(unprocessed, c)
			}
		}
		if len(unprocessed) == 0 {
			continue
		}

		return &statemachine.WorkItem{
			State: statemachine.StateInReview,
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

func (pd *PriorityDispatcher) checkCIFailures(ctx context.Context) (*statemachine.WorkItem, error) {
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

		return &statemachine.WorkItem{
			State: statemachine.StateCIFailure,
			Issue: issue,
			Context: map[string]interface{}{
				"pr_number": prNumber,
				"branch":    branch,
			},
		}, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkPlanningReady(ctx context.Context, activePlans []*db.PlanningState, todoMap map[string]tracker.Issue) (*statemachine.WorkItem, error) {
	for _, ps := range activePlans {
		issue, ok := todoMap[ps.IssueKey]
		if !ok {
			continue
		}
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		// Check thumbs_up reaction on the bot's comment directly by ID
		if ps.BotCommentID != "" {
			reactions, err := pd.tracker.GetCommentReactions(ctx, issue.Key, ps.BotCommentID)
			if err == nil {
				for _, r := range reactions {
					if r.Type == "thumbs_up" {
						if !issue.IsAssignedTo(pd.botUserID) {
							log.Debug().Str("issue", issue.Key).Msg("thumbs-up detected but issue not assigned to bot, staying in planning")
							break
						}
						return &statemachine.WorkItem{
							State: statemachine.StatePlanningReady,
							Issue: issue,
							Context: map[string]interface{}{
								"planning_state": ps,
							},
						}, nil
					}
				}
			}
		}
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkNewIssues(activePlans []*db.PlanningState, todoIssues []tracker.Issue) *statemachine.WorkItem {
	// Build set of issues that already have planning state
	hasPlanning := make(map[string]bool, len(activePlans))
	for _, ps := range activePlans {
		hasPlanning[ps.IssueKey] = true
	}

	for _, issue := range todoIssues {
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}
		if hasPlanning[issue.Key] {
			continue
		}

		return &statemachine.WorkItem{
			State: statemachine.StateTodo,
			Issue: issue,
		}
	}

	return nil
}

// recordDoneTickets finds issues in "Done" status assigned to the bot that
// have no existing DB record, and inserts a "complete" planning state so
// they are never picked up as new work.
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
			Status:           planning.StatusComplete,
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

// reconcileTrackerState detects when humans move tickets on the tracker board
// and fixes stale bot state. Two rules:
//   - Rule 1: Issue is in TODO but has a non-active planning_state → reopened.
//     Delete old state + bot comment so planning starts fresh.
//   - Rule 2: Issue is NOT in TODO but has an active planning_state → moved out.
//     Mark planning complete (stale).
func (pd *PriorityDispatcher) reconcileTrackerState(ctx context.Context, allStates []*db.PlanningState, todoMap map[string]tracker.Issue) {
	for _, ps := range allStates {
		_, inTodo := todoMap[ps.IssueKey]

		if inTodo && ps.Status != planning.StatusActive {
			// Rule 1: reopened ticket — clear old state so it starts fresh.
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
		} else if !inTodo && ps.Status == planning.StatusActive {
			// Rule 2: moved out of TODO — mark planning complete.
			ps.Status = planning.StatusComplete
			if err := pd.stateDB.UpdatePlanningState(ps); err != nil {
				log.Error().Err(err).Str("issue", ps.IssueKey).Msg("reconcile: failed to mark planning complete")
				continue
			}
			log.Info().Str("issue", ps.IssueKey).Msg("reconcile: marked active planning as complete (ticket moved out of TODO)")
		}
	}
}
