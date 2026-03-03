from __future__ import annotations

import logging
import time
from dataclasses import dataclass, field
from datetime import datetime, timezone

log = logging.getLogger("codehephaestus.loop_prevention")

MAX_ATTEMPTS = 5
WINDOW_SECONDS = 600  # 10 minutes


@dataclass
class AttemptRecord:
    timestamps: list[float] = field(default_factory=list)


class LoopPrevention:
    def __init__(
        self,
        max_attempts: int = MAX_ATTEMPTS,
        window_seconds: float = WINDOW_SECONDS,
    ) -> None:
        self._max_attempts = max_attempts
        self._window = window_seconds
        self._attempts: dict[str, AttemptRecord] = {}
        self._processed_shas: set[str] = set()
        self._feedback_cutoffs: dict[str, datetime] = {}

    def should_skip(self, issue_key: str) -> bool:
        record = self._attempts.get(issue_key)
        if not record:
            return False
        now = time.monotonic()
        recent = [t for t in record.timestamps if now - t < self._window]
        record.timestamps = recent
        if len(recent) >= self._max_attempts:
            log.warning(
                "%s: skipping — %d attempts in last %ds",
                issue_key,
                len(recent),
                int(self._window),
            )
            return True
        return False

    def record_attempt(self, issue_key: str) -> None:
        if issue_key not in self._attempts:
            self._attempts[issue_key] = AttemptRecord()
        self._attempts[issue_key].timestamps.append(time.monotonic())

    def is_sha_processed(self, sha: str) -> bool:
        return sha in self._processed_shas

    def mark_sha_processed(self, sha: str) -> None:
        self._processed_shas.add(sha)

    def get_feedback_cutoff(self, issue_key: str) -> datetime | None:
        """Get the cutoff timestamp — only comments after this are 'new'."""
        return self._feedback_cutoffs.get(issue_key)

    def mark_feedback_processed(self, issue_key: str) -> None:
        """Record that we've addressed all feedback up to now."""
        self._feedback_cutoffs[issue_key] = datetime.now(timezone.utc)
