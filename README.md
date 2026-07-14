# mattermost-cli

A CLI tool to fetch and display Mattermost messages (DMs, channels, threads) with automatic secret redaction for safe LLM processing.

## Features

- Fetch DMs from all channels or filter by specific users
- Fetch messages from public/private channels via `mm channel <name>`
- Search messages with Mattermost query syntax via `mm search <query>`
- Find mentions via `mm mentions` (supports configurable aliases)
- Show unread summary via `mm unread` (optional `--peek`)
- Watch channel live via `mm watch <channel>`
- List all channel types via `mm channels` with `--type` filtering
- Complete visible threads by default (`--no-threads` returns selected seeds only)
- Fetch a single thread via `mm thread <postId>`
- Automatic detection and redaction of secrets (API keys, tokens, passwords, etc.)
- Multiple output formats: pretty terminal, markdown, JSON, and JSON Lines for live watch
- Time-based filtering (`--since`) and message limits (`--limit`)

## Prerequisites

- Node.js >= 22.0.0
- Mattermost personal access token

## Installation

```bash
# npm
npm install -g mattermost-cli

# yarn
yarn global add mattermost-cli

# pnpm
pnpm add -g mattermost-cli

# bun
bun add -g mattermost-cli
```

Or run without installing:

```bash
bunx mattermost-cli
```

### From source

```bash
git clone https://github.com/ardasevinc/mattermost-cli
cd mattermost-cli
bun install
bun link  # Makes `mm` available globally
```

## Configuration

Configuration is resolved in this order: **CLI flags → environment variables → config file**

### Option 1: Config file (recommended)

```bash
mm config --init  # Creates ~/.config/mattermost-cli/config.toml
```

Then edit the file:

```toml
# ~/.config/mattermost-cli/config.toml
url = "https://mattermost.example.com"
token = "your-personal-access-token"
# redact = false  # Uncomment to disable secret redaction
# mention_names = ["Arda", "arda.sevinc"]  # Optional aliases for `mm mentions`
```

### Option 2: Environment variables

```bash
export MM_URL="https://mattermost.example.com"
export MM_TOKEN="your-personal-access-token"
# Optional: disable redaction globally
export MM_REDACT="false"
```

### Option 3: CLI flags

```bash
mm --url https://mattermost.example.com --token your-token channels
```

## Usage

### List channels

```bash
mm channels
mm channels --json
mm channels --type public
```

### Fetch direct messages

```bash
# All DMs from last 7 days
mm dms

# From specific user(s)
mm dms -u alice
mm dms -u alice -u bob

# With time filter
mm dms --since 24h
mm dms --since 30d --limit 100

# JSON output (for piping to other tools)
mm dms --json

# Return only selected seed posts (no root/sibling hydration)
mm dms --no-threads
```

`mm dms --limit` is a total output cap across all matched DM channels, not a per-channel cap.

### Fetch group direct messages

```bash
# All group DMs from the last 7 days
mm group-dms

# One group DM by channel ID
mm group-dms --channel <channel-id>

# Adjust the shared output budget and time range
mm group-dms --since 24h --limit 100
```

`mm group-dms --limit` is a total output cap across all matched group-DM channels.

### Fetch a specific thread

```bash
mm thread <post-id>
```

### Fetch a channel

```bash
mm channel general
mm channel #dev --team myteam
```

### Search messages

```bash
mm search "deployment"
mm search "from:alice in:general after:2026-02-01"
```

### Find mentions

```bash
mm mentions
mm mentions --since 7d
mm mentions --channel general --limit 20
```

### Show unread channels

```bash
mm unread
mm unread --peek 5
```

### Watch a channel live

```bash
mm watch general
mm watch dev --team myteam
mm watch --dm alice
mm --json watch general >> mattermost-posts.jsonl
```

Watch emits only newly posted events. It reconnects automatically with bounded backoff and resumes
from the next expected WebSocket sequence when Mattermost preserves the connection. Sequence gaps
or a changed server connection are reported, but watch does not perform REST backfill.

With `--json`, stdout is JSON Lines: one redacted post object per line, suitable for piping or
append-only logs. Connection status, retries, malformed-event warnings, and gap diagnostics go to
stderr, so they never corrupt the JSONL stream. Stop cleanly with either `SIGINT` (Ctrl+C) or
`SIGTERM`.

### Manage configuration

```bash
mm config           # Show config file status
mm config --path    # Print config file path
mm config --init    # Create config file with template
```

### Inspect identity and teams

```bash
mm whoami           # Authenticated user, id, and roles
mm teams            # Team names, ids, and access types
mm --json whoami    # Whitelisted identity JSON
mm --json teams     # Deterministically sorted team JSON
```

These commands are read-only. Identity output omits email, authentication, notification, and
arbitrary property fields. Team output likewise exposes only names, IDs, and access type. A valid
account with no team memberships prints `No teams found.` (`[]` with `--json`).

### Options

