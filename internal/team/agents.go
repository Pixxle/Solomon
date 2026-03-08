package team

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/rs/zerolog/log"
)

// agentFiles lists the agent definition files shipped in the agents/ directory.
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

// getAgentsSourceDir finds the agents/ directory containing the shipped agent markdown files.
func getAgentsSourceDir() string {
	// Try relative to working directory
	if dir, err := os.Getwd(); err == nil {
		candidate := filepath.Join(dir, "agents")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Try relative to binary
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), "agents")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	// Try relative to source file
	_, filename, _, ok := runtime.Caller(0)
	if ok {
		candidate := filepath.Join(filepath.Dir(filename), "..", "..", "agents")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return "agents"
}

// InjectAgents copies agent definition files from the agents/ directory
// into the worktree's .claude/agents/ directory.
// Adds alongside existing files without overwriting them.
func InjectAgents(worktreePath string) error {
	srcDir := getAgentsSourceDir()
	destDir := filepath.Join(worktreePath, ".claude", "agents")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("creating agents directory: %w", err)
	}

	for _, filename := range agentFiles {
		srcPath := filepath.Join(srcDir, filename)
		destPath := filepath.Join(destDir, filename)

		// Don't overwrite existing files
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

// CleanupAgents removes the injected agent definitions from the worktree.
func CleanupAgents(worktreePath string) {
	for _, filename := range agentFiles {
		path := filepath.Join(worktreePath, ".claude", "agents", filename)
		_ = os.Remove(path)
	}
	// Try to remove the agents dir if empty
	agentsDir := filepath.Join(worktreePath, ".claude", "agents")
	_ = os.Remove(agentsDir)
	// Try to remove .claude dir if empty
	_ = os.Remove(filepath.Join(worktreePath, ".claude"))
}
