# mattermost-cli

A CLI tool to fetch and display Mattermost direct messages with automatic secret redaction for safe LLM processing.

## Features

- Fetch DMs from all channels or filter by specific users
- Automatic detection and redaction of secrets (API keys, tokens, passwords, etc.)
- Multiple output formats: pretty terminal, markdown, JSON
- Time-based filtering (`--since`) and message limits (`--limit`)

## Prerequisites

- [Bun](https://bun.sh) >= 1.0.0
- Mattermost personal access token

## Installation

```bash
git clone https://github.com/ardasevinc/mattermost-cli
cd mattermost-cli
bun install
bun link  # Makes `mm` available globally
```

## Configuration

Set environment variables, use a `.env` file, or pass CLI flags:

```bash
# Option 1: Environment variables
export MM_URL="https://mattermost.example.com"
export MM_TOKEN="your-personal-access-token"

# Option 2: .env file (Bun auto-loads it)
cat > .env << EOF
MM_URL=https://mattermost.example.com
MM_TOKEN=your-personal-access-token
EOF

# Option 3: CLI flags
mm --url https://mattermost.example.com --token your-token channels
```

## Usage

### List DM channels

```bash
mm channels
mm channels --json
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
```

### Options

```
Global:
  -t, --token <token>     Mattermost personal access token (or MM_TOKEN env)
  --url <url>             Mattermost server URL (or MM_URL env)
  --json                  Output as JSON
  --no-color              Disable colored output

DMs:
  -u, --user <username>   Filter by username (repeatable)
  -l, --limit <number>    Max messages to fetch (default: 50)
  -s, --since <duration>  Time range: "24h", "7d", "30d" (default: 7d)
  -c, --channel <id>      Specific channel ID
```

## Security

This tool automatically detects and redacts secrets in message content:

- AWS access keys and secret keys
- GitHub/GitLab tokens
- Slack/Discord tokens and webhooks
- JWTs
- Connection strings (postgres://, mongodb://, etc.)
- API keys, passwords, and more

Secrets are partially masked (e.g., `ghp_...cret`) to preserve context while preventing exposure.

**Note:** Redaction happens on display. Original messages are not modified on the server.

## License

MIT
