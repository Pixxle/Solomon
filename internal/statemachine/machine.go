package statemachine

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	"github.com/pixxle/codehephaestus/internal/figma"
	ghclient "github.com/pixxle/codehephaestus/internal/github"
	"github.com/pixxle/codehephaestus/internal/planning"
	"github.com/pixxle/codehephaestus/internal/team"
	"github.com/pixxle/codehephaestus/internal/tracker"
)

// Machine routes work items to the correct state handler.
type Machine struct {
	cfg        *config.Config
	tracker    tracker.TaskTracker
	github     *ghclient.Client
	stateDB    *db.StateDB
	planner    *planning.Planner
	teamLaunch *team.Launcher
	botUserID  string
	handlers   *Handlers
}

func NewMachine(
	cfg *config.Config,
	t tracker.TaskTracker,
	gh *ghclient.Client,
	stateDB *db.StateDB,
	figmaClient *figma.Client,
	botUserID string,
) *Machine {
	planner := planning.NewPlanner(cfg, t, stateDB, figmaClient, botUserID)
	launcher := team.NewLauncher(cfg)

	m := &Machine{
		cfg:        cfg,
		tracker:    t,
		github:     gh,
		stateDB:    stateDB,
		planner:    planner,
		teamLaunch: launcher,
		botUserID:  botUserID,
	}
	m.handlers = NewHandlers(m)
	return m
}

// Handlers returns the handlers for housekeeping operations.
func (m *Machine) Handlers() *Handlers {
	return m.handlers
}

// Handle dispatches a work item to the appropriate handler based on its current state.
func (m *Machine) Handle(ctx context.Context, item *WorkItem) error {
	log.Info().
		Str("issue", item.Issue.Key).
		Str("state", string(item.State)).
		Msg("handling state transition")

	switch item.State {
	case StateTodo:
		return m.handlers.HandleNewIssue(ctx, item)
	case StatePlanning:
		return m.handlers.HandlePlanningConversation(ctx, item)
	case StatePlanningReady:
		return m.handlers.HandlePlanningReady(ctx, item)
	case StateCIFailure:
		return m.handlers.HandleCIFailure(ctx, item)
	case StateInReview:
		return m.handlers.HandleReviewFeedback(ctx, item)
	default:
		return fmt.Errorf("no handler for state %q", item.State)
	}
}

// WorkItem is a unit of work identified by the priority dispatcher,
// tagged with the internal state the issue is in.
type WorkItem struct {
	State   State
	Issue   tracker.Issue
	Context map[string]interface{}
}
