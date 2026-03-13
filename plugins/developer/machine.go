package developer

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/config"
	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/figma"
	ghclient "github.com/pixxle/solomon/internal/github"
	"github.com/pixxle/solomon/internal/slack"
	"github.com/pixxle/solomon/internal/tracker"
)

// Machine routes work items to the correct state handler.
type Machine struct {
	cfg        *config.Config
	tracker    tracker.TaskTracker
	github     *ghclient.Client
	stateDB    *db.StateDB
	planner    *Planner
	teamLaunch *Launcher
	botUserID  string
	notifier   slack.Notifier
	handlers   *Handlers
}

func NewMachine(
	cfg *config.Config,
	t tracker.TaskTracker,
	gh *ghclient.Client,
	stateDB *db.StateDB,
	figmaClient *figma.Client,
	botUserID string,
	notifier slack.Notifier,
) *Machine {
	planner := NewPlanner(cfg, t, stateDB, figmaClient, botUserID, notifier)
	launcher := NewLauncher(cfg)

	m := &Machine{
		cfg:        cfg,
		tracker:    t,
		github:     gh,
		stateDB:    stateDB,
		planner:    planner,
		teamLaunch: launcher,
		botUserID:  botUserID,
		notifier:   notifier,
	}
	m.handlers = NewHandlers(m)
	return m
}

func (m *Machine) Handlers() *Handlers {
	return m.handlers
}

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
