package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	slackapi "github.com/slack-go/slack"

	"github.com/pixxle/solomon/internal/config"
	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/figma"
	ghclient "github.com/pixxle/solomon/internal/github"
	"github.com/pixxle/solomon/internal/plugin"
	"github.com/pixxle/solomon/internal/tracker"

	// Register plugins via init().
	_ "github.com/pixxle/solomon/plugins/changelog"
	_ "github.com/pixxle/solomon/plugins/developer"
	_ "github.com/pixxle/solomon/plugins/securityengineer"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	configPath := flag.String("config", "solomon.yaml", "path to solomon.yaml config file")
	dryRun := flag.Bool("dry-run", false, "log actions without executing side effects")
	once := flag.Bool("once", false, "run all plugin schedules once then exit")
	verbose := flag.Bool("verbose", false, "enable debug logging")
	flag.Parse()

	// Load .env for ${VAR} expansion in YAML config.
	_ = godotenv.Load()

	// Parse YAML config.
	yamlCfg, err := config.LoadYAML(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	cfg, err := yamlCfg.ToConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}
	cfg.DryRun = *dryRun
	cfg.Once = *once
	cfg.Verbose = *verbose

	// Logging.
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	if cfg.Verbose {
		level = zerolog.DebugLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = zerolog.New(zerolog.ConsoleWriter{Out: os.Stderr}).
		With().Timestamp().Logger()

	// Verify required external tools are available.
	for _, bin := range []string{"claude", "gh", "git"} {
		if _, err := exec.LookPath(bin); err != nil {
			fmt.Fprintf(os.Stderr, "required tool %q not found in PATH\n", bin)
			os.Exit(1)
		}
	}

	log.Info().Str("config", *configPath).Msg("starting solomon")

	// State database.
	stateDB, err := db.Open(cfg.StateDBPath)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open state db")
	}
	defer stateDB.Close()

	// Tracker.
	trk, err := tracker.NewTracker(cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create tracker")
	}

	// Slack client (shared across plugins; each plugin creates its own notifier).
	var slackClient *slackapi.Client
	if cfg.SlackEnabled() && !cfg.DryRun {
		slackClient = slackapi.New(cfg.SlackBotToken)
	}

	// Figma client (optional).
	var figmaClient *figma.Client
	if cfg.FigmaAccessToken != "" {
		figmaClient = figma.NewClient(cfg.FigmaAccessToken, cfg.FigmaExportScale, cfg.FigmaExportFormat)
	}

	// GitHub clients — one per configured repo.
	ghClients := make(map[string]*ghclient.Client, len(yamlCfg.Repos))
	for _, repo := range yamlCfg.Repos {
		ghClients[repo.Name] = ghclient.NewClient(repo.Path)
	}

	// Resolve git identity from the first repo.
	var botUserID, ghUsername string
	if len(yamlCfg.Repos) > 0 {
		firstClient := ghClients[yamlCfg.Repos[0].Name]
		identity, err := firstClient.GetAuthenticatedIdentity(context.Background())
		if err != nil {
			log.Warn().Err(err).Msg("could not resolve GitHub identity — git commits may use default identity")
		} else {
			ghUsername = identity.Login
			botUserID = identity.Login
			cfg.GitUserName = identity.Name
			cfg.GitUserEmail = identity.Email
			log.Info().Str("login", identity.Login).Msg("authenticated as")
		}
	}

	// Resolve tracker user identity (e.g. Jira accountId) so that
	// IsAssignedTo checks compare the correct identifier.
	if trk != nil {
		if trackerUID, err := trk.ResolveCurrentUser(context.Background()); err != nil {
			log.Warn().Err(err).Msg("could not resolve tracker user identity — assignment detection may not work")
		} else {
			botUserID = trackerUID
			log.Info().Str("tracker_user", trackerUID).Msg("resolved tracker identity")
		}
	}

	// Shared libraries for plugins.
	libs := &plugin.SharedLibs{
		Config:      cfg,
		DB:          stateDB,
		Tracker:     trk,
		GitHub:      ghClients,
		SlackClient: slackClient,
		Figma:       figmaClient,
		Logger:      log.Logger,
		BotUserID:   botUserID,
		GHUsername:  ghUsername,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Instantiate and start plugins.
	var plugins []plugin.Plugin
	for i, entry := range yamlCfg.Plugins {
		pcfg := plugin.PluginConfig{
			Type:     entry.Type,
			BoardID:  entry.Board,
			Settings: entry.Settings,
		}
		for _, r := range entry.Repos {
			pcfg.Repos = append(pcfg.Repos, plugin.RepoRef{
				Name:  r.Name,
				Label: r.Label,
			})
		}
		for _, s := range entry.Schedules {
			pcfg.Schedules = append(pcfg.Schedules, plugin.Schedule{
				Name:     s.Name,
				CronExpr: s.CronExpr,
			})
		}

		p, err := plugin.New(entry.Type, pcfg, libs)
		if err != nil {
			log.Fatal().Err(err).Int("index", i).Str("type", entry.Type).
				Msg("failed to create plugin")
		}

		if err := p.Start(ctx); err != nil {
			log.Fatal().Err(err).Str("plugin", p.Name()).
				Msg("failed to start plugin")
		}
		log.Info().Str("plugin", p.Name()).Msg("plugin started")
		plugins = append(plugins, p)
	}

	if cfg.Once {
		log.Info().Msg("--once mode: running all schedules immediately then exiting")
		// Plugins already ran via Start; just shut down.
		shutdownPlugins(plugins)
		return
	}

	// Wait for shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	sig := <-sigCh
	log.Info().Str("signal", sig.String()).Msg("shutting down")
	cancel()

	shutdownPlugins(plugins)
	log.Info().Msg("shutdown complete")
}

// shutdownPlugins calls Stop on each plugin, ignoring individual errors after
// logging them. Uses a fresh context with no deadline — plugins are expected to
// clean up quickly on their own.
func shutdownPlugins(plugins []plugin.Plugin) {
	for _, p := range plugins {
		if err := p.Stop(context.Background()); err != nil {
			log.Error().Err(err).Str("plugin", p.Name()).Msg("plugin stop error")
		}
	}
}
