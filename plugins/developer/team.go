package developer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/solomon/internal/claude"
	"github.com/pixxle/solomon/internal/config"
)

// --- Launcher ---

type Launcher struct {
	cfg *config.Config
}

func NewLauncher(cfg *config.Config) *Launcher {
	return &Launcher{cfg: cfg}
}

func (l *Launcher) LaunchTeam(ctx context.Context, prompt, worktreePath string) (*claude.ClaudeResult, error) {
	if err := InjectAgents(worktreePath); err != nil {
		return nil, fmt.Errorf("injecting agents: %w", err)
	}
	defer CleanupAgents(worktreePath)

	timeout := time.Duration(l.cfg.AgentTeamTimeout) * time.Second

	log.Info().
		Str("cwd", worktreePath).
		Str("model", l.cfg.TeamLeadModel).
		Dur("timeout", timeout).
		Msg("launching agent team")

	result, err := claude.RunClaudeAgentTeam(ctx, prompt, worktreePath, l.cfg.TeamLeadModel, timeout)
	if err != nil {
		return nil, fmt.Errorf("agent team session failed: %w", err)
	}

	if result.ExitCode != 0 {
		log.Warn().Int("exit_code", result.ExitCode).Msg("agent team exited with non-zero code")
	}

	return result, nil
}

// --- Prompt builders ---

type TeamLeadContext struct {
	IssueKey             string
	IssueTitle           string
	Specification        string
	AcceptanceCriteria   string
	EdgeCases            string
	PlanningConversation string
	BotDisplayName       string
	ImagePaths           []string
	MaxReviewRounds      int
}

func BuildTeamLeadPrompt(ctx TeamLeadContext) (string, error) {
	prompt, err := claude.RenderPrompt("team_lead.md.tmpl", ctx)
	if err != nil {
		return "", fmt.Errorf("rendering team lead prompt: %w", err)
	}
	return prompt, nil
}

func BuildPRDescriptionPrompt(issueKey, issueTitle, originalDesc, planConversation, diff, commitLog, botName string) (string, error) {
	data := map[string]interface{}{
		"IssueKey":         issueKey,
		"IssueTitle":       issueTitle,
		"OriginalDesc":     originalDesc,
		"PlanConversation": planConversation,
		"Diff":             truncateStr(diff, 50000),
		"CommitLog":        commitLog,
		"BotDisplayName":   botName,
	}
	return claude.RenderPrompt("pr_description.md.tmpl", data)
}

func BuildCIFixPrompt(issueKey, ciOutput, diff, spec string) (string, error) {
	data := map[string]interface{}{
		"IssueKey": issueKey,
		"CIOutput": truncateStr(ciOutput, 20000),
		"Diff":     truncateStr(diff, 50000),
		"Spec":     spec,
	}
	return claude.RenderPrompt("fix_ci.md.tmpl", data)
}

func BuildAnswerQuestionPrompt(question, diff string) (string, error) {
	data := map[string]interface{}{
		"Question": question,
		"Diff":     truncateStr(diff, 50000),
	}
	return claude.RenderPrompt("answer_question.md.tmpl", data)
}

func BuildAddressChangesPrompt(issueKey string, feedback []string, diff string) (string, error) {
	data := map[string]interface{}{
		"IssueKey": issueKey,
		"Feedback": strings.Join(feedback, "\n\n---\n\n"),
		"Diff":     truncateStr(diff, 50000),
	}
	return claude.RenderPrompt("address_changes.md.tmpl", data)
}

func truncateStr(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "\n\n... (truncated)"
}

// --- Agent injection ---

var agentFiles = []string{
	"frontend-dev.md",
	"backend-dev.md",
	"infra-dev.md",
	"database-specialist.md",
	"api-designer.md",
	"test-engineer.md",
	"devils-advocate.md",
	"security-auditor.md",
	"performance-reviewer.md",
}

func getAgentsSourceDir() string {
	if dir, err := os.Getwd(); err == nil {
		candidate := filepath.Join(dir, "agents")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "agents")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		candidate := filepath.Join(filepath.Dir(filename), "..", "..", "agents")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "agents"
}

func InjectAgents(worktreePath string) error {
	srcDir := getAgentsSourceDir()
	destDir := filepath.Join(worktreePath, ".claude", "agents")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating agents directory: %w", err)
	}

	for _, filename := range agentFiles {
		srcPath := filepath.Join(srcDir, filename)
		destPath := filepath.Join(destDir, filename)

		if _, err := os.Stat(destPath); err == nil {
			log.Debug().Str("file", filename).Msg("agent definition already exists, skipping")
			continue
		}

		content, err := os.ReadFile(srcPath)
		if err != nil {
			return fmt.Errorf("reading agent definition %s: %w", filename, err)
		}

		if err := os.WriteFile(destPath, content, 0o644); err != nil {
			return fmt.Errorf("writing agent %s: %w", filename, err)
		}
		log.Debug().Str("file", filename).Msg("injected agent definition")
	}
	return nil
}

func CleanupAgents(worktreePath string) {
	for _, filename := range agentFiles {
		path := filepath.Join(worktreePath, ".claude", "agents", filename)
		_ = os.Remove(path)
	}
	agentsDir := filepath.Join(worktreePath, ".claude", "agents")
	_ = os.Remove(agentsDir)
	_ = os.Remove(filepath.Join(worktreePath, ".claude"))
}
