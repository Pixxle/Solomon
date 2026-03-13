package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type TrackerType string

const (
	TrackerJira   TrackerType = "jira"
	TrackerLinear TrackerType = "linear"
)

type Config struct {
	// Branding
	BotDisplayName string

	// Tracker
	TaskTracker       TrackerType
	TrackerAPIKey     string
	TrackerBaseURL    string
	TrackerProject    string
	JiraPlanningLabel string
	JiraApprovalLabel string
	JiraEmail         string

	// Status Mapping - Jira
	JiraStatusTodo       string
	JiraStatusInProgress string
	JiraStatusInReview   string
	JiraStatusDone       string
	JiraStatusCancelled  string

	// Status Mapping - Linear
	LinearStatusTodo       string
	LinearStatusInProgress string
	LinearStatusInReview   string
	LinearStatusDone       string
	LinearStatusCancelled  string

	// Claude Code
	TeamLeadModel string
	TeammateModel string
	PlanningModel string

	// Planning
	PlanningReminderDays     int
	PlanningTimeoutAction    string
	AutoLaunchImplementation bool

	// Figma
	FigmaAccessToken  string
	FigmaExportScale  int
	FigmaExportFormat string

	// Slack (optional — enabled when SLACK_BOT_TOKEN and SLACK_CHANNEL_ID are set)
	SlackBotToken  string
	SlackChannelID string

	// Implementation
	MaxReviewRounds  int
	MaxCIFixAttempts int
	MaxUATRetries    int
	AgentTeamTimeout int

	// Runtime
	DataPath       string // base directory for all Solomon data (default ~/.solomon)
	TargetRepoPath string
	WorktreePath   string // derived: DataPath/worktrees
	StateDBPath    string // derived: DataPath/state.db

	// Logging
	LogLevel string

	// Post-processing
	SimplifyEnabled bool

	// Git identity (set at boot from GitHub auth)
	GitUserName  string
	GitUserEmail string

	// CLI overrides
	DryRun  bool
	Once    bool
	Verbose bool
}

