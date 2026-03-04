from __future__ import annotations

import logging
from dataclasses import dataclass
from enum import Enum

from codehephaestus.config import Settings
from codehephaestus.github.client import GitHubClient
from codehephaestus.trackers.base import TaskTracker
from codehephaestus.trackers.models import Issue
from codehephaestus.utils.loop_prevention import LoopPrevention

log = logging.getLogger("codehephaestus.priorities")


class Tier(str, Enum):
    REVIEW_FEEDBACK = "review_feedback"
    CI_FAILURE = "ci_failure"
    NEW_ISSUE = "new_issue"


@dataclass
class WorkItem:
    tier: Tier
    issue: Issue
    context: dict


class PriorityDispatcher:
    def __init__(
        self,
        tracker: TaskTracker,
        github: GitHubClient,
        settings: Settings,
        loop_prevention: LoopPrevention,
    ) -> None:
        self._tracker = tracker
        self._github = github
        self._settings = settings
        self._lp = loop_prevention

    async def check_merged_prs(self) -> None:
        """Housekeeping: transition merged PRs to Done."""
        log.info("Checking for merged PRs...")
        issues = []
        for status in (self._settings.status_in_review, self._settings.status_in_progress):
            issues.extend(await self._tracker.fetch_issues_by_status(status))
        for issue in issues:
            branch = self._tracker.get_issue_branch_name(issue)
            pr_number = await self._github.find_pr_for_branch(branch)
            if pr_number and await self._github.is_pr_merged(pr_number):
                log.info(
                    "PR #%d for %s is merged → transitioning to Done",
                    pr_number,
                    issue.key,
                )
                await self._tracker.transition_issue(
                    issue.key, self._settings.status_done
                )

    async def check_ci_passed(self) -> None:
        """Housekeeping: transition In Progress issues to In Review when CI passes."""
        log.info("Checking for CI-passing PRs...")
        issues = await self._tracker.fetch_issues_by_status(
            self._settings.status_in_progress
        )
        for issue in issues:
            branch = self._tracker.get_issue_branch_name(issue)
            pr_number = await self._github.find_pr_for_branch(branch)
            if not pr_number:
                continue
            status = await self._github.get_pr_check_status(pr_number)
            if status == "success":
                log.info(
                    "PR #%d for %s: CI passed → transitioning to %s",
                    pr_number,
                    issue.key,
                    self._settings.status_in_review,
                )
                await self._tracker.transition_issue(
                    issue.key, self._settings.status_in_review
                )

    async def find_next_work_item(self) -> WorkItem | None:
        # Priority 1: Review feedback
        item = await self._check_review_feedback()
        if item:
            return item

        # Priority 2: CI failures
        item = await self._check_ci_failures()
        if item:
            return item

        # Priority 3: New To Do issues
        item = await self._check_new_issues()
        if item:
            return item

        return None

    async def _check_review_feedback(self) -> WorkItem | None:
        log.info("Checking Priority 1: review feedback...")
        for status in (
            self._settings.status_in_review,
            self._settings.status_in_progress,
        ):
            issues = await self._tracker.fetch_issues_by_status(status)
            log.info("  Fetching '%s' issues... found %d", status, len(issues))

            for issue in issues:
                if self._lp.should_skip(issue.key):
                    continue

                cutoff = self._lp.get_feedback_cutoff(issue.key)
                since_str = cutoff.isoformat() if cutoff else None

                branch = self._tracker.get_issue_branch_name(issue)
                pr_number = await self._github.find_pr_for_branch(branch)

                all_comments: list[dict[str, str]] = []

                # Check GitHub PR comments (only reacted ones since cutoff)
                if pr_number:
                    pr_comments = await self._github.get_pr_comments(
                        pr_number, since=since_str
                    )
                    log.info(
                        "  %s: checking PR #%d for reacted comments (since %s)... %d found",
                        issue.key,
                        pr_number,
                        since_str or "beginning",
                        len(pr_comments),
                    )
                    for c in pr_comments:
                        all_comments.append(
                            {
                                "author": c.author,
                                "body": c.body,
                                "source": "GitHub PR",
                                "reaction": c.reaction,
                            }
                        )

                if all_comments:
                    log.info(
                        "→ Selected: %s (review feedback, %d comments)",
                        issue.key,
                        len(all_comments),
                    )
                    return WorkItem(
                        tier=Tier.REVIEW_FEEDBACK,
                        issue=issue,
                        context={"comments": all_comments},
                    )

        return None

    async def _check_ci_failures(self) -> WorkItem | None:
        log.info("Checking Priority 2: CI failures...")
        for status in (
            self._settings.status_in_progress,
            self._settings.status_in_review,
        ):
            issues = await self._tracker.fetch_issues_by_status(status)
            for issue in issues:
                if self._lp.should_skip(issue.key):
                    continue

                branch = self._tracker.get_issue_branch_name(issue)
                pr_number = await self._github.find_pr_for_branch(branch)
                if not pr_number:
                    continue

                check_status = await self._github.get_pr_check_status(pr_number)
                if check_status == "failure":
                    log.info(
                        "→ Selected: %s (CI failure on PR #%d)",
                        issue.key,
                        pr_number,
                    )
                    return WorkItem(
                        tier=Tier.CI_FAILURE,
                        issue=issue,
                        context={"pr_number": pr_number, "check_output": ""},
                    )

        return None

    async def _check_new_issues(self) -> WorkItem | None:
        log.info("Checking Priority 3: new To Do issues...")
        issues = await self._tracker.fetch_issues_by_status(
            self._settings.status_todo
        )
        log.info("  Found %d To Do issues", len(issues))

        for issue in issues:
            if self._lp.should_skip(issue.key):
                continue
            log.info("→ Selected: %s (new issue)", issue.key)
            return WorkItem(
                tier=Tier.NEW_ISSUE,
                issue=issue,
                context={},
            )

        return None
