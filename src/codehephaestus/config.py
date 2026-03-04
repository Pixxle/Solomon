from __future__ import annotations

from enum import Enum

from pydantic import Field
from pydantic_settings import BaseSettings, SettingsConfigDict


class TaskTrackerType(str, Enum):
    JIRA = "jira"


class Settings(BaseSettings):
    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    task_tracker: TaskTrackerType = TaskTrackerType.JIRA

    tracker_api_key: str
    tracker_base_url: str
    tracker_project: str
    tracker_label: str = "codehephaestus"
    tracker_email: str = ""

    # Jira status mapping
    jira_status_todo: str = "To Do"
    jira_status_in_progress: str = "In Progress"
    jira_status_in_review: str = "In Review"
    jira_status_done: str = "Done"

    poll_interval: int = 120
    max_iterations: int = Field(default=0, description="0 = infinite")
    tool: str = "claude"
    target_repo_path: str = "."
    worktree_path: str = ""

    # CLI overrides (not from .env)
    dry_run: bool = False
    verbose: bool = False
