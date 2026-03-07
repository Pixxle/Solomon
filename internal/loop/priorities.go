package loop

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	ghclient "github.com/pixxle/codehephaestus/internal/github"
	"github.com/pixxle/codehephaestus/internal/statemachine"
	"github.com/pixxle/codehephaestus/internal/tracker"
)

type PriorityDispatcher struct {
	cfg       *config.Config
	tracker   tracker.TaskTracker
	github    *ghclient.Client
	stateDB   *db.StateDB
	loopPrev  *LoopPrevention
	botUserID string
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

func (pd *PriorityDispatcher) checkPlanningConversations(ctx context.Context) (*statemachine.WorkItem, error) {
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

		if ps.LastSystemCommentAt != nil {
			comments, err := pd.tracker.GetCommentsSince(ctx, issue.Key, *ps.LastSystemCommentAt)
			if err != nil {
				continue
			}
			hasHumanComment := false
			for _, c := range comments {
				if c.Author != pd.botUserID {
					hasHumanComment = true
					break
				}
			}
			if hasHumanComment {
				return &statemachine.WorkItem{
					State: statemachine.StatePlanning,
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
			State: statemachine.StateInProgress,
			Issue: issue,
			Context: map[string]interface{}{
				"pr_number": prNumber,
				"branch":    branch,
			},
		}, nil
	}

	return nil, nil
}

func (pd *PriorityDispatcher) checkPlanningReady(ctx context.Context) (*statemachine.WorkItem, error) {
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
		if ps.LastSystemCommentAt == nil {
			continue
		}

		comments, err := pd.tracker.GetCommentsSince(ctx, issue.Key, *ps.LastSystemCommentAt)
		if err != nil {
			continue
		}
		for _, c := range comments {
			if c.Author == pd.botUserID {
				continue
			}
			lower := strings.ToLower(c.Body)
			if strings.Contains(lower, "ready") || strings.Contains(lower, "lgtm") || strings.Contains(lower, "approved") {
				return &statemachine.WorkItem{
					State: statemachine.StatePlanningReady,
					Issue: issue,
					Context: map[string]interface{}{
						"planning_state": ps,
					},
				}, nil
			}
		}

		// Check thumbs_up reaction on last system comment
		allComments, err := pd.tracker.GetComments(ctx, issue.Key)
		if err != nil {
			continue
		}
		for _, c := range allComments {
			if c.Author != pd.botUserID || !c.Created.Equal(*ps.LastSystemCommentAt) {
				continue
			}
			reactions, err := pd.tracker.GetCommentReactions(ctx, issue.Key, c.ID)
			if err != nil {
				continue
			}
			for _, r := range reactions {
				if r.Type == "thumbs_up" && r.UserID != pd.botUserID {
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

	return nil, nil
}

func (pd *PriorityDispatcher) checkNewIssues(ctx context.Context) (*statemachine.WorkItem, error) {
	todoIssues, err := pd.tracker.FetchIssuesByStatus(ctx, pd.cfg.StatusTodo())
	if err != nil {
		return nil, err
	}

	for _, issue := range todoIssues {
		if pd.loopPrev.ShouldSkip(issue.Key) {
			continue
		}

		ps, err := pd.stateDB.GetPlanningState(issue.Key)
		if err != nil {
			continue
		}
		if ps != nil {
			continue
		}

		return &statemachine.WorkItem{
			State: statemachine.StateTodo,
			Issue: issue,
		}, nil
	}

	return nil, nil
}