func Load(envPath string) (*Config, error) {
	if envPath != "" {
		_ = godotenv.Load(envPath)
	} else {
		_ = godotenv.Load()
	}

	cfg := &Config{
		BotDisplayName: envOrDefault("BOT_DISPLAY_NAME", "Solomon"),

		TaskTracker:       TrackerType(envOrDefault("TASK_TRACKER", "jira")),
		TrackerAPIKey:     os.Getenv("TRACKER_API_KEY"),
		TrackerBaseURL:    os.Getenv("TRACKER_BASE_URL"),
		TrackerProject:    os.Getenv("TRACKER_PROJECT"),
		JiraPlanningLabel: envOrDefault("JIRA_PLANNING_LABEL", "solomon"),
		JiraApprovalLabel: envOrDefault("JIRA_APPROVAL_LABEL", "approved"),
		JiraEmail:         os.Getenv("JIRA_EMAIL"),

		JiraStatusTodo:       envOrDefault("JIRA_STATUS_TODO", "To Do"),
		JiraStatusInProgress: envOrDefault("JIRA_STATUS_IN_PROGRESS", "In Progress"),
		JiraStatusInReview:   envOrDefault("JIRA_STATUS_IN_REVIEW", "In Review"),
		JiraStatusDone:       envOrDefault("JIRA_STATUS_DONE", "Done"),
		JiraStatusCancelled:  envOrDefault("JIRA_STATUS_CANCELLED", "Cancelled"),

		LinearStatusTodo:       envOrDefault("LINEAR_STATUS_TODO", "Todo"),
		LinearStatusInProgress: envOrDefault("LINEAR_STATUS_IN_PROGRESS", "In Progress"),
		LinearStatusInReview:   envOrDefault("LINEAR_STATUS_IN_REVIEW", "In Review"),
		LinearStatusDone:       envOrDefault("LINEAR_STATUS_DONE", "Done"),
		LinearStatusCancelled:  envOrDefault("LINEAR_STATUS_CANCELLED", "Cancelled"),

		TeamLeadModel: envOrDefault("TEAM_LEAD_MODEL", "opus"),
		TeammateModel: envOrDefault("TEAMMATE_MODEL", "sonnet"),
		PlanningModel: envOrDefault("PLANNING_MODEL", "sonnet"),

		PlanningReminderDays:     envOrDefaultInt("PLANNING_REMINDER_DAYS", 7),
		PlanningTimeoutAction:    envOrDefault("PLANNING_TIMEOUT_ACTION", "remind"),
		AutoLaunchImplementation: os.Getenv("AUTO_LAUNCH_IMPLEMENTATION") == "true",

		FigmaAccessToken:  os.Getenv("FIGMA_ACCESS_TOKEN"),
		FigmaExportScale:  envOrDefaultInt("FIGMA_EXPORT_SCALE", 2),
		FigmaExportFormat: envOrDefault("FIGMA_EXPORT_FORMAT", "png"),

		SlackBotToken:  os.Getenv("SLACK_BOT_TOKEN"),
		SlackChannelID: os.Getenv("SLACK_CHANNEL_ID"),

		MaxReviewRounds:  envOrDefaultInt("MAX_REVIEW_ROUNDS", 3),
		MaxCIFixAttempts: envOrDefaultInt("MAX_CI_FIX_ATTEMPTS", 5),
		MaxUATRetries:    envOrDefaultInt("MAX_UAT_RETRIES", 3),
		AgentTeamTimeout: envOrDefaultInt("AGENT_TEAM_TIMEOUT", 3600),

		TargetRepoPath: envOrDefault("TARGET_REPO_PATH", "."),

		LogLevel:        envOrDefault("LOG_LEVEL", "info"),
		SimplifyEnabled: os.Getenv("SIMPLIFY_ENABLED") != "false",
	}

	cfg.DataPath = os.Getenv("SOLOMON_DATA_PATH")
	if cfg.DataPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home directory: %w", err)
		}
		cfg.DataPath = filepath.Join(home, ".solomon")
	}
	cfg.WorktreePath = filepath.Join(cfg.DataPath, "worktrees")
	cfg.StateDBPath = filepath.Join(cfg.DataPath, "state.db")

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.TaskTracker != TrackerJira && c.TaskTracker != TrackerLinear {
		return fmt.Errorf("TASK_TRACKER must be 'jira' or 'linear', got %q", c.TaskTracker)
	}
	if c.TrackerAPIKey == "" {
		return fmt.Errorf("TRACKER_API_KEY is required")
	}
	if c.TrackerBaseURL == "" {
		return fmt.Errorf("TRACKER_BASE_URL is required")
	}
	if c.TrackerProject == "" {
		return fmt.Errorf("TRACKER_PROJECT is required")
	}
	if c.TaskTracker == TrackerJira && c.JiraEmail == "" {
		return fmt.Errorf("JIRA_EMAIL is required for Jira")
	}
	return nil
}

func (c *Config) StatusTodo() string {
	if c.TaskTracker == TrackerJira {
		return c.JiraStatusTodo
	}
	return c.LinearStatusTodo
}

func (c *Config) StatusInProgress() string {
	if c.TaskTracker == TrackerJira {
		return c.JiraStatusInProgress
	}
	return c.LinearStatusInProgress
}

func (c *Config) StatusInReview() string {
	if c.TaskTracker == TrackerJira {
		return c.JiraStatusInReview
	}
	return c.LinearStatusInReview
}

func (c *Config) StatusDone() string {
	if c.TaskTracker == TrackerJira {
		return c.JiraStatusDone
	}
	return c.LinearStatusDone
}

func (c *Config) StatusCancelled() string {
	if c.TaskTracker == TrackerJira {
		return c.JiraStatusCancelled
	}
	return c.LinearStatusCancelled
}

func (c *Config) BotSlug() string {
	s := strings.ToLower(c.BotDisplayName)
	s = strings.ReplaceAll(s, " ", "-")
	var result []byte
	for _, ch := range s {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			result = append(result, byte(ch))
		}
	}
	return string(result)
}

func (c *Config) SlackEnabled() bool {
	return c.SlackBotToken != "" && c.SlackChannelID != ""
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envOrDefaultInt(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}
