<p align="center">
  <img src="assets/banner.png" alt="Solomon" width="700">
</p>

# Solomon

Autonomous coding agent that picks up tracker issues, runs an interactive planning conversation, implements with a multi-agent team, and creates GitHub PRs — powered by [Claude Code](https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/overview) agent teams.

## How it works

Solomon runs as a long-lived process with a plugin architecture. Each plugin operates on its own cron schedule.

### Developer plugin

1. **Planning** — Discovers a To Do issue, posts clarifying questions as tracker comments, and iterates with the human until the spec is clear
2. **Implementation** — Launches a Claude Code agent team (team lead on Opus + specialist teammates on Sonnet) to implement the work
3. **Review** — Creates a draft PR, monitors CI, handles review feedback, and transitions the issue through to Done

### Security engineer plugin

1. **Scanning** — Runs 9 external security tools and 8 LLM-powered analysis agents against the codebase
2. **Consolidation** — Deduplicates and prioritises findings into a single report
3. **Ticketing** — Automatically creates Jira tickets for findings above a configurable severity threshold

## Prerequisites

- [Go](https://go.dev/dl/) 1.26+
- [Claude Code CLI](https://claude.ai/code) (`claude`) — installed and authenticated
- [GitHub CLI](https://cli.github.com) (`gh`) — installed and authenticated
- A Jira project with issues labeled for the bot

Requires `CLAUDE_CODE_EXPERIMENTAL_AGENT_TEAMS=1` in your environment for multi-agent implementation.

## Install

```bash
go build -o solomon ./cmd/solomon
cp solomon.yaml.example solomon.yaml  # edit with your settings
cp .env.example .env                  # add secrets here
```

## Usage

```
./solomon                              # Run continuously
./solomon --once                       # Single iteration and exit
./solomon --dry-run                    # Show what would be done
./solomon --verbose                    # Debug logging
./solomon --config solomon.yaml        # Custom config file path
./solomon --plugin developer           # Run only one plugin
```

## Configuration

Solomon uses a YAML config file (`solomon.yaml`) for structure and an `.env` file for secrets. The YAML file references environment variables with `${VAR_NAME}` syntax.

See [`solomon.yaml.example`](solomon.yaml.example) for the full annotated config and [`.env.example`](.env.example) for required secrets.

### Quick reference

| Section | Key settings |
|---|---|
| `global.claude` | `team_lead_model`, `teammate_model`, `agent_team_timeout` |
| `global.slack` | `bot_token`, `channel_id`, `standup_enabled` |
| `global.planning` | `reminder_days`, `timeout_action` |
| `boards[]` | Tracker type, base URL, project, statuses, labels |
| `plugins[].schedules` | Cron expressions controlling how often each plugin runs |
| `plugins[].settings` | Plugin-specific settings (see example file) |

### Plugin settings

**developer:**

| Setting | Default | Description |
|---|---|---|
| `auto_launch` | `false` | Start implementation automatically after planning completes |
| `max_review_rounds` | `3` | Maximum PR review/fix cycles |
| `max_ci_fix_attempts` | `3` | Maximum CI failure fix attempts |
| `max_uat_retries` | `2` | Maximum UAT retry attempts |

**securityengineer:**

| Setting | Default | Description |
|---|---|---|
| `parallel_agents` | `4` | Number of concurrent security analysis agents |
| `jira_threshold` | `HIGH` | Minimum severity to create Jira tickets (`CRITICAL`, `HIGH`, `MEDIUM`, `LOW`) |

## License

[MIT](LICENSE)
