---
name: mattermost-cli
description: >-
  This skill should be used when the user asks to "check my mattermost messages",
  "fetch DMs", "what did X say", "check messages from coworker", "read mattermost",
  "search mattermost for X", "check my mentions", "any unread messages", "watch a channel",
  "monitor chat", "what's new on mattermost", "find messages about X", "check channel history",
  "follow up on that mattermost thread", or mentions mattermost conversations, chat history,
  unread messages, or finding tasks mentioned in chat. Also use when the user needs context
  from team communication, wants to find action items from conversations, or needs to monitor
  a channel for updates in real-time.
version: 1.5.0
---

# Mattermost CLI

Read-only CLI for fetching Mattermost messages. Output is automatically redacted for safe LLM processing.

## When to Invoke Immediately

Trigger this skill when the user:
- Asks to check/read/fetch Mattermost messages, DMs, or channels
- Wants to see what someone said ("what did alice say about X")
- Needs to find tasks or action items from chat
- Asks to search for something in Mattermost
- Wants to check their mentions or unread messages
- Needs to monitor a channel or DM in real-time
- References a conversation or thread from Mattermost

## When to Suggest (Don't Auto-Invoke)

Offer to use this skill when:
- User mentions a task "from chat" without specifying Mattermost
- User is looking for context that might be in messages
- You need message history to understand a task better
- User seems unaware of recent team discussions that might be relevant

Say: "want me to check your Mattermost messages for context?" — don't auto-invoke.

## Prerequisites

The `mm` CLI must be installed and configured:

```bash
mm channels  # test if working
mm doctor    # diagnose partial configuration and connectivity safely
```

If it fails, configure credentials using one of:
1. Config file: `mm config --init` then edit `~/.config/mattermost-cli/config.toml`
2. Environment variables: `MM_URL` and `MM_TOKEN`
3. CLI flags: `--url` and `--token`

## Commands

### Inspect Identity and Teams
```bash
mm whoami                 # authenticated username, id, and roles
mm teams                  # team names, ids, and access types
mm --json whoami          # narrow identity schema for automation
mm --json teams           # stable sorted team list for automation
mm users [query]          # list or search active users
mm users --team core      # restrict the directory to one team
mm --json users arda      # narrow user schema plus retrieval coverage
```

Both commands are read-only and intentionally omit private profile and server metadata. An account
with no team memberships prints `No teams found.` (`[]` in JSON mode).
User results omit email, roles, authentication, notification, and arbitrary property fields.

### List Channels
```bash
mm channels                  # account-wide channels, with team identity for O/P
mm channels --type dm        # DMs only
mm channels --type public    # public channels only
mm channels --type private   # private channels only
mm channels --type group     # group channels only
```

### Fetch Direct Messages
```bash
mm dms                             # all DMs, last 7 days
mm dms -u <username>               # from specific user
mm dms -u alice -u bob             # multiple users
mm dms --since 24h                 # last 24 hours
mm dms --since 30d --limit 100     # more history
mm dms -c <channel-id>             # specific DM channel by ID
mm dms -c <channel-id> --cursor <opaque> # resume this DM's history
```

`mm dms --limit` is a total output cap across all matched DM channels.
The limit selects seed messages; complete thread context may make the visible output larger.

### Fetch Group Direct Messages
```bash
mm group-dms                         # all group DMs, last 7 days
mm group-dms --channel <channel-id>  # one validated group DM
mm group-dms --since 24h --limit 100 # shared output budget across group DMs
mm group-dms --channel <channel-id> --cursor <opaque> # resume one group DM
```

### Fetch Channel Messages
```bash
mm channel general                 # last 7 days from #general
mm channel dev-ops --team myteam   # specify team if multi-team
mm channel general --since 24h     # last 24 hours
mm channel general --limit 200     # more messages
mm channel general --cursor <opaque> # resume deterministic history
```

For these three single-channel forms, copy `retrieval.selection.nextCursor` into the next
`--cursor` call. Do not combine it with an explicit `--since`. If an empty result has
`queryTruncated: null` and repeats the same cursor, retry later; an unchanged cursor does not mean
progress. Merged DMs/group DMs and `dms --user` do not support cursors.

