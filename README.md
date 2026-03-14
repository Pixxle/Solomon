<p align="center">
  <img src="assets/banner.png" alt="Solomon" width="700">
</p>

# Solomon

Autonomous coding agent that picks up tracker issues, runs an interactive planning conversation, implements with a multi-agent team, and creates GitHub PRs — powered by [Claude Code](https://docs.anthropic.com/en/docs/agents-and-tools/claude-code/overview) agent teams.

## How it works

Solomon runs as a long-lived process with a plugin architecture. Each plugin operates on its own cron schedule.

## Prerequisites

- [Go](https://go.dev/dl/) 1.26+
- [Claude Code CLI](https://claude.ai/code) (`claude`) — installed and authenticated
- [GitHub CLI](https://cli.github.com) (`gh`) — installed and authenticated
- A Jira or Linear project with issues labeled for the bot

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

Solomon uses a YAML config file (`solomon.yaml`) for structure and an `.env` file for secrets.

### Environment variable expansion

The YAML file supports `${VAR_NAME}` syntax to reference environment variables. Variables are expanded before YAML parsing, so they can be used anywhere in the file. Unresolved variables are left as-is. Secrets should always live in `.env`, not in the YAML file.

```yaml
boards:
  - api_key: ${JIRA_API_KEY}   # replaced with the value of JIRA_API_KEY from .env
```

See [`.env.example`](.env.example) for all supported environment variables.

### Example configuration

```yaml
global:
  bot_display_name: Solomon
  log_level: info

  claude:
    team_lead_model: opus
    teammate_model: sonnet
    planning_model: opus
    agent_team_timeout: 3600

  slack:
    bot_token: ${SLACK_BOT_TOKEN}
    channel_id: ${SLACK_CHANNEL_ID}

  figma:
    access_token: ${FIGMA_ACCESS_TOKEN}
    export_scale: 2
    export_format: png

  planning:
    reminder_days: 7
    timeout_action: remind

boards:
  - id: main
    tracker: jira
    base_url: ${JIRA_BASE_URL}
    project: ${JIRA_PROJECT}
    email: ${JIRA_EMAIL}
    api_key: ${JIRA_API_KEY}
    labels:
      planning: refine_automatically
      approval: develop_automatically
    statuses:
      todo: To Do
      in_progress: In Progress
      in_review: In Review
      done: Done
      cancelled: Cancelled

repos:
  - name: frontend
    path: /home/user/repos/frontend
  - name: backend
    path: /home/user/repos/backend

plugins:
  # Single board, multi-repo developer plugin
  - type: developer
    board: main
    repos:
      - name: frontend
      - name: backend
    schedules:
      - name: check
        cron: "*/1 * * * *"
    settings:
      auto_launch: false
      max_review_rounds: 5
      max_ci_fix_attempts: 5
      max_uat_retries: 3
      slack_channel_id: ${SLACK_DEV_CHANNEL_ID}
      standup_enabled: true
      standup_hour: 9
      standup_channel_id: ${SLACK_STANDUP_CHANNEL_ID}

  - type: securityengineer
    board: main
    repos:
      - name: frontend
      - name: backend
    schedules:
      - name: full
        cron: "3 1 * * *"
      - name: quick
        cron: "0 12 * * *"
    settings:
      parallel_agents: 4
      jira_threshold: HIGH
      slack_channel_id: ${SLACK_SEC_CHANNEL_ID}
```

---

## Configuration reference

### Global settings

| Key | Type | Default | Description |
|---|---|---|---|
| `bot_display_name` | string | `Solomon` | Display name used in Slack messages and logs |
| `log_level` | string | `info` | Logging level: `debug`, `info`, `warn`, `error` |
| `data_path` | string | `~/.solomon` | Base directory for worktrees, state DB, and output files |

### Claude settings (`global.claude`)

| Key | Type | Default | Description |
|---|---|---|---|
| `team_lead_model` | string | `opus` | Model for the agent team lead (orchestration) |
| `teammate_model` | string | `sonnet` | Model for agent teammates (implementation) |
| `planning_model` | string | `sonnet` | Model for the planning phase |
| `agent_team_timeout` | int | `3600` | Maximum seconds for agent team execution |

### Slack settings (`global.slack`) — optional

| Key | Type | Default | Description |
|---|---|---|---|
| `bot_token` | string | — | Slack bot token (`xoxb-...`) |
| `channel_id` | string | — | Default Slack channel for notifications |

### Figma settings (`global.figma`) — optional

| Key | Type | Default | Description |
|---|---|---|---|
| `access_token` | string | — | Figma API access token |
| `export_scale` | int | `2` | Scale factor for Figma exports (1–4) |
| `export_format` | string | `png` | Export format: `png`, `jpg`, `svg`, `pdf` |

### Planning settings (`global.planning`)

| Key | Type | Default | Description |
|---|---|---|---|
| `reminder_days` | int | `7` | Days before sending a planning reminder |
| `timeout_action` | string | `remind` | Action on timeout: `remind` or `close` |

### Board settings (`boards[]`)

| Key | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Unique board identifier, referenced by plugins |
| `tracker` | string | yes | Tracker type: `jira` or `linear` |
| `base_url` | string | yes | Tracker instance URL |
| `project` | string | yes | Project key or ID |
| `api_key` | string | yes | API token for authentication |
| `email` | string | Jira only | Email for Jira API auth |
| `labels.planning` | string | no | Label marking issues for planning (default: `solomon`) |
| `labels.approval` | string | no | Label marking planning approval (default: `approved`) |
| `statuses.todo` | string | no | Status name for To Do |
| `statuses.in_progress` | string | no | Status name for In Progress |
| `statuses.in_review` | string | no | Status name for In Review |
| `statuses.done` | string | no | Status name for Done |
| `statuses.cancelled` | string | no | Status name for Cancelled |

### Repository settings (`repos[]`)

| Key | Type | Required | Description |
|---|---|---|---|
| `name` | string | yes | Unique repo identifier, referenced by plugins |
| `path` | string | yes | Absolute path to the local git repository |

### Plugin settings (`plugins[]`)

| Key | Type | Required | Description |
|---|---|---|---|
| `type` | string | yes | Plugin type: `developer` or `securityengineer` |
| `board` | string | yes | Board ID this plugin uses |
| `repos` | list | yes | Repositories this plugin operates on (each with a `name` field) |
| `schedules` | list | yes | Cron schedules (each with `name` and `cron` fields) |
| `settings` | map | no | Plugin-specific settings (see below) |

### Developer plugin settings

| Setting | Type | Default | Description |
|---|---|---|---|
| `auto_launch` | bool | `false` | Start implementation automatically after planning completes |
| `max_review_rounds` | int | `3` | Maximum PR review/fix cycles |
| `max_ci_fix_attempts` | int | `5` | Maximum CI failure fix attempts |
| `max_uat_retries` | int | `3` | Maximum UAT retry attempts |
| `slack_channel_id` | string | global | Override Slack channel for this plugin's notifications |
| `standup_enabled` | bool | `false` | Enable daily standup reports via Slack |
| `standup_hour` | int | `9` | Hour of day (0–23) to send standup |
| `standup_channel_id` | string | `slack_channel_id` | Override Slack channel for standup messages |

### Security engineer plugin settings

| Setting | Type | Default | Description |
|---|---|---|---|
| `parallel_agents` | int | `4` | Number of concurrent security analysis agents |
| `jira_threshold` | string | `HIGH` | Minimum severity for auto-creating Jira tickets: `CRITICAL`, `HIGH`, `MEDIUM`, `LOW` |
| `slack_channel_id` | string | global | Override Slack channel for this plugin's notifications |

### Environment variables (`.env`)

| Variable | Required | Description |
|---|---|---|
| `JIRA_BASE_URL` | yes* | Jira instance URL |
| `JIRA_PROJECT` | yes* | Jira project key |
| `JIRA_API_KEY` | yes* | Jira API token |
| `JIRA_EMAIL` | yes* | Email for Jira API auth |
| `SLACK_BOT_TOKEN` | no | Slack bot token |
| `SLACK_CHANNEL_ID` | no | Default Slack channel |
| `SLACK_DEV_CHANNEL_ID` | no | Developer plugin Slack channel override |
| `SLACK_STANDUP_CHANNEL_ID` | no | Standup Slack channel override |
| `SLACK_SEC_CHANNEL_ID` | no | Security plugin Slack channel override |
| `FIGMA_ACCESS_TOKEN` | no | Figma API token |

\* Required when using Jira as the tracker.

## License

[MIT](LICENSE)
