from __future__ import annotations

import logging
import re
from datetime import datetime, timezone
from pathlib import Path

import httpx

from codehephaestus.config import Settings
from codehephaestus.trackers.base import TaskTracker
from codehephaestus.trackers.models import Comment, Issue

log = logging.getLogger("codehephaestus.linear")

API_URL = "https://api.linear.app/graphql"


def _slugify(text: str, max_len: int = 40) -> str:
    slug = re.sub(r"[^a-zA-Z0-9]+", "-", text).strip("-").lower()
    return slug[:max_len].rstrip("-")


def _parse_datetime(value: str | None) -> datetime | None:
    if not value:
        return None
    try:
        return datetime.fromisoformat(value)
    except ValueError:
        return None


class LinearTracker(TaskTracker):
    def __init__(self, settings: Settings) -> None:
        self._settings = settings
        self._team_key = settings.tracker_project
        self._label = settings.tracker_label
        self._client = httpx.AsyncClient(
            headers={
                "Authorization": settings.tracker_api_key,
                "Content-Type": "application/json",
            },
            timeout=30.0,
        )
        self._team_id: str | None = None
        self._state_map: dict[str, str] = {}  # state name -> state id

    async def _gql(self, query: str, variables: dict | None = None) -> dict:
        payload: dict = {"query": query}
        if variables:
            payload["variables"] = variables
        resp = await self._client.post(API_URL, json=payload)
        resp.raise_for_status()
        body = resp.json()
        if body.get("errors"):
            raise RuntimeError(f"Linear GraphQL errors: {body['errors']}")
        return body["data"]

    async def _ensure_team_loaded(self) -> None:
        if self._team_id:
            return
        data = await self._gql(
            """
            query($key: String!) {
                teams(filter: { key: { eq: $key } }) {
                    nodes {
                        id
                        states { nodes { id name } }
                    }
                }
            }
            """,
            {"key": self._team_key},
        )
        nodes = data["teams"]["nodes"]
        if not nodes:
            raise RuntimeError(f"Linear team with key '{self._team_key}' not found")
        team = nodes[0]
        self._team_id = team["id"]
        self._state_map = {s["name"]: s["id"] for s in team["states"]["nodes"]}
        log.debug("Loaded team %s with states: %s", self._team_key, list(self._state_map))

    async def validate_connection(self) -> bool:
        data = await self._gql("{ viewer { id name email } }")
        viewer = data["viewer"]
        log.info(
            "Connected as %s (%s) | Team: %s | Label: %s",
            viewer.get("name", "unknown"),
            viewer.get("email", ""),
            self._team_key,
            self._label,
        )
        return True

    async def fetch_issues_by_status(self, status: str) -> list[Issue]:
        await self._ensure_team_loaded()
        data = await self._gql(
            """
            query($teamKey: String!, $stateName: String!, $label: String!) {
                issues(filter: {
                    team: { key: { eq: $teamKey } }
                    state: { name: { eq: $stateName } }
                    labels: { some: { name: { eq: $label } } }
                }) {
                    nodes {
                        id
                        identifier
                        title
                        description
                        state { name }
                        labels { nodes { name } }
                        createdAt
                        updatedAt
                    }
                }
            }
            """,
            {
                "teamKey": self._team_key,
                "stateName": status,
                "label": self._label,
            },
        )
        issues: list[Issue] = []
        for node in data["issues"]["nodes"]:
            issues.append(
                Issue(
                    key=node["identifier"],
                    title=node["title"],
                    description=node.get("description") or "",
                    status=node.get("state", {}).get("name", ""),
                    labels=[l["name"] for l in node.get("labels", {}).get("nodes", [])],
                    created=_parse_datetime(node.get("createdAt")),
                    updated=_parse_datetime(node.get("updatedAt")),
                )
            )
        return issues

    async def transition_issue(self, issue_key: str, to_status: str) -> None:
        await self._ensure_team_loaded()
        state_id = self._state_map.get(to_status)
        if not state_id:
            log.warning(
                "%s: state '%s' not found. Available: %s",
                issue_key,
                to_status,
                list(self._state_map),
            )
            return

        issue_id = await self._get_issue_id(issue_key)

        await self._gql(
            """
            mutation($id: String!, $stateId: String!) {
                issueUpdate(id: $id, input: { stateId: $stateId }) {
                    issue { id identifier state { name } }
                }
            }
            """,
            {"id": issue_id, "stateId": state_id},
        )
        log.info("%s: transitioned → %s", issue_key, to_status)

    async def _get_issue_id(self, issue_key: str) -> str:
        """Resolve a Linear identifier (e.g. PROJ-123) to its internal UUID."""
        number = int(issue_key.split("-")[-1])
        data = await self._gql(
            """
            query($teamKey: String!, $number: Float!) {
                issues(filter: {
                    team: { key: { eq: $teamKey } }
                    number: { eq: $number }
                }) { nodes { id } }
            }
            """,
            {"teamKey": self._team_key, "number": number},
        )
        nodes = data["issues"]["nodes"]
        if not nodes:
            raise RuntimeError(f"Linear issue '{issue_key}' not found")
        return nodes[0]["id"]

    def get_issue_branch_name(self, issue: Issue) -> str:
        return f"codehephaestus/{issue.key}-{_slugify(issue.title)}"

    async def get_comments(self, issue_key: str) -> list[Comment]:
        issue_id = await self._get_issue_id(issue_key)
        data = await self._gql(
            """
            query($id: String!) {
                issue(id: $id) {
                    comments {
                        nodes {
                            id
                            body
                            createdAt
                            user { displayName }
                            botActor { name }
                        }
                    }
                }
            }
            """,
            {"id": issue_id},
        )
        comments: list[Comment] = []
        for c in data["issue"]["comments"]["nodes"]:
            author = "unknown"
            if c.get("user") and c["user"].get("displayName"):
                author = c["user"]["displayName"]
            elif c.get("botActor") and c["botActor"].get("name"):
                author = c["botActor"]["name"]
            comments.append(
                Comment(
                    id=c["id"],
                    author=author,
                    body=c.get("body", ""),
                    created=_parse_datetime(c.get("createdAt")),
                )
            )
        return comments

    async def add_comment(self, issue_key: str, body: str) -> None:
        issue_id = await self._get_issue_id(issue_key)
        await self._gql(
            """
            mutation($issueId: String!, $body: String!) {
                commentCreate(input: { issueId: $issueId, body: $body }) {
                    comment { id }
                }
            }
            """,
            {"issueId": issue_id, "body": body},
        )
        log.debug("%s: comment added", issue_key)

    async def attach_file(self, issue_key: str, file_path: str) -> None:
        path = Path(file_path)
        file_bytes = path.read_bytes()
        content_type = "application/octet-stream"

        # Step 1: Get presigned upload URL
        data = await self._gql(
            """
            mutation($filename: String!, $contentType: String!, $size: Int!) {
                fileUpload(filename: $filename, contentType: $contentType, size: $size) {
                    uploadFile {
                        uploadUrl
                        assetUrl
                    }
                }
            }
            """,
            {
                "filename": path.name,
                "contentType": content_type,
                "size": len(file_bytes),
            },
        )
        upload_info = data["fileUpload"]["uploadFile"]
        upload_url = upload_info["uploadUrl"]
        asset_url = upload_info["assetUrl"]

        # Step 2: PUT file to presigned URL
        put_resp = await self._client.put(
            upload_url,
            content=file_bytes,
            headers={"Content-Type": content_type},
        )
        put_resp.raise_for_status()

        # Step 3: Add comment with link to the uploaded file
        issue_id = await self._get_issue_id(issue_key)
        await self._gql(
            """
            mutation($issueId: String!, $body: String!) {
                commentCreate(input: { issueId: $issueId, body: $body }) {
                    comment { id }
                }
            }
            """,
            {
                "issueId": issue_id,
                "body": f"[{path.name}]({asset_url})",
            },
        )
        log.info("%s: attached %s", issue_key, path.name)

    async def close(self) -> None:
        await self._client.aclose()
