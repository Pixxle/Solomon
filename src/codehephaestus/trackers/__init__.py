from __future__ import annotations

from codehephaestus.config import Settings, TaskTrackerType
from codehephaestus.trackers.base import TaskTracker
from codehephaestus.trackers.jira import JiraTracker
from codehephaestus.trackers.linear import LinearTracker


def create_tracker(settings: Settings) -> TaskTracker:
    if settings.task_tracker == TaskTrackerType.JIRA:
        return JiraTracker(settings)
    if settings.task_tracker == TaskTrackerType.LINEAR:
        return LinearTracker(settings)
    raise ValueError(f"Unsupported tracker: {settings.task_tracker}")


__all__ = ["TaskTracker", "create_tracker"]
