package developer

import (
	"context"
	"sync"
	"time"

	"github.com/robfig/cron/v3"
	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/config"
	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/figma"
	"github.com/pixxle/solomon/internal/git"
	ghclient "github.com/pixxle/solomon/internal/github"
	"github.com/pixxle/solomon/internal/plugin"
	"github.com/pixxle/solomon/internal/slack"
	"github.com/pixxle/solomon/internal/tracker"
)

func init() {
	plugin.Register("developer", func(cfg plugin.PluginConfig, libs *plugin.SharedLibs) (plugin.Plugin, error) {
		return NewDeveloperPlugin(cfg, libs)
	})
}

// DeveloperPlugin implements the ticket-implementation workflow:
// planning → coding → PR → review → merge.
type DeveloperPlugin struct {
	pluginCfg  plugin.PluginConfig
	cfg        *config.Config
	stateDB    *db.StateDB
	tracker    tracker.TaskTracker
	github     *ghclient.Client
	notifier   slack.Notifier
	figma      *figma.Client
	botUserID  string
	ghUsername string

	machine    *Machine
	loopPrev   *LoopPrevention
	dispatcher *PriorityDispatcher

	cronRunner *cron.Cron
	mu         sync.Mutex
	cancel     context.CancelFunc
	lastPrune  time.Time
}

func NewDeveloperPlugin(pluginCfg plugin.PluginConfig, libs *plugin.SharedLibs) (*DeveloperPlugin, error) {
	// Use the first GitHub client if available
	var gh *ghclient.Client
	for _, client := range libs.GitHub {
		gh = client
		break
	}

	p := &DeveloperPlugin{
		pluginCfg:  pluginCfg,
		cfg:        libs.Config,
		stateDB:    libs.DB,
		tracker:    libs.Tracker,
		github:     gh,
		notifier:   libs.Notifier,
		figma:      libs.Figma,
		botUserID:  libs.BotUserID,
		ghUsername: libs.GHUsername,
	}

	p.loopPrev = NewLoopPrevention(p.stateDB)
	p.machine = NewMachine(p.cfg, p.tracker, p.github, p.stateDB, p.figma, p.botUserID, p.notifier)
	p.dispatcher = NewPriorityDispatcher(p.cfg, p.tracker, p.github, p.stateDB, p.loopPrev, p.botUserID, p.ghUsername)

	return p, nil
}

func (p *DeveloperPlugin) Name() string { return "developer" }

func (p *DeveloperPlugin) Start(ctx context.Context) error {
	ctx, p.cancel = context.WithCancel(ctx)

	// --once mode: run a single iteration and return
	if p.cfg.Once {
		defer p.cancel()
		log.Info().Msg("developer: running single iteration (once mode)")
		return p.run(ctx)
	}

	p.cronRunner = cron.New()
	for _, sched := range p.pluginCfg.Schedules {
		s := sched
		_, err := p.cronRunner.AddFunc(s.CronExpr, func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			if err := p.run(ctx); err != nil {
				log.Error().Err(err).Str("trigger", s.Name).Msg("developer plugin run failed")
			}
		})
		if err != nil {
			return err
		}
		log.Info().Str("schedule", s.Name).Str("cron", s.CronExpr).Msg("developer: registered cron schedule")
	}
	p.cronRunner.Start()

	return nil
}

func (p *DeveloperPlugin) Stop(ctx context.Context) error {
	if p.cronRunner != nil {
		p.cronRunner.Stop()
	}
	if p.cancel != nil {
		p.cancel()
	}
	return nil
}

func (p *DeveloperPlugin) run(ctx context.Context) error {
	log.Debug().Msg("developer: starting iteration")

	// Update main branch
	if err := git.UpdateMain(ctx, p.cfg.TargetRepoPath); err != nil {
		log.Warn().Err(err).Msg("failed to update main branch")
	}

	// Housekeeping
	p.machine.Handlers().CheckMergedPRs(ctx)
	p.machine.Handlers().CheckCIPassed(ctx)
	p.machine.Handlers().CheckClosedTickets(ctx)

	// Find and handle next work item
	item, err := p.dispatcher.FindWork(ctx)
	if err != nil {
		return err
	}
	if item == nil {
		log.Debug().Msg("no work items found")
		return nil
	}

	p.loopPrev.RecordAttempt(item.Issue.Key)
	if err := p.machine.Handle(ctx, item); err != nil {
		return err
	}
	if item.State == StateInReview {
		p.loopPrev.MarkFeedbackProcessed(item.Issue.Key)
	}

	if time.Since(p.lastPrune) >= 20*time.Minute {
		p.loopPrev.Prune()
		p.lastPrune = time.Now()
	}

	return nil
}
