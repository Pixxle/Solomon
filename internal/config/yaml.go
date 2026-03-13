package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

// YAMLConfig represents the full solomon.yaml configuration.
type YAMLConfig struct {
	Global  GlobalConfig  `yaml:"global"`
	Boards  []BoardConfig `yaml:"boards"`
	Repos   []RepoConfig  `yaml:"repos"`
	Plugins []PluginEntry `yaml:"plugins"`
}

// GlobalConfig holds application-wide settings.
type GlobalConfig struct {
	BotDisplayName string           `yaml:"bot_display_name"`
	DataPath       string           `yaml:"data_path"`
	Claude         ClaudeYAMLConfig `yaml:"claude"`
	Slack          SlackYAMLConfig  `yaml:"slack"`
	Figma          FigmaYAMLConfig  `yaml:"figma"`
	Planning       PlanningConfig   `yaml:"planning"`
	LogLevel       string           `yaml:"log_level"`
}

// ClaudeYAMLConfig holds Claude CLI settings.
type ClaudeYAMLConfig struct {
	MaxConcurrent    int    `yaml:"max_concurrent"`
	TeamLeadModel    string `yaml:"team_lead_model"`
	TeammateModel    string `yaml:"teammate_model"`
	PlanningModel    string `yaml:"planning_model"`
	AgentTeamTimeout int    `yaml:"agent_team_timeout"`
}

// SlackYAMLConfig holds Slack integration settings.
type SlackYAMLConfig struct {
	BotToken  string `yaml:"bot_token"`
	ChannelID string `yaml:"channel_id"`
}

// FigmaYAMLConfig holds Figma integration settings.
type FigmaYAMLConfig struct {
	AccessToken  string `yaml:"access_token"`
	ExportScale  int    `yaml:"export_scale"`
	ExportFormat string `yaml:"export_format"`
}

// PlanningConfig holds planning behavior settings.
type PlanningConfig struct {
	ReminderDays  int    `yaml:"reminder_days"`
	TimeoutAction string `yaml:"timeout_action"`
}

// BoardConfig describes a tracker board.
type BoardConfig struct {
	ID       string       `yaml:"id"`
	Tracker  string       `yaml:"tracker"`
	BaseURL  string       `yaml:"base_url"`
	Project  string       `yaml:"project"`
	Email    string       `yaml:"email"`
	APIKey   string       `yaml:"api_key"`
	Labels   BoardLabels  `yaml:"labels"`
	Statuses StatusConfig `yaml:"statuses"`
}

// BoardLabels holds label configuration for a board.
type BoardLabels struct {
	Planning string `yaml:"planning"`
	Approval string `yaml:"approval"`
}

// StatusConfig maps logical states to tracker-specific status names.
type StatusConfig struct {
	Todo       string `yaml:"todo"`
	InProgress string `yaml:"in_progress"`
	InReview   string `yaml:"in_review"`
	Done       string `yaml:"done"`
	Cancelled  string `yaml:"cancelled"`
}

// RepoConfig describes a git repository.
type RepoConfig struct {
	Name string `yaml:"name"`
	Path string `yaml:"path"`
}

// PluginEntry describes a plugin instance in the YAML config.
type PluginEntry struct {
	Type      string                 `yaml:"type"`
	Board     string                 `yaml:"board"`
	Repos     []PluginRepoRef        `yaml:"repos"`
	Schedules []ScheduleConfig       `yaml:"schedules"`
	Settings  map[string]interface{} `yaml:"settings"`
}

// PluginRepoRef links a plugin to a repo with an optional routing label.
type PluginRepoRef struct {
	Name  string `yaml:"name"`
	Label string `yaml:"label"`
}

// ScheduleConfig describes a cron schedule.
type ScheduleConfig struct {
	Name     string `yaml:"name"`
	CronExpr string `yaml:"cron"`
}

// DefaultDeveloperCron is the default cron expression used when generating
// YAML config from a legacy .env file.
const DefaultDeveloperCron = "*/2 * * * *"

// envVarRe matches ${VAR_NAME} patterns for environment variable expansion.
var envVarRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars replaces ${VAR_NAME} references in a string with their
// environment variable values.
func expandEnvVars(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		varName := envVarRe.FindStringSubmatch(match)[1]
		if val := os.Getenv(varName); val != "" {
			return val
		}
		return match // leave unresolved vars as-is
	})
}

// LoadYAML reads and parses a YAML configuration file.
// Environment variable references (${VAR_NAME}) are expanded before parsing.
func LoadYAML(path string) (*YAMLConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file: %w", err)
	}

	// Expand environment variable references
	expanded := expandEnvVars(string(data))

	var cfg YAMLConfig
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file: %w", err)
	}

	return &cfg, nil
}

