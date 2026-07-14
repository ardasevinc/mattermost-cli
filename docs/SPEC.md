# Mattermost CLI - Specification

## Overview
A TypeScript + Bun CLI tool to fetch and display Mattermost messages (DMs and channels), with built-in secret redaction for safe LLM processing later.

## Requirements Summary

| Aspect | Decision |
|--------|----------|
| Auth | Personal access token (env var) |
| Message Scope | DMs + public/private/group channels |
| Threading | Complete visible threads by default, `--no-threads` for selected seeds only |
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
# Inspect the authenticated identity and team memberships
mm whoami
mm teams
mm users [query] [--team <name>] [--limit 20]

# List all channels
mm channels
mm channels --type public

# Fetch DMs (all or filtered)
mm dms [options]

# Fetch DMs from specific user
mm dms --user=<username>
mm dms -u bob -u alice    # Multiple users

# Fetch group DMs (all or one validated group channel)
mm group-dms [--limit 50] [--since 7d]
mm group-dms --channel <channel-id>

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
--json            Output as JSON (JSON Lines for watch)
--no-color        Disable colored output
-r, --relative    Show relative times
--no-relative     Show absolute times
--redact          Enable secret redaction (default)
--no-redact       Disable secret redaction
--threads         Show thread structure (default)
--no-threads      Return selected seed posts only (except thread command)
```

### DMs Options

```
--user, -u        Filter by username (repeatable)
--limit, -l       Max total messages across matched DMs (default: 50)
--since, -s       Time range: "24h", "7d", "30d" (default: 7d)
--channel, -c     Specific direct-message channel ID (type D only)
```

### Group DMs Options

```
--limit, -l       Max total messages across matched group DMs (default: 50)
--since, -s       Time range: "24h", "7d", "30d" (default: 7d)
--channel, -c     Specific group-DM channel ID (type G only)
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

Selection limits, time windows, and search predicates apply to seed posts. Thread hydration runs
after selection, so roots and sibling replies are included as context even when they fall outside
those predicates, and visible output may exceed the requested limit. Hydration uses bounded
concurrency and reports partial coverage rather than dropping seed posts when a thread cannot be
proved complete. `--no-threads` performs no thread hydration requests except for `mm thread`, which
always fetches the explicitly requested thread.

---

## Secret Redaction

`mm doctor` performs only `GET /system/ping?get_server_status=true` and, when both URL and token are
available, `GET /users/me`. It always emits configuration, server, and authentication checks with
`pass`, `warn`, `fail`, or `skipped` status. JSON is `{ "ok": boolean, "checks": [...] }`; `ok` is
false and the exit status is nonzero when any check fails. Credential values, raw remote bodies,
and arbitrary ping/user fields are never emitted. Missing optional ping fields are reported as
`unknown`, not inferred healthy. Missing URL or token is a configuration failure, but all checks
are still emitted and any possible ping still runs. Each request has an independent bounded
timeout. Every remote string passes through control-character sanitization and, when enabled, the
normal secret-redaction pipeline. A configured token is never emitted even with redaction disabled.
An insecure file containing a token fails regardless of which credential source wins precedence.

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

Retrieved posts are normalized as their latest visible state. Structured JSON includes
`updatedAt`, optional `editedAt`/`deletedAt`, deletion/system/pinned markers, post type, file IDs and
available file metadata, safe attachment display fields, and reaction summaries with available
actor identities. Pretty and Markdown output render the same state. Webhook override usernames are
used for display while preserving the underlying `userId`; empty-author system posts use `system`.

Only metadata already present in list, search, and thread payloads is used. There are no per-post
file or reaction requests, no edit-history reconstruction, and arbitrary attachment actions or
props are never emitted. Normal retrieval excludes deleted posts (`deletedPostsIncluded: false`);
if one is returned in hydrated context, only a stable `[deleted post]` placeholder is exposed.

### Pretty Terminal (default for TTY)
- Colored usernames
- Grouped by date
- Timestamps formatted nicely
- `--no-color` keeps pretty layout and disables ANSI colors

### Markdown (default for pipe/non-TTY)
- Standard markdown format
- Good for LLM processing
- Quoted messages
- Selected for non-TTY stdout regardless of `--no-color`

### JSON (`--json`)
- Full structured data
- Includes redaction log
- Good for programmatic use

Empty successful message reads (`dms`, `group-dms`, `channel`, `search`, and `mentions`) return `[]` and exit
zero. Pretty and Markdown modes print `No messages found.` instead. A missing requested thread or
post is still an error. Empty unread JSON retains its command-specific shape: `{ "unread": [] }`.
The exception is an empty resumed channel-history read whose completeness is unknown. It returns
one empty channel envelope with `queryTruncated: null`, echoes `inputCursor`, and repeats the same
value as `nextCursor`. Consumers must treat an unchanged cursor as a retry token, not progress.
Empty `teams` JSON is `[]`; human output is `No teams found.`. `whoami` and `teams` use narrow
command-specific schemas and never emit raw Mattermost user or team objects. Malformed identity or
team responses fail closed without including remote values in errors.
`users` is a read-only directory lookup (its search endpoint uses POST semantically). It emits a
narrow user whitelist inside `{ users, retrieval }`, sorts by username then ID, and probes with
`limit + 1`. Single-page probes are capped at 200 users for listing and 1000 for search, with
`truncated: null` when the relevant endpoint ceiling prevents a conclusive result. Empty human
output is `No users found.`.

### Deterministic channel-history cursors

Only `channel <name>`, `dms --channel <D-id>`, and `group-dms --channel <G-id>` accept
`--cursor`. Merged conversation reads, `dms --user`, and explicit `--since` plus `--cursor` are
rejected. The opaque, versioned base64url cursor binds the raw channel ID, absolute original since
boundary, and the last selected `(create_at, id)` boundary. Selection order is `create_at DESC`,
then ASCII post ID ascending. Resume accepts older timestamps plus same-timestamp IDs greater than
the boundary ID. A safe optional Mattermost `before` anchor may reduce scanning; the local boundary
filter remains authoritative. Cursors are unsigned pagination state, not authorization tokens.

### JSON Lines (`--json watch ...`)
- One redacted `posted` event per stdout line
- Connection, retry, malformed-event, and gap diagnostics on stderr
- Automatic reconnect with next-sequence resume when the server preserves the connection
- Changed connections and sequence gaps are reported honestly; no implicit REST backfill

---

## Future Extensions (Not in current scope)

- LLM task extraction module
- SQLite cache for offline access
- Interactive TUI mode
