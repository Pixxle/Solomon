package loop

import (
	"context"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/db"
	"github.com/pixxle/codehephaestus/internal/figma"
	"github.com/pixxle/codehephaestus/internal/git"
	ghclient "github.com/pixxle/codehephaestus/internal/github"
	"github.com/pixxle/codehephaestus/internal/statemachine"
	"github.com/pixxle/codehephaestus/internal/tracker"
)

type Runner struct {
	cfg        *config.Config
	machine    *statemachine.Machine
	loopPrev   *LoopPrevention
	dispatcher *PriorityDispatcher
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
	m := statemachine.NewMachine(cfg, t, gh, stateDB, figmaClient, botUserID)
	return &Runner{
		cfg:        cfg,
		machine:    m,
		loopPrev:   lp,
		dispatcher: NewPriorityDispatcher(cfg, t, gh, stateDB, lp, botUserID),
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
		log.Debug().Int("iteration", iteration).Msg("starting iteration")

		if err := r.runIteration(ctx); err != nil {
			log.Error().Err(err).Msg("iteration error")
		}

		if r.cfg.MaxIterations > 0 && iteration >= r.cfg.MaxIterations {
			log.Info().Int("iterations", iteration).Msg("max iterations reached")
			return nil
		}

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
	if err := git.UpdateMain(ctx, r.cfg.TargetRepoPath); err != nil {
		log.Warn().Err(err).Msg("failed to update main branch")
	}

	// Housekeeping transitions
	r.machine.Handlers().CheckMergedPRs(ctx)
	r.machine.Handlers().CheckCIPassed(ctx)

	// Find and handle next work item
	item, err := r.dispatcher.FindWork(ctx)
	if err != nil {
		return err
	}
	if item == nil {
		log.Debug().Msg("no work items found")
		return nil
	}

	r.loopPrev.RecordAttempt(item.Issue.Key)
	return r.machine.Handle(ctx, item)
}
