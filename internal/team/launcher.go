package team

import (
	"context"
	"fmt"
	"time"

	"github.com/rs/zerolog/log"

	"github.com/pixxle/codehephaestus/internal/config"
	"github.com/pixxle/codehephaestus/internal/worker"
)

type Launcher struct {
	cfg *config.Config
}

func NewLauncher(cfg *config.Config) *Launcher {
	return &Launcher{cfg: cfg}
}

// LaunchTeam starts an agent team session in the worktree with the given prompt.
func (l *Launcher) LaunchTeam(ctx context.Context, prompt, worktreePath string) (*worker.ClaudeResult, error) {
	// Inject agent definitions
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

	result, err := worker.RunClaudeAgentTeam(ctx, prompt, worktreePath, l.cfg.TeamLeadModel, timeout)
	if err != nil {
		return nil, fmt.Errorf("agent team session failed: %w", err)
	}

	if result.ExitCode != 0 {
		log.Warn().Int("exit_code", result.ExitCode).Msg("agent team exited with non-zero code")
	}

	return result, nil
}
