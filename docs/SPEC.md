# Mattermost CLI - Specification

## Overview
A TypeScript + Bun CLI tool to fetch and display Mattermost messages (DMs and channels), with built-in secret redaction for safe LLM processing later.

## Requirements Summary

| Aspect | Decision |
|--------|----------|
| Auth | Personal access token (env var) |
| Message Scope | DMs + public/private/group channels |
| Threading | Threaded output by default, `--no-threads` to flatten |
| Output | Pretty terminal for TTY, markdown for pipe/non-TTY, `--json` flag |
| Time Range | `--since` (duration) and `--limit` (count) flags |
| Style | One-shot command |
| Secret Handling | Redact by default; can disable via `--no-redact` / `MM_REDACT=false` / config |
| Future | Modular design for LLM task extraction |

---

## Usage

### Environment Variables

```bash
MM_URL=https://mattermost.example.com
MM_TOKEN=your-personal-access-token
MM_REDACT=false
```

### Commands

```bash
# List all channels
mm channels
mm channels --type public

# Fetch DMs (all or filtered)
mm dms [options]

# Fetch DMs from specific user
mm dms --user=<username>
mm dms -u bob -u alice    # Multiple users

# Fetch one thread
mm thread <postId>

# Fetch one channel by name
mm channel general
mm channel #dev --team myteam
```

### Global Options

```
--token, -t       Mattermost personal access token (or MM_TOKEN env)
--url             Mattermost server URL (or MM_URL env)
--json            Output as JSON
--no-color        Disable colored output
-r, --relative    Show relative times
--no-relative     Show absolute times
--redact          Enable secret redaction (default)
--no-redact       Disable secret redaction
--threads         Show thread structure (default)
--no-threads      Flatten thread replies
```

### DMs Options

```
--user, -u        Filter by username (repeatable)
--limit, -l       Max messages to fetch (default: 50)
--since, -s       Time range: "24h", "7d", "30d" (default: 7d)
--channel, -c     Specific channel ID (skip channel lookup)
```

### Channels Options

```bash
--type            Filter list by type: dm, public, private, group, all
```

### Channel Command

```bash
channel <name>    Fetch and display one channel
--team            Team name (required if multiple teams)
--limit, -l       Max messages to fetch (default: 50)
--since, -s       Time range: "24h", "7d", "30d" (default: 7d)
```

### Thread Command

```bash
thread <postId>   Fetch and display one thread
```

---

## Secret Redaction

The CLI automatically detects and redacts secrets including:

- AWS access keys and secret keys
- GitHub/GitLab tokens
- Slack/Discord tokens and webhooks
- JWTs
- Bearer/Basic auth tokens
- Connection strings (postgres://, mongodb://, etc.)
- API keys and passwords in config
- Private keys
- Stripe, SendGrid, Twilio, OpenAI keys

Redaction uses partial masking to show first/last characters:
- Example: `ghp_abc123xyz789secret` → `ghp_...cret`

---

## Output Formats

### Pretty Terminal (default for TTY)
- Colored usernames
- Grouped by date
- Timestamps formatted nicely

### Markdown (default for pipe/non-TTY)
- Standard markdown format
- Good for LLM processing
- Quoted messages

### JSON (`--json`)
- Full structured data
- Includes redaction log
- Good for programmatic use

---

## Future Extensions (Not in current scope)

- `--watch` mode for real-time polling
- LLM task extraction module
- SQLite cache for offline access
- Interactive TUI mode