```
Global:
  -t, --token <token>     Mattermost personal access token (or MM_TOKEN env)
  --url <url>             Mattermost server URL (or MM_URL env)
  --json                  Output as JSON (JSON Lines for watch)
  --no-color              Disable colored output
  -r, --relative          Show relative times
  --no-relative           Show absolute times
  --redact                Enable secret redaction (default)
  --no-redact             Disable secret redaction (or MM_REDACT=false env)
  --threads               Show thread structure (default)
  --no-threads            Return selected seed posts only (except thread command)

DMs:
  -u, --user <username>   Filter by username (repeatable)
  -l, --limit <number>    Max total messages across matched DMs (default: 50)
  -s, --since <duration>  Time range: "24h", "7d", "30d" (default: 7d)
  -c, --channel <id>      Specific direct-message channel ID (type D only)

Group DMs:
  -l, --limit <number>    Max total messages across matched group DMs (default: 50)
  -s, --since <duration>  Time range: "24h", "7d", "30d" (default: 7d)
  -c, --channel <id>      Specific group-DM channel ID (type G only)

Channels:
  channels --type <type>  Filter list by type: dm, public, private, group, all

Channel:
  channel <name>          Fetch messages from one channel
  --team <name>           Team name (required if multiple teams)
  -l, --limit <number>    Max messages to fetch (default: 50)
  -s, --since <duration>  Time range: "24h", "7d", "30d" (default: 7d)

Search:
  search <query>          Search messages (supports Mattermost search modifiers)
  --team <name>           Team name (required if multiple teams)
  -l, --limit <number>    Max results (default: 50)

Mentions:
  mentions                Find @username + configured alias mentions
  --team <name>           Team name (required if multiple teams)
  -l, --limit <number>    Max results (default: 50)
  -s, --since <duration>  Time range filter (e.g. 24h, 7d)
  --channel <name>        Restrict mentions to one channel

Unread:
  unread                  Show channels with unread messages
  --team <name>           Team name (required if multiple teams)
  --peek <number>         Fetch N recent unread messages per channel

Watch:
  watch [channel]         Live posted events with automatic reconnect
  --team <name>           Team name (required if multiple teams)
  --dm <username>         Watch a DM conversation instead of a channel

Thread:
  thread <postId>         Fetch and display one thread
```

### Retrieval coverage

`--limit`, `--since`, and search terms select seed posts first. With threads enabled (the default),
the CLI then fetches the complete visible thread for every selected root or reply. The resulting
context can exceed `--limit` and can include replies outside the requested time range or search.
`--no-threads` disables these extra requests and returns only the selected seeds, except `mm thread`
which always fetches the requested thread. JSON message
outputs include additive `retrieval` metadata with the selected count, requested limit and time
boundary, whether the query was proven truncated, visible post/thread coverage, and the deleted-post policy. A
`queryTruncated` value of `null` means bounded pagination stopped before completeness could be
proved. `visibleThreads.status: "partial"` and `failedRootIds` report threads the server did not let
the CLI prove complete. Each message also includes its stable post `id` and Mattermost `permalink`.

Empty successful reads from `dms`, `group-dms`, `channel`, `search`, and `mentions` exit successfully. They emit
`[]` with `--json` and `No messages found.` in terminal or piped text output. A missing requested
thread or post remains an error.

### Current post state

Message output represents the latest visible state returned by Mattermost, not edit history. It
includes edit/update timestamps, pinned and system-post markers, webhook display names, payload
file metadata, safe attachment display fields, and reaction summaries. This data comes from the
list, search, or thread response itself; the CLI does not make per-post file or reaction requests.

Normal retrieval excludes deleted posts and reports `deletedPostsIncluded: false`. If Mattermost
does return a deleted post while hydrating context, the CLI emits only `[deleted post]` plus stable
post metadata and suppresses its stale message, files, attachments, and reactions. Availability of
file details, reactions, and actor usernames depends on the metadata included by the server and the
caller's permissions.

## Security

This tool automatically detects and redacts secrets in all remotely controlled emitted strings,
including messages, display names, file metadata, attachments, URLs, and reactions:

- AWS access keys and secret keys
- GitHub/GitLab tokens
- Slack/Discord tokens and webhooks
- JWTs
- Connection strings (postgres://, mongodb://, etc.)
- API keys, passwords, and more

Secrets are partially masked (e.g., `ghp_...cret`) to preserve context while preventing exposure.

**Note:** Redaction happens on display. Original messages are not modified on the server.
`--no-redact` disables secret masking, but unsafe control characters are still made visible.

## AI Agent Skill

This repo ships an agent skill for the [Vercel Skills CLI](https://github.com/vercel-labs/skills). Install it to give your AI coding agent access to Mattermost:

```bash
bunx skills@latest add ardasevinc/mattermost-cli --skill mattermost-cli
```

## Contributing

The published CLI targets Node.js >= 22. Development uses Bun 1.3.14 (matching `@types/bun`).

```bash
bun install --frozen-lockfile  # Install exact dependencies
bun run lint    # Biome lint
bun run check   # Biome full check
bun run typecheck  # Typecheck
bun run test    # Run tests with Vitest
bun run build   # Build for npm
bun run verify  # Full release verification
bun run mm      # Run CLI from source
```

## License

MIT
