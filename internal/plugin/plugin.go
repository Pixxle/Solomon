package plugin

import (
	"context"

	"github.com/rs/zerolog"

	"github.com/pixxle/solomon/internal/config"
	"github.com/pixxle/solomon/internal/db"
	"github.com/pixxle/solomon/internal/figma"
	ghclient "github.com/pixxle/solomon/internal/github"
	"github.com/pixxle/solomon/internal/slack"
	"github.com/pixxle/solomon/internal/tracker"
)

// Plugin is the interface that all Solomon plugins must implement.
type Plugin interface {
	// Name returns the plugin identifier (e.g., "developer", "securityengineer").
	Name() string

	// Start begins the plugin's autonomous operation. The plugin should set up
	// its own cron schedules and run in goroutines. Start should return quickly.
	// The ctx is cancelled on application shutdown.
	Start(ctx context.Context) error

	// Stop is called during graceful shutdown. Plugins should clean up
	// child processes, worktrees, etc. within the context timeout.
	Stop(ctx context.Context) error
}

// SharedLibs holds references to shared infrastructure that plugins can use.
type SharedLibs struct {
	Config    *config.Config
	DB        *db.StateDB
	Tracker   tracker.TaskTracker
	GitHub    map[string]*ghclient.Client // keyed by repo name
	Notifier  slack.Notifier
	Figma     *figma.Client
	Logger    zerolog.Logger
	BotUserID string
	GHUsername string
}

// RepoRef describes a repository that a plugin targets, with an optional
// label for routing issues to specific repos.
type RepoRef struct {
	Name  string
	Path  string
	Label string
}

// Schedule describes a cron schedule for a plugin.
type Schedule struct {
	Name     string
	CronExpr string
}

// PluginConfig holds the configuration for a single plugin instance.
type PluginConfig struct {
	Type      string
	BoardID   string
	Repos     []RepoRef
	Schedules []Schedule
	Settings  map[string]interface{}
}
