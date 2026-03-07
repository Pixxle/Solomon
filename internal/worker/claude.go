package worker

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/rs/zerolog/log"
)

type ClaudeResult struct {
	ExitCode int
	Output   string
}

// RunClaude runs a claude --print session with the given prompt.
func RunClaude(ctx context.Context, prompt, workingDir, model string) (*ClaudeResult, error) {
	args := []string{"--dangerously-skip-permissions", "--print"}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workingDir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "PUSHOVER_ENABLED=false")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Debug().
		Str("model", model).
		Str("cwd", workingDir).
		Int("prompt_len", len(prompt)).
		Msg("launching claude session")

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("running claude: %w", err)
		}
	}

	return &ClaudeResult{
		ExitCode: exitCode,
		Output:   stdout.String(),
	}, nil
}

// RunClaudeAgentTeam launches a claude session with agent teams enabled.
func RunClaudeAgentTeam(ctx context.Context, prompt, workingDir, model string, timeout time.Duration) (*ClaudeResult, error) {
	args := []string{"--dangerously-skip-permissions", "--print"}
	if model != "" {
		args = append(args, "--model", model)
	}

	teamCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(teamCtx, "claude", args...)
	cmd.Dir = workingDir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(),
		"CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1",
		"PUSHOVER_ENABLED=false",
	)
	// Use process group so we can kill all child processes
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Debug().
		Str("model", model).
		Str("cwd", workingDir).
		Dur("timeout", timeout).
		Msg("launching agent team session")

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if teamCtx.Err() != nil {
			// Timeout - kill the process group
			if cmd.Process != nil {
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
				time.Sleep(5 * time.Second)
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			}
			return nil, fmt.Errorf("agent team timed out after %v", timeout)
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("running agent team: %w", err)
		}
	}

	return &ClaudeResult{
		ExitCode: exitCode,
		Output:   stdout.String(),
	}, nil
}

// RunClaudeText runs a claude --print session for pure text generation (no tool use).
// Used for planning, PR descriptions, answering questions — anything that doesn't need
// to read/edit files or run commands.
func RunClaudeText(ctx context.Context, prompt, workingDir, model string) (*ClaudeResult, error) {
	args := []string{"--print", "--max-turns", "1"}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Dir = workingDir
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "PUSHOVER_ENABLED=false")

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	log.Debug().
		Str("model", model).
		Str("cwd", workingDir).
		Int("prompt_len", len(prompt)).
		Msg("launching claude text session")

	err := cmd.Run()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		} else {
			return nil, fmt.Errorf("running claude text: %w", err)
		}
	}

	return &ClaudeResult{
		ExitCode: exitCode,
		Output:   stdout.String(),
	}, nil
}

// RunClaudeQuick runs a quick claude --print call for yes/no classification (no tool use).
func RunClaudeQuick(ctx context.Context, prompt, model string) (string, error) {
	args := []string{"--print", "--max-turns", "1"}
	if model != "" {
		args = append(args, "--model", model)
	}

	cmd := exec.CommandContext(ctx, "claude", args...)
	cmd.Stdin = strings.NewReader(prompt)
	cmd.Env = append(os.Environ(), "PUSHOVER_ENABLED=false")

	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("running claude quick: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// GracefulStop sends SIGTERM to a process and waits up to gracePeriod before SIGKILL.
func GracefulStop(cmd *exec.Cmd, gracePeriod time.Duration) {
	if cmd == nil || cmd.Process == nil {
		return
	}

	_ = cmd.Process.Signal(syscall.SIGTERM)

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	select {
	case <-done:
		return
	case <-time.After(gracePeriod):
		_ = cmd.Process.Kill()
	}
}
