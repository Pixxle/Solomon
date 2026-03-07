<p align="center">
  <img src="assets/banner.png" alt="CodeHephaestus" width="700">
</p>

# CodeHephaestus

Autonomous coding agent that picks up tracker issues, runs an interactive planning conversation, implements with a multi-agent team, and creates GitHub PRs — powered by [Claude Code](https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/overview) agent teams.

## How it works

1. **Planning** — Discovers a To Do issue, posts clarifying questions as tracker comments, and iterates with the human until the spec is clear
2. **Implementation** — Launches a Claude Code agent team (team lead on Opus + specialist teammates on Sonnet) to implement the work
3. **Review** — Creates a draft PR, monitors CI, handles review feedback, and transitions the issue through to Done

## Prerequisites

- [Go](https://go.dev/dl/) 1.22+
- [Claude Code CLI](https://claude.ai/code) (`claude`) — installed and authenticated
- [GitHub CLI](https://cli.github.com) (`gh`) — installed and authenticated
- A Jira project with issues labeled for the bot

## Install

```bash
go build -o codehephaestus ./cmd/codehephaestus
cp .env.example .env  # fill in your credentials
```

## Usage

```
./codehephaestus              # Run continuously
./codehephaestus --once       # Single iteration
./codehephaestus --dry-run    # Show what would be done
./codehephaestus --verbose    # Debug logging
```

## Configuration

All configuration is via environment variables (loaded from `.env`). See [`.env.example`](.env.example) for the full list.

Key variables:

| Variable | Description |
|---|---|
| `TASK_TRACKER` | `jira` (Linear support planned) |
| `TRACKER_API_KEY` | Jira API token |
| `TRACKER_PROJECT` | Jira project key |
| `TRACKER_LABEL` | Label that marks issues for the bot |
| `BOT_DISPLAY_NAME` | Name used in comments and PRs (default: `CodeHephaestus`) |
| `PLANNING_MODEL` | Model for planning conversations (default: `sonnet`) |
| `TEAM_LEAD_MODEL` | Model for the team lead (default: `opus`) |

## License

[MIT](LICENSE)
