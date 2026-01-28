# Mattermost DM CLI - Agent Instructions

This document describes how AI agents should use the `mm` CLI tool.

## When to Use

Use `mm` when the user asks about:
- Mattermost direct messages or DMs
- Checking messages from coworkers
- Finding tasks or action items mentioned in chat
- Reviewing recent conversations

## Prerequisites

The CLI must be installed and configured:

```bash
# Install
git clone https://github.com/ardasevinc/mattermost-dm-cli
cd mattermost-dm-cli
bun install
bun link  # Makes `mm` available globally
```

## Configuration

Set `MM_URL` and `MM_TOKEN` via environment variables or `.env` file:

```bash
# Option 1: Export directly
export MM_URL="https://mattermost.example.com"
export MM_TOKEN="your-personal-access-token"

# Option 2: Create .env file (Bun auto-loads it)
echo 'MM_URL=https://mattermost.example.com' >> .env
echo 'MM_TOKEN=your-token' >> .env
```

Or pass via CLI flags:
```bash
mm --url https://... --token your-token channels
```

## Commands

### List DM Channels

```bash
mm channels           # Pretty output
mm channels --json    # JSON output
```

Returns list of DM channels with usernames, message counts, and last activity.

### Fetch Messages

```bash
# All DMs from last 7 days (default)
mm dms

# From specific user
mm dms -u <username>
mm dms -u alice -u bob   # Multiple users

# Time filters
mm dms --since 24h       # Last 24 hours
mm dms --since 7d        # Last 7 days
mm dms --since 30d       # Last 30 days

# Limit message count
mm dms --limit 100

# JSON output (best for parsing)
mm dms --json
```

## Output Formats

| Context | Format | Notes |
|---------|--------|-------|
| TTY (terminal) | Pretty | Colored, grouped by date |
| Piped/non-TTY | Markdown | Good for LLM processing |
| `--json` flag | JSON | Structured, includes redaction metadata |

## Security

**All output is automatically redacted.** The CLI detects and masks:
- API keys (AWS, GitHub, Stripe, OpenAI, etc.)
- Tokens (JWT, Bearer, Slack, Discord)
- Connection strings
- Passwords in config snippets

Example: `ghp_abc123xyz789secret` becomes `ghp_...cret`

**Safe for LLM context:** You can pass the output to other AI tools without leaking secrets.

## Example Workflows

### Check recent messages from a user
```bash
mm dms -u alice --since 24h
```

### Export all DMs as JSON for analysis
```bash
mm dms --json > /tmp/dms.json
```

### Find who messaged recently
```bash
mm channels --json | jq '.[] | select(.lastPost != null) | .user'
```

## Error Handling

If `MM_URL` or `MM_TOKEN` are not set, the CLI exits with an error message explaining what's needed. Never hardcode credentials.

## Development

When working on this codebase:

- **Runtime:** Bun (not Node.js)
- **Run tests:** `bun test`
- **Entry point:** `src/index.ts`
- **Run CLI:** `bun src/index.ts` or `mm` (if linked)

**Important:** Never log or expose MM_TOKEN. The CLI is designed to never print tokens in `--help` or error messages.
