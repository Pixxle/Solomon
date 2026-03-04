from __future__ import annotations

import asyncio
import json
import logging
import re
from dataclasses import dataclass

log = logging.getLogger("codehephaestus.github")


async def _run(
    cmd: list[str], cwd: str | None = None
) -> tuple[int, str, str]:
    proc = await asyncio.create_subprocess_exec(
        *cmd,
        stdout=asyncio.subprocess.PIPE,
        stderr=asyncio.subprocess.PIPE,
        cwd=cwd,
    )
    stdout, stderr = await proc.communicate()
    return proc.returncode, stdout.decode().strip(), stderr.decode().strip()


async def _run_ok(cmd: list[str], cwd: str | None = None) -> str:
    rc, out, err = await _run(cmd, cwd=cwd)
    if rc != 0:
        raise RuntimeError(f"Command {cmd!r} failed (rc={rc}): {err}")
    return out


@dataclass
class PRComment:
    id: int
    author: str
    body: str
    created_at: str


class GitHubClient:
    def __init__(self, repo_path: str) -> None:
        self._cwd = repo_path
        self._username: str | None = None

    async def validate_auth(self) -> str:
        """Return the authenticated GitHub username."""
        out = await _run_ok(["gh", "auth", "status", "--active"], cwd=self._cwd)
        log.debug("gh auth status: %s", out)
        # Extract username from status output
        user_out = await _run_ok(
            ["gh", "api", "/user", "--jq", ".login"], cwd=self._cwd
        )
        self._username = user_out.strip()
        return self._username

    async def find_pr_for_branch(self, branch: str) -> int | None:
        rc, out, _ = await _run(
            ["gh", "pr", "list", "--head", branch, "--json", "number", "--limit", "1"],
            cwd=self._cwd,
        )
        if rc != 0 or not out:
            return None
        prs = json.loads(out)
        return prs[0]["number"] if prs else None

    async def get_pr_comments(
        self, pr_number: int, since: str | None = None
    ) -> list[PRComment]:
        """Get review comments and issue comments on a PR."""
        comments: list[PRComment] = []

        # Review comments
        out = await _run_ok(
            [
                "gh",
                "api",
                f"/repos/{{owner}}/{{repo}}/pulls/{pr_number}/comments",
                "--jq",
                ".[] | {id: .id, author: .user.login, body: .body, created_at: .created_at}",
            ],
            cwd=self._cwd,
        )
        for line in out.strip().splitlines():
            if not line:
                continue
            try:
                c = json.loads(line)
                if since and c["created_at"] <= since:
                    continue
                if c["author"] == self._username or c["author"].endswith("[bot]"):
                    continue
                comments.append(
                    PRComment(
                        id=c["id"], author=c["author"], body=c["body"], created_at=c["created_at"]
                    )
                )
            except (json.JSONDecodeError, KeyError):
                continue

        # Issue comments on PR
        out2 = await _run_ok(
            [
                "gh",
                "api",
                f"/repos/{{owner}}/{{repo}}/issues/{pr_number}/comments",
                "--jq",
                ".[] | {id: .id, author: .user.login, body: .body, created_at: .created_at}",
            ],
            cwd=self._cwd,
        )
        for line in out2.strip().splitlines():
            if not line:
                continue
            try:
                c = json.loads(line)
                if since and c["created_at"] <= since:
                    continue
                if c["author"] == self._username or c["author"].endswith("[bot]"):
                    continue
                comments.append(
                    PRComment(
                        id=c["id"], author=c["author"], body=c["body"], created_at=c["created_at"]
                    )
                )
            except (json.JSONDecodeError, KeyError):
                continue

        return comments

    async def get_pr_check_status(self, pr_number: int) -> str:
        """Return 'success', 'failure', or 'pending'."""
        rc, out, _ = await _run(
            [
                "gh",
                "pr",
                "checks",
                str(pr_number),
                "--json",
                "state",
                "--jq",
                ".[].state",
            ],
            cwd=self._cwd,
        )
        if rc != 0 or not out:
            return "pending"
        states = set(out.strip().splitlines())
        if not states:
            return "pending"
        if "FAILURE" in states or "ERROR" in states:
            return "failure"
        if states <= {"SUCCESS", "SKIPPED"}:
            return "success"
        return "pending"

    async def is_pr_merged(self, pr_number: int) -> bool:
        out = await _run_ok(
            [
                "gh",
                "pr",
                "view",
                str(pr_number),
                "--json",
                "mergedAt",
                "--jq",
                ".mergedAt",
            ],
            cwd=self._cwd,
        )
        return out.strip() != "" and out.strip() != "null"

    async def create_pr(
        self,
        title: str,
        body: str,
        branch: str,
        *,
        draft: bool = True,
    ) -> int:
        out = await _run_ok(
            [
                "gh",
                "pr",
                "create",
                "--title",
                title,
                "--body",
                body,
                "--head",
                branch,
                *(["--draft"] if draft else []),
            ],
            cwd=self._cwd,
        )
        # gh pr create outputs the PR URL, e.g. https://github.com/org/repo/pull/123
        match = re.search(r"/pull/(\d+)", out.strip())
        if not match:
            raise RuntimeError(f"Could not parse PR number from gh output: {out!r}")
        pr_number = int(match.group(1))
        log.info("Created PR #%d for branch %s", pr_number, branch)
        return pr_number

    async def post_pr_comment(self, pr_number: int, body: str) -> None:
        """Post a comment on a PR. Uses --body-file to avoid shell truncation."""
        proc = await asyncio.create_subprocess_exec(
            "gh", "pr", "comment", str(pr_number), "--body-file", "-",
            stdin=asyncio.subprocess.PIPE,
            stdout=asyncio.subprocess.PIPE,
            stderr=asyncio.subprocess.PIPE,
            cwd=self._cwd,
        )
        _, stderr = await proc.communicate(input=body.encode())
        if proc.returncode != 0:
            raise RuntimeError(
                f"gh pr comment failed (rc={proc.returncode}): {stderr.decode().strip()}"
            )
        log.info("Posted comment on PR #%d", pr_number)

    async def push_branch(self, branch: str) -> None:
        await _run_ok(
            ["git", "push", "--set-upstream", "origin", branch],
            cwd=self._cwd,
        )
        log.info("Pushed %s to origin", branch)
