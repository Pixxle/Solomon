package config

import (
	"strings"
	"testing"
)

func TestBotSlug(t *testing.T) {
	tests := []struct {
		displayName string
		want        string
	}{
		{"Solomon", "solomon"},
		{"My Cool Bot", "my-cool-bot"},
		{"Bot 2.0!", "bot-20"},
		{"UPPER CASE", "upper-case"},
		{"already-slug", "already-slug"},
	}

	for _, tt := range tests {
		t.Run(tt.displayName, func(t *testing.T) {
			c := &Config{BotDisplayName: tt.displayName}
			if got := c.BotSlug(); got != tt.want {
				t.Errorf("BotSlug() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestStatusMethods(t *testing.T) {
	jiraCfg := &Config{
		TaskTracker:          TrackerJira,
		JiraStatusTodo:       "To Do",
		JiraStatusInProgress: "In Progress",
		JiraStatusInReview:   "In Review",
		JiraStatusDone:       "Done",
	}

	linearCfg := &Config{
		TaskTracker:            TrackerLinear,
		LinearStatusTodo:       "Todo",
		LinearStatusInProgress: "Started",
		LinearStatusInReview:   "Review",
		LinearStatusDone:       "Completed",
	}

	t.Run("Jira statuses", func(t *testing.T) {
		if got := jiraCfg.StatusTodo(); got != "To Do" {
			t.Errorf("StatusTodo() = %q, want %q", got, "To Do")
		}
		if got := jiraCfg.StatusInProgress(); got != "In Progress" {
			t.Errorf("StatusInProgress() = %q, want %q", got, "In Progress")
		}
		if got := jiraCfg.StatusInReview(); got != "In Review" {
			t.Errorf("StatusInReview() = %q, want %q", got, "In Review")
		}
		if got := jiraCfg.StatusDone(); got != "Done" {
			t.Errorf("StatusDone() = %q, want %q", got, "Done")
		}
	})

	t.Run("Linear statuses", func(t *testing.T) {
		if got := linearCfg.StatusTodo(); got != "Todo" {
			t.Errorf("StatusTodo() = %q, want %q", got, "Todo")
		}
		if got := linearCfg.StatusInProgress(); got != "Started" {
			t.Errorf("StatusInProgress() = %q, want %q", got, "Started")
		}
		if got := linearCfg.StatusInReview(); got != "Review" {
			t.Errorf("StatusInReview() = %q, want %q", got, "Review")
		}
		if got := linearCfg.StatusDone(); got != "Completed" {
			t.Errorf("StatusDone() = %q, want %q", got, "Completed")
		}
	})
}

func TestLoad_ValidationErrors(t *testing.T) {
	tests := []struct {
		name    string
		envVars map[string]string
		wantErr string
	}{
		{
			name: "invalid tracker type",
			envVars: map[string]string{
				"TASK_TRACKER":     "github",
				"TRACKER_API_KEY":  "key",
				"TRACKER_BASE_URL": "https://example.com",
				"TRACKER_PROJECT":  "TEST",
				"JIRA_EMAIL":       "test@example.com",
			},
			wantErr: "TASK_TRACKER must be 'jira' or 'linear'",
		},
		{
			name: "missing api key",
			envVars: map[string]string{
				"TASK_TRACKER":     "jira",
				"TRACKER_BASE_URL": "https://example.com",
				"TRACKER_PROJECT":  "TEST",
				"JIRA_EMAIL":       "test@example.com",
			},
			wantErr: "TRACKER_API_KEY is required",
		},
		{
			name: "missing base url",
			envVars: map[string]string{
				"TASK_TRACKER":    "jira",
				"TRACKER_API_KEY": "key",
				"TRACKER_PROJECT": "TEST",
				"JIRA_EMAIL":      "test@example.com",
			},
			wantErr: "TRACKER_BASE_URL is required",
		},
		{
			name: "missing project",
			envVars: map[string]string{
				"TASK_TRACKER":     "jira",
				"TRACKER_API_KEY":  "key",
				"TRACKER_BASE_URL": "https://example.com",
				"JIRA_EMAIL":       "test@example.com",
			},
			wantErr: "TRACKER_PROJECT is required",
		},
		{
			name: "jira missing email",
			envVars: map[string]string{
				"TASK_TRACKER":     "jira",
				"TRACKER_API_KEY":  "key",
				"TRACKER_BASE_URL": "https://example.com",
				"TRACKER_PROJECT":  "TEST",
			},
			wantErr: "JIRA_EMAIL is required for Jira",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Use t.Chdir to prevent godotenv from reading the real .env
			t.Chdir(t.TempDir())

			// Clear all relevant env vars first
			for _, key := range []string{
				"TASK_TRACKER", "TRACKER_API_KEY", "TRACKER_BASE_URL",
				"TRACKER_PROJECT", "JIRA_EMAIL", "JIRA_PLANNING_LABEL",
				"BOT_DISPLAY_NAME",
			} {
				t.Setenv(key, "")
			}

			// Set test-specific env vars
			for k, v := range tt.envVars {
				t.Setenv(k, v)
			}

			_, err := Load("")
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if got := err.Error(); !strings.Contains(got, tt.wantErr) {
				t.Errorf("error = %q, want substring %q", got, tt.wantErr)
			}
		})
	}
}

func TestLoad_Defaults(t *testing.T) {
	t.Chdir(t.TempDir())

	t.Setenv("TASK_TRACKER", "jira")
	t.Setenv("TRACKER_API_KEY", "test-key")
	t.Setenv("TRACKER_BASE_URL", "https://jira.example.com")
	t.Setenv("TRACKER_PROJECT", "TEST")
	t.Setenv("JIRA_EMAIL", "bot@example.com")

	// Clear env vars that have defaults to test the defaults
	for _, key := range []string{
		"BOT_DISPLAY_NAME", "JIRA_PLANNING_LABEL", "JIRA_APPROVAL_LABEL",
		"PLANNING_MODEL", "POLL_INTERVAL", "MAX_REVIEW_ROUNDS",
	} {
		t.Setenv(key, "")
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.BotDisplayName != "Solomon" {
		t.Errorf("BotDisplayName = %q, want %q", cfg.BotDisplayName, "Solomon")
	}
	if cfg.JiraPlanningLabel != "solomon" {
		t.Errorf("JiraPlanningLabel = %q, want %q", cfg.JiraPlanningLabel, "solomon")
	}
	if cfg.JiraApprovalLabel != "approved" {
		t.Errorf("JiraApprovalLabel = %q, want %q", cfg.JiraApprovalLabel, "approved")
	}
	if cfg.PlanningModel != "sonnet" {
		t.Errorf("PlanningModel = %q, want %q", cfg.PlanningModel, "sonnet")
	}
	if cfg.PollInterval != 120 {
		t.Errorf("PollInterval = %d, want %d", cfg.PollInterval, 120)
	}
	if cfg.MaxReviewRounds != 3 {
		t.Errorf("MaxReviewRounds = %d, want %d", cfg.MaxReviewRounds, 3)
	}
	if cfg.AutoLaunchImplementation {
		t.Error("AutoLaunchImplementation should default to false")
	}
}

func TestLoad_AutoLaunchImplementation(t *testing.T) {
	t.Chdir(t.TempDir())

	for _, key := range []string{
		"TASK_TRACKER", "TRACKER_API_KEY", "TRACKER_BASE_URL",
		"TRACKER_PROJECT", "JIRA_EMAIL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("TASK_TRACKER", "jira")
	t.Setenv("TRACKER_API_KEY", "test-key")
	t.Setenv("TRACKER_BASE_URL", "https://jira.example.com")
	t.Setenv("TRACKER_PROJECT", "TEST")
	t.Setenv("JIRA_EMAIL", "bot@example.com")
	t.Setenv("AUTO_LAUNCH_IMPLEMENTATION", "true")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !cfg.AutoLaunchImplementation {
		t.Error("AutoLaunchImplementation should be true when env is set to 'true'")
	}
}
