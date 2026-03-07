package loop

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	ghclient "github.com/pixxle/codehephaestus/internal/github"
	"github.com/pixxle/codehephaestus/internal/tracker"
)

type Tier int

const (
	TierPlanningConversation Tier = iota + 1
	TierReviewFeedback
	TierCIFailure
	TierPlanningReady
	TierNewIssue
)

type WorkItem struct {
	Tier    Tier
	Issue   tracker.Issue
	Context map[string]interface{}
}

type PriorityDispatcher struct {
	cfg       *config.Config
	tracker   tracker.TaskTracker
	github    *ghclient.Client
	stateDB   *db.StateDB
	loopPrev  *LoopPrevention
}

func NewPriorityDispatcher(cfg *config.Config, t tracker.TaskTracker, gh *ghclient.Client, stateDB *db.StateDB, lp *LoopPrevention) *PriorityDispatcher {
	return &PriorityDispatcher{
		cfg:      cfg,
		tracker:  t,
		github:   gh,
		stateDB:  stateDB,
		loopPrev: lp,
	}
}

// FindWork returns the highest-priority work item, or nil if there's nothing to do.
func (pd *PriorityDispatcher) FindWork(ctx context.Context) (*WorkItem, error) {
	// Priority 1: Active planning with new human comments
	if item, err := pd.checkPlanningConversations(ctx); err != nil {
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
	if item, err := pd.checkPlanningReady(ctx); err != nil {
		log.Warn().Err(err).Msg("error checking planning ready")
	} else if item != nil {
		return item, nil
	}

	// Priority 5: New issues
	if item, err := pd.checkNewIssues(ctx); err != nil {
		log.Warn().Err(err).Msg("error checking new issues")
	} else if item != nil {
		return item, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkPlanningConversations(ctx context.Context) (*WorkItem, error) {
	activePlans, err := pd.stateDB.GetActivePlanningStates()
	if err != nil {
		return nil, err
	}

	todoIssues, err := pd.tracker.FetchIssuesByStatus(ctx, pd.cfg.StatusTodo())
	if err != nil {
		return nil, err
	}
	issueMap := make(map[string]tracker.Issue)
	for _, i := range todoIssues {
		issueMap[i.Key] = i
	}

	for _, ps := range activePlans {
		issue, ok := issueMap[ps.IssueKey]
		if !ok {
			continue
		}
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		// Check for new human comments
		if ps.LastSystemCommentAt != nil {
			comments, err := pd.tracker.GetCommentsSince(ctx, issue.Key, *ps.LastSystemCommentAt)
			if err != nil {
				continue
			}
			// Filter out bot comments
			hasHumanComment := false
			for _, c := range comments {
				botUserID, _ := pd.stateDB.GetFeedbackCutoff("_bot_user_id")
				if c.Author != fmt.Sprint(botUserID) {
					hasHumanComment = true
					break
				}
			}
			if hasHumanComment {
				return &WorkItem{
					Tier:  TierPlanningConversation,
					Issue: issue,
					Context: map[string]interface{}{
						"planning_state": ps,
					},
				}, nil
			}
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

		// Filter already-processed comments
		var unprocessed []ghclient.PRComment
		for _, c := range comments {
			if !pd.loopPrev.IsCommentProcessed(strconv.FormatInt(c.ID, 10)) {
				unprocessed = append(unprocessed, c)
			}
		}
		if len(unprocessed) == 0 {
			continue
		}

		return &WorkItem{
			Tier:  TierReviewFeedback,
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

		// Check if we already processed this SHA
		// (would need to get the current SHA and check)

		return &WorkItem{
			Tier:  TierCIFailure,
			Issue: issue,
			Context: map[string]interface{}{
				"pr_number": prNumber,
				"branch":    branch,
			},
		}, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkPlanningReady(ctx context.Context) (*WorkItem, error) {
	// This is checked as part of planning conversations in the main loop
	// The planner.CheckReadySignal is called during conversation handling
	return nil, nil
}

func (pd *PriorityDispatcher) checkNewIssues(ctx context.Context) (*WorkItem, error) {
	todoIssues, err := pd.tracker.FetchIssuesByStatus(ctx, pd.cfg.StatusTodo())
	if err != nil {
		return nil, err
	}

	for _, issue := range todoIssues {
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		// Check if this issue already has a planning state
		ps, err := pd.stateDB.GetPlanningState(issue.Key)
		if err != nil {
			continue
		}
		if ps != nil {
			continue // Already in planning
		}

		return &WorkItem{
			Tier:  TierNewIssue,
			Issue: issue,
		}, nil
	}

	return nil, nil
}