### Search Messages
```bash
mm search "deployment"                    # one team (auto only for single-team accounts)
mm search "from:alice bug"                # Mattermost search syntax
mm search "in:general after:2026-01-01"   # channel + date filters
mm search "deployment" --team myteam      # scope to team
mm search "deployment" --limit 20         # limit results
```

Search resolves one explicit team scope. On multi-team accounts pass `--team`; do not describe its
results as account-wide or as complete DM/group-DM coverage.

### Check Mentions
```bash
mm mentions                        # messages mentioning you
mm mentions --since 24h            # recent mentions
mm mentions --channel general      # mentions in specific channel
mm mentions --team myteam          # scope to team
```

Mentions has the same one-team and incomplete D/G coverage boundary as search.

Mention detection uses your username plus any aliases configured in `mention_names` in the config file.

### Unread Summary
```bash
mm unread                          # list channels with unread counts
mm unread --peek 5                 # also fetch 5 messages per unread channel
mm unread --team myteam            # selected-team O/P plus account-global D/G
```

Unread always includes account-global, deduplicated DMs and group DMs. `--team` selects which
public/private channels are included. Channels are sorted by mentions first, then by unread count.

### Watch (Real-Time)
```bash
mm watch general                   # live-stream channel messages
mm watch general --team myteam     # specify team
mm watch --dm alice                # live-stream DMs with a user
mm --json watch general            # redacted JSON Lines on stdout
```

WebSocket-based and limited to newly posted messages. It reconnects automatically and resumes from
the next event sequence when Mattermost preserves the connection. JSON mode writes one event per
stdout line; connection and gap diagnostics stay on stderr. Ctrl+C or SIGTERM stops cleanly.

### Fetch a Thread
```bash
mm thread <postId>                 # fetch root post + all replies
```

### Manage Configuration
```bash
mm config                # show config status
mm config --init         # create config file template
mm config --path         # print config file path
mm doctor                # read-only config, server, and auth checks
mm --json doctor         # stable diagnostic envelope
```

Use `doctor` for setup failures. It reports credential sources but never values or raw server error
bodies, and it can still ping a configured server when the token is missing. Missing credentials
make readiness fail after all checks print; network checks use independent bounded timeouts.

## Quick Reference

| Task | Command |
|------|---------|
| Current identity | `mm whoami` |
| Team memberships | `mm teams` |
| Recent DMs | `mm dms --since 24h` |
| DMs from specific person | `mm dms -u alice` |
| All channels list | `mm channels` |
| Channel history | `mm channel general --since 7d` |
| Search messages | `mm search "deployment issue"` |
| Check mentions | `mm mentions --since 24h` |
| Unread summary | `mm unread` |
| Unread with preview | `mm unread --peek 5` |
| Watch channel live | `mm watch general` |
| Watch DM live | `mm watch --dm alice` |
| Specific thread | `mm thread <postId>` |
| JSON for processing | `mm dms --json` |
| Setup config | `mm config --init` |

## Global Options

These apply to all commands:

| Flag | Effect |
|------|--------|
| `--json` | JSON output; JSON Lines for `watch` |
| `--no-color` | Disable ANSI colors |
| `-r, --relative` | Relative timestamps ("2 days ago") |
| `--no-relative` | Absolute timestamps |
| `--redact` | Enable secret redaction (default) |
| `--no-redact` | Disable secret redaction |
| `--threads` | Show thread structure (default) |
| `--no-threads` | Return selected seeds only, without hydration requests (except `mm thread`) |

## Output Formats

| Context | Format | Use Case |
|---------|--------|----------|
| Terminal (TTY) | Pretty | Reading directly; ANSI colors unless `--no-color`, grouping, thread indentation |
| Piped / non-TTY | Markdown | Passing to other tools or LLMs, regardless of `--no-color` |
| `--json` flag | JSON | Parsing, analysis, programmatic access |
| `--json watch ...` | JSON Lines | Streaming into pipes or append-only logs |

