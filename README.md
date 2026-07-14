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

`channels` is account-wide and deduplicates channel IDs. JSON items use the narrow schema
`{ id, type, name, displayName?, team, lastPost, messageCount }`: `team` is
`{ id, name, displayName? }` for public/private channels and `null` for direct/group messages.
Human public/private labels use `team-slug/#channel`, and every row includes the channel ID needed
by explicit D/G fetches.

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
mm dms --channel <channel-id> --cursor <opaque>

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
mm group-dms --channel <channel-id> --cursor <opaque>
```

`mm group-dms --limit` is a total output cap across all matched group-DM channels.

### Fetch a specific thread

```bash
mm thread <post-id>
```

### Fetch a channel

```bash
mm channel general
mm channel general --cursor <opaque>
mm channel '#dev' --team myteam
```

### Search messages

```bash
mm search "deployment"
mm search "from:alice in:general after:2026-02-01"
mm search "deployment" --team myteam
```

Search is scoped to one team. Accounts with multiple memberships must pass `--team`; results are
not guaranteed to include DMs or group DMs.

### Find mentions

```bash
mm mentions
mm mentions --since 7d
mm mentions --channel general --limit 20
mm mentions --team myteam
```

Mentions use the same single-team search scope and are not guaranteed to include DMs or group DMs.

### Show unread channels

```bash
mm unread
mm unread --peek 5
```

Unread resolves one team for public/private channels while including account-global, deduplicated
DMs and group DMs. Multi-team accounts must pass `--team`.

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
mm doctor           # Check config, server health, and authentication
mm --json doctor    # Stable { ok, checks } diagnostic envelope
```

`doctor` is read-only and inspects partial configuration without aborting early. Missing URL or
token makes readiness fail, while an available URL is still pinged and unavailable checks are
reported as skipped. It reports whether credentials came
from CLI flags, environment variables, a file, or are missing, never their values. Server checks
expose only ping health fields; authentication exposes only user ID and username. An insecure
config file is fatal whenever it stores a token, even if another source overrides it, and a warning
otherwise. Network checks have independent bounded timeouts.

### Inspect identity, teams, and users

```bash
mm whoami           # Authenticated user, id, and roles
mm teams            # Team names, ids, and access types
mm --json whoami    # Whitelisted identity JSON
mm --json teams     # Deterministically sorted team JSON
mm users            # List active users (default limit: 20)
mm users arda       # Search active users
mm users --team core --limit 50
mm --json users arda
```

These commands are read-only. Identity output omits email, authentication, notification, and
arbitrary property fields. Team output likewise exposes only names, IDs, and access type. A valid
account with no team memberships prints `No teams found.` (`[]` with `--json`). `users` emits only
IDs, usernames, display names, and nicknames. Its JSON envelope includes retrieval coverage;
`truncated: null` means the single-page endpoint ceiling prevented a conclusive limit-plus-one
probe. Listing is capped at 200 fetched users; search is capped at 1000.

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
  --cursor <opaque>       Resume that channel's deterministic history

Group DMs:
  -l, --limit <number>    Max total messages across matched group DMs (default: 50)
  -s, --since <duration>  Time range: "24h", "7d", "30d" (default: 7d)
  -c, --channel <id>      Specific group-DM channel ID (type G only)
  --cursor <opaque>       Resume that channel's deterministic history

Channels:
  channels --type <type>  Filter the account-wide list: dm, public, private, group, all

Users:
  users [query]            List or search active users
  --team <name>           Restrict users to a team
  -l, --limit <number>    Max users to show (default: 20)

Channel:
  channel <name>          Fetch messages from one channel
  --team <name>           Team name (required if multiple teams)
  -l, --limit <number>    Max seed messages; thread context may exceed it (default: 50)
  -s, --since <duration>  Time range: "24h", "7d", "30d" (default: 7d)
  --cursor <opaque>       Resume deterministic channel history

Search:
  search <query>          Search messages (supports Mattermost search modifiers)
  --team <name>           Team name (required if multiple teams)
  -l, --limit <number>    Max seed results; thread context may exceed it (default: 50)

Mentions:
  mentions                Find @username + configured alias mentions
  --team <name>           Team name (required if multiple teams)
  -l, --limit <number>    Max seed results; thread context may exceed it (default: 50)
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

`mm channel`, `mm dms --channel`, and `mm group-dms --channel` return an opaque
`nextCursor` when more history is available or completeness is unknown. Pass it back with
`--cursor`; do not combine it with an explicit `--since`. Cursors are bound to the resolved raw
channel ID and preserve the original absolute time boundary, so newly arriving posts do not shift
later pages. Merged DM/group-DM reads and `dms --user` do not support cursors. Pretty and Markdown
output print the cursor on a final `Next cursor:` line.
An empty resumed read with unknown completeness returns one empty channel envelope and repeats the
input cursor as `nextCursor`. Consumers must detect this unchanged cursor and retry later; it does
not represent pagination progress. A proven-complete empty read remains `[]`.

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

Every message envelope reports `channel.metadataStatus` as `"resolved"` or `"unavailable"`. If
posts were retrieved successfully but the follow-up channel metadata lookup fails or returns a
malformed payload, the CLI preserves those posts, emits one generic channel-metadata warning, and
uses the sanitized channel ID with `type: "unknown"`, `name: "unknown"`, and
`metadataStatus: "unavailable"`. That warning is in addition to bounded request retry/transport
diagnostics. Remote error bodies are never emitted. HTTP 401 remains a fatal authentication error;
other lookup errors, including 403 responses that may be channel-specific, use the explicit
unavailable state.

Search tolerates stale ordered hits whose post payload is missing, continues scanning for usable
results, and marks completeness unknown instead of crashing or claiming exhaustion. Search scans
are capped at 100 pages; reaching that cap also produces unknown completeness.
Channel history and thread reads apply the same missing-payload rule while preserving every valid
post returned on the page.

Proven-complete empty reads from `dms`, `group-dms`, `channel`, `search`, and `mentions` exit
successfully. They emit `[]` with `--json` and `No messages found.` in terminal or piped text
output. An empty result with unknown completeness fails instead of claiming there were no matches;
the unchanged resumed-cursor envelope above is the sole retryable exception. A missing requested
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
`--no-redact` disables heuristic secret masking, but the exact active Mattermost credential remains
protected. Unsafe terminal controls and Unicode bidirectional controls are always made visible;
ordinary Unicode, emoji, and joiners are preserved.

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