// ToConfig converts a YAMLConfig into the legacy Config struct used by shared
// libraries. It uses the first configured board for tracker settings.
func (y *YAMLConfig) ToConfig() (*Config, error) {
	if len(y.Boards) == 0 {
		return nil, fmt.Errorf("at least one board must be configured")
	}

	board := y.Boards[0]

	// Determine default repo path (first repo or ".")
	targetRepoPath := "."
	if len(y.Repos) > 0 {
		targetRepoPath = y.Repos[0].Path
	}

	dataPath := y.Global.DataPath
	if dataPath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolving home directory: %w", err)
		}
		dataPath = filepath.Join(home, ".solomon")
	}

	cfg := &Config{
		BotDisplayName: orDefault(y.Global.BotDisplayName, "Solomon"),

		TaskTracker:       TrackerType(orDefault(board.Tracker, "jira")),
		TrackerAPIKey:     board.APIKey,
		TrackerBaseURL:    board.BaseURL,
		TrackerProject:    board.Project,
		JiraPlanningLabel: orDefault(board.Labels.Planning, "solomon"),
		JiraApprovalLabel: orDefault(board.Labels.Approval, "approved"),
		JiraEmail:         board.Email,

		JiraStatusTodo:       orDefault(board.Statuses.Todo, "To Do"),
		JiraStatusInProgress: orDefault(board.Statuses.InProgress, "In Progress"),
		JiraStatusInReview:   orDefault(board.Statuses.InReview, "In Review"),
		JiraStatusDone:       orDefault(board.Statuses.Done, "Done"),
		JiraStatusCancelled:  orDefault(board.Statuses.Cancelled, "Cancelled"),

		TeamLeadModel: orDefault(y.Global.Claude.TeamLeadModel, "opus"),
		TeammateModel: orDefault(y.Global.Claude.TeammateModel, "sonnet"),
		PlanningModel: orDefault(y.Global.Claude.PlanningModel, "sonnet"),

		PlanningReminderDays:  orDefaultInt(y.Global.Planning.ReminderDays, 7),
		PlanningTimeoutAction: orDefault(y.Global.Planning.TimeoutAction, "remind"),

		FigmaAccessToken:  y.Global.Figma.AccessToken,
		FigmaExportScale:  orDefaultInt(y.Global.Figma.ExportScale, 2),
		FigmaExportFormat: orDefault(y.Global.Figma.ExportFormat, "png"),

		SlackBotToken:  y.Global.Slack.BotToken,
		SlackChannelID: y.Global.Slack.ChannelID,

		AgentTeamTimeout: orDefaultInt(y.Global.Claude.AgentTeamTimeout, 3600),

		DataPath:        dataPath,
		TargetRepoPath:  targetRepoPath,
		WorktreePath:    filepath.Join(dataPath, "worktrees"),
		StateDBPath:     filepath.Join(dataPath, "state.db"),
		LogLevel:        orDefault(y.Global.LogLevel, "info"),
		SimplifyEnabled: true,
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return cfg, nil
}

// GenerateFromEnv creates a YAMLConfig from the current .env-based Config.
// Used for backwards compatibility when no YAML file exists.
func GenerateFromEnv(cfg *Config) *YAMLConfig {
	boardID := "default"
	repoName := "default"

	y := &YAMLConfig{
		Global: GlobalConfig{
			BotDisplayName: cfg.BotDisplayName,
			DataPath:       cfg.DataPath,
			Claude: ClaudeYAMLConfig{
				TeamLeadModel:    cfg.TeamLeadModel,
				TeammateModel:    cfg.TeammateModel,
				PlanningModel:    cfg.PlanningModel,
				AgentTeamTimeout: cfg.AgentTeamTimeout,
			},
			Slack: SlackYAMLConfig{
				BotToken:  cfg.SlackBotToken,
				ChannelID: cfg.SlackChannelID,
			},
			Figma: FigmaYAMLConfig{
				AccessToken:  cfg.FigmaAccessToken,
				ExportScale:  cfg.FigmaExportScale,
				ExportFormat: cfg.FigmaExportFormat,
			},
			Planning: PlanningConfig{
				ReminderDays:  cfg.PlanningReminderDays,
				TimeoutAction: cfg.PlanningTimeoutAction,
			},
			LogLevel: cfg.LogLevel,
		},
		Boards: []BoardConfig{
			{
				ID:      boardID,
				Tracker: string(cfg.TaskTracker),
				BaseURL: cfg.TrackerBaseURL,
				Project: cfg.TrackerProject,
				Email:   cfg.JiraEmail,
				APIKey:  cfg.TrackerAPIKey,
				Labels: BoardLabels{
					Planning: cfg.JiraPlanningLabel,
					Approval: cfg.JiraApprovalLabel,
				},
				Statuses: StatusConfig{
					Todo:       cfg.StatusTodo(),
					InProgress: cfg.StatusInProgress(),
					InReview:   cfg.StatusInReview(),
					Done:       cfg.StatusDone(),
					Cancelled:  cfg.StatusCancelled(),
				},
			},
		},
		Repos: []RepoConfig{
			{
				Name: repoName,
				Path: cfg.TargetRepoPath,
			},
		},
		Plugins: []PluginEntry{
			{
				Type:  "developer",
				Board: boardID,
				Repos: []PluginRepoRef{
					{Name: repoName},
				},
				Schedules: []ScheduleConfig{
					{
						Name:     "check",
						CronExpr: DefaultDeveloperCron,
					},
				},
				Settings: map[string]interface{}{
					"auto_launch":         cfg.AutoLaunchImplementation,
					"max_review_rounds":   cfg.MaxReviewRounds,
					"max_ci_fix_attempts": cfg.MaxCIFixAttempts,
					"max_uat_retries":     cfg.MaxUATRetries,
				},
			},
		},
	}
	return y
}

func orDefault(val, fallback string) string {
	if val == "" {
		return fallback
	}
	return val
}

func orDefaultInt(val, fallback int) int {
	if val == 0 {
		return fallback
	}
	return val
}

// IsYAMLConfig returns true if the given path looks like a YAML config file.
func IsYAMLConfig(path string) bool {
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}
