from __future__ import annotations

import asyncio
import logging
from pathlib import Path

log = logging.getLogger("codehephaestus.git")


async def _run(cmd: list[str], cwd: str | Path | None = None) -> str:
    proc = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        cwd=cwd,
    )
    stdout, stderr = await proc.communicate()
    if proc.returncode != 0:
        raise RuntimeError(
            f"Command {cmd!r} failed (rc={proc.returncode}): {stderr.decode().strip()}"
        )
    return stdout.decode().strip()


async def _run_quiet(cmd: list[str], cwd: str | Path | None = None) -> tuple[int, str]:
    """Run a command, returning (exit_code, stdout) without raising on failure."""
    proc = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        cwd=cwd,
    )
    stdout, _ = await proc.communicate()
    return proc.returncode or 0, stdout.decode().strip()


async def update_main(repo_path: str) -> None:
    """Fetch origin and fast-forward the main branch so new worktrees are up to date."""
    log.info("Fetching origin...")
    await _run(["git", "fetch", "origin"], cwd=repo_path)

    # Determine the default branch (main or master)
    rc, default_branch = await _run_quiet(
        ["git", "symbolic-ref", "refs/remotes/origin/HEAD"], cwd=repo_path
    )
    if rc == 0 and default_branch:
        # refs/remotes/origin/main → main
        default_branch = default_branch.split("/")[-1]
    else:
        default_branch = "main"

    # Fast-forward main without checking it out (works even when on another branch)
    rc, _ = await _run_quiet(
        ["git", "update-ref", f"refs/heads/{default_branch}", f"refs/remotes/origin/{default_branch}"],
        cwd=repo_path,
    )
    if rc == 0:
        log.info("Fast-forwarded %s to origin/%s", default_branch, default_branch)
    else:
        log.warning("Could not fast-forward %s (may have local commits)", default_branch)


async def ensure_worktree(branch: str, repo_path: str, worktree_path: str = "") -> str:
    """Create a git worktree for the branch. Returns the worktree path.

    Uses worktrees so the main repo working tree stays on its current branch.
    New branches are created from HEAD (which should be up-to-date main after update_main).
    If worktree_path is set, worktrees are created there instead of inside the repo.
    """
    base = Path(worktree_path) if worktree_path else Path(repo_path) / ".worktrees"
    wt_dir = base / branch.replace("/", "_")
    if wt_dir.exists():
        # Worktree exists — pull latest for the branch
        log.info("Worktree already exists at %s, pulling latest...", wt_dir)
        rc, _ = await _run_quiet(["git", "pull", "--ff-only", "origin", branch], cwd=str(wt_dir))
        if rc != 0:
            log.debug("Pull failed for %s (may not exist on remote yet), continuing", branch)
        return str(wt_dir)

    wt_dir.parent.mkdir(parents=True, exist_ok=True)

    # Check if branch already exists (locally or on remote)
    rc, _ = await _run_quiet(["git", "rev-parse", "--verify", branch], cwd=repo_path)
    if rc == 0:
        # Branch exists — create worktree pointing to it
        await _run(["git", "worktree", "add", str(wt_dir), branch], cwd=repo_path)
        log.info("Created worktree at %s for existing branch %s", wt_dir, branch)
    else:
        # Check remote
        rc, _ = await _run_quiet(
            ["git", "rev-parse", "--verify", f"origin/{branch}"], cwd=repo_path
        )
        if rc == 0:
            # Exists on remote — create local tracking branch in worktree
            await _run(
                ["git", "worktree", "add", "--track", "-b", branch, str(wt_dir), f"origin/{branch}"],
                cwd=repo_path,
            )
            log.info("Created worktree at %s tracking origin/%s", wt_dir, branch)
        else:
            # Brand new branch — create from HEAD
            await _run(
                ["git", "worktree", "add", "-b", branch, str(wt_dir)],
                cwd=repo_path,
            )
            log.info("Created worktree at %s with new branch %s", wt_dir, branch)

    return str(wt_dir)


async def cleanup_worktree(branch: str, repo_path: str, worktree_path: str = "") -> None:
    """Remove a worktree after work is done."""
    base = Path(worktree_path) if worktree_path else Path(repo_path) / ".worktrees"
    wt_dir = base / branch.replace("/", "_")
    if wt_dir.exists():
        await _run(["git", "worktree", "remove", str(wt_dir), "--force"], cwd=repo_path)
        log.info("Cleaned up worktree at %s", wt_dir)
    # Also prune stale worktree references
    await _run(["git", "worktree", "prune"], cwd=repo_path)


async def get_current_sha(cwd: str) -> str:
    return await _run(["git", "rev-parse", "HEAD"], cwd=cwd)
