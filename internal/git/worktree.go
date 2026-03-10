package git

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func UpdateMain(ctx context.Context, repoPath string) error {
	// Just fetch — this updates origin/main (or origin/master) without
	// touching the local branch, so it works even when main is checked out.
	if err := run(ctx, repoPath, "git", "fetch", "origin"); err != nil {
		return fmt.Errorf("fetching origin: %w", err)
	}
	return nil
}

func EnsureWorktree(ctx context.Context, branch, repoPath, worktreeBase string) (string, error) {
	safeName := strings.ReplaceAll(branch, "/", "_")
	wtDir := filepath.Join(worktreeBase, safeName)

	// Check if worktree already exists
	out, err := output(ctx, repoPath, "git", "worktree", "list", "--porcelain")
	if err == nil && strings.Contains(out, wtDir) {
		return wtDir, nil
	}

	// Check if branch exists locally
	if err := run(ctx, repoPath, "git", "rev-parse", "--verify", branch); err == nil {
		// Local branch exists, create worktree from it
		if err := run(ctx, repoPath, "git", "worktree", "add", wtDir, branch); err != nil {
			return "", fmt.Errorf("creating worktree from local branch: %w", err)
		}
		return wtDir, nil
	}

	// Check if branch exists on remote
	if err := run(ctx, repoPath, "git", "rev-parse", "--verify", "origin/"+branch); err == nil {
		// Remote branch exists
		if err := run(ctx, repoPath, "git", "worktree", "add", wtDir, "-b", branch, "origin/"+branch); err != nil {
			return "", fmt.Errorf("creating worktree from remote branch: %w", err)
		}
		return wtDir, nil
	}

	// New branch from main
	baseBranch := "main"
	if err := run(ctx, repoPath, "git", "rev-parse", "--verify", "main"); err != nil {
		baseBranch = "master"
	}
	if err := run(ctx, repoPath, "git", "worktree", "add", wtDir, "-b", branch, baseBranch); err != nil {
		return "", fmt.Errorf("creating worktree for new branch: %w", err)
	}
	return wtDir, nil
}

func CleanupWorktree(ctx context.Context, branch, repoPath, worktreeBase string) error {
	safeName := strings.ReplaceAll(branch, "/", "_")
	wtDir := filepath.Join(worktreeBase, safeName)

	_ = run(ctx, repoPath, "git", "worktree", "remove", "--force", wtDir)
	_ = run(ctx, repoPath, "git", "worktree", "prune")
	return nil
}

// EnsureWorktreeWithIdentity creates or reuses a worktree and configures the
// git author identity so commits are attributed to the bot.
func EnsureWorktreeWithIdentity(ctx context.Context, branch, repoPath, worktreeBase, userName, userEmail string) (string, error) {
	wtDir, err := EnsureWorktree(ctx, branch, repoPath, worktreeBase)
	if err != nil {
		return "", err
	}
	if userName != "" && userEmail != "" {
		if err := configureIdentity(ctx, wtDir, userName, userEmail); err != nil {
			return wtDir, fmt.Errorf("configuring git identity: %w", err)
		}
	}
	return wtDir, nil
}

func configureIdentity(ctx context.Context, dir, name, email string) error {
	if err := run(ctx, dir, "git", "config", "user.name", name); err != nil {
		return fmt.Errorf("setting git user.name: %w", err)
	}
	if err := run(ctx, dir, "git", "config", "user.email", email); err != nil {
		return fmt.Errorf("setting git user.email: %w", err)
	}
	return nil
}

func GetCurrentSHA(ctx context.Context, cwd string) (string, error) {
	out, err := output(ctx, cwd, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// DiffFromMain returns the diff between main (or master) and HEAD.
func DiffFromMain(ctx context.Context, cwd string) (string, error) {
	return output(ctx, cwd, "git", "diff", "main...HEAD")
}

// CommitLogFromMain returns the oneline commit log between main and HEAD.
func CommitLogFromMain(ctx context.Context, cwd string) (string, error) {
	return output(ctx, cwd, "git", "log", "main...HEAD", "--oneline")
}

func run(ctx context.Context, dir string, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, string(out))
	}
	return nil
}

func output(ctx context.Context, dir string, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}