Empty `dms`, `group-dms`, `channel`, `search`, and `mentions` reads are successful: JSON output is `[]`, while
terminal and piped text output is `No messages found.`. Treat a missing requested thread or post as
an error. Empty `unread --json` output is `{ "unread": [] }`.

Under AI agents, relative time is auto-enabled ("2 days ago" instead of "29 Jan 2026"). Override with `--no-relative`.

With threads enabled, limits, time windows, and search terms select seed posts first. The CLI then
hydrates each visible thread, including its root and sibling replies as context. Inspect JSON
`retrieval.visibleThreads` before treating that context as complete; `partial` lists unproven roots
in `failedRootIds` while preserving all selected and successfully hydrated posts.

Inspect `channel.metadataStatus` before relying on channel labels or types. If a follow-up channel
lookup fails after posts were retrieved, the CLI preserves the posts and reports the sanitized ID,
`type: "unknown"`, `name: "unknown"`, and `metadataStatus: "unavailable"`, plus one generic
channel-metadata warning. Bounded request retry/transport diagnostics may also be present. It never
includes remote error bodies. HTTP 401 remains fatal; other lookup failures, including potentially
channel-specific 403 responses, use the unavailable state.

Outputs describe each post's latest visible state, including edited/system/pinned markers, payload
file metadata, safe attachment fields, and reactions when Mattermost includes them. The CLI does
not fetch edit history or issue extra per-post file/reaction requests. Deleted posts are normally
excluded; a deleted post returned during context hydration is reduced to `[deleted post]` without
stale content.

## Security

All secrets are automatically redacted before output:
- API keys and tokens (AWS, GitHub, GitLab, Slack, OpenAI, Anthropic, Stripe, etc.)
- JWTs and Bearer/Basic auth headers
- Connection strings (postgres://, mongodb://)
- Private keys, passwords, credentials in config snippets

Masking preserves context: `ghp_abc123xyz789secret` → `ghp_...cret`

Remote strings are control-character sanitized even with `--no-redact`. Secret masking can be
disabled with `--no-redact` when explicitly needed.

## Configuration

Credentials resolve in this order (first wins):
1. CLI flags (`--url`, `--token`)
2. Environment variables (`MM_URL`, `MM_TOKEN`)
3. Config file (`~/.config/mattermost-cli/config.toml`)

Config file format:
```toml
url = "https://mattermost.example.com"
token = "your-personal-access-token"
redact = true
mention_names = ["Arda", "arda.sevinc"]  # aliases for mm mentions
```

## Error Handling

| Error | Cause | Solution |
|-------|-------|----------|
| "Mattermost URL required" | Not configured | `mm config --init` or set `MM_URL` |
| "Mattermost token required" | Not configured | Edit config or set `MM_TOKEN` |
| "Could not find DM channel" | User doesn't exist or no DM history | Check username spelling |
| "Could not find channel" | Channel name wrong or no access | Verify name and team |
| "Multiple teams found" | Multi-team user without `--team` | Add `--team <name>` |
| Connection errors | Network/server issues | Verify URL is correct and accessible |
| WebSocket auth failure | Invalid token for watch mode | Regenerate access token |

## When NOT to Use

- User is asking about Slack, Discord, or other chat platforms
- User wants to *send* messages (this tool is read-only)
- User needs webhook or bot integrations

## Example Workflows

### "What did Alice say about the deployment?"
```bash
mm dms -u alice --since 7d
```
Scan output for deployment-related content.

### "Check my recent messages for any tasks"
```bash
mm dms --since 24h
```
Review output for action items, requests, or TODOs.

### "Any mentions I missed?"
```bash
mm mentions --since 24h
```
Catch up on messages that tagged you.

### "What's happening in the dev channel?"
```bash
mm channel dev --since 24h
```
Or for ongoing monitoring:
```bash
mm watch dev
```

### "Find that discussion about the API migration"
```bash
mm search "API migration"
```

### "Quick catch-up on everything unread"
```bash
mm unread --peek 3
```
Shows unread channels with mention counts and previews 3 messages from each.

### "Get full context from a thread someone linked"
```bash
mm thread abc123def456
```
