# Agent Guide

Codebase guide for AI agents working on this project.

## Project Structure

```
src/
├── index.ts              # CLI entry point (commander setup)
├── cli.ts                # Command handlers (listChannels, fetchDMs)
├── config.ts             # TOML config file loading (~/.config/mattermost-cli/)
├── types.ts              # All TypeScript interfaces
├── api/
│   ├── client.ts         # MattermostClient class (HTTP, auth, rate limits)
│   ├── users.ts          # User fetching + caching
│   ├── channels.ts       # Channel/DM fetching
│   └── posts.ts          # Message fetching + pagination
├── preprocessing/
│   ├── patterns.ts       # Secret detection regex patterns
│   ├── secrets.ts        # Detection + masking logic
│   └── index.ts          # Pipeline entry (currently just secrets)
├── utils/
│   ├── date.ts           # Date formatting (DD/MM, relative time)
│   └── threading.ts      # Thread grouping + ordering
└── formatters/
    ├── json.ts           # JSON output
    ├── markdown.ts       # Markdown output (for pipes/LLMs)
    └── pretty.ts         # Terminal output (colors, grouping)

tests/
├── api/                  # API-related tests
├── preprocessing/        # Secret detection/masking tests
└── utils/                # Date + threading tests
```

## Key Flows

### CLI → API → Output
```
index.ts (parse args)
    → cli.ts (listChannels/fetchDMs/fetchThread)
        → api/* (fetch data from Mattermost)
        → preprocessing/* (redact secrets)
        → formatters/* (format output)
```

### Secret Redaction Pipeline
```
cli.ts:146 calls preprocess(post.message)
    → preprocessing/index.ts
        → secrets.ts:detectSecrets() finds matches
        → secrets.ts:maskSecret() partial-masks each
        → returns { text, redactions }
```

## Common Tasks

### Add a new secret pattern
1. Edit `src/preprocessing/patterns.ts`
2. Add to `SECRET_PATTERNS` array with `name` and `pattern` (regex with capture group)
3. Add test in `tests/preprocessing/secrets.test.ts`

### Add a new output format
1. Create `src/formatters/newformat.ts`
2. Export from `src/formatters/index.ts`
3. Wire up in `cli.ts` output logic (~line 180)

### Add a new API endpoint
1. Add function in appropriate `src/api/*.ts` file
2. Use `getClient().get<T>()` or `.post<T>()`
3. Add types to `src/types.ts` if needed

## Code Conventions

- **Bun for dev** - use `bun test`, `bun run`; production code uses cross-runtime APIs
- **Biome is configured** - use `bun run lint` / `bun run check` / `bun run format`
- **No dotenv** - Bun auto-loads `.env`
- **Types in types.ts** - keep interfaces centralized
- **Singleton client** - use `getClient()` after `initClient()`

## Testing

```bash
bun run lint                # Biome lint
bun run check               # Biome full check
bun x tsc --noEmit          # Typecheck
bun test                    # Run all tests
bun test secrets            # Run tests matching "secrets"
bun run build               # Build npm artifact
```

Test files live in `tests/` by domain.

## Security Rules

**Never:**
- Log or print `MM_TOKEN` (not even in errors)
- Store original secret values in output (we removed `originalText` and `redactions.original` for this reason)
- Make write operations in read commands (we removed POST fallback in `getDMChannelWithUser`)

**Always:**
- Redact before output
- Use partial masking (show prefix/suffix for context)

## Configuration

**Priority chain:** CLI args → env vars → TOML config file

### Environment Variables
```bash
MM_URL=https://mattermost.example.com   # Server URL
MM_TOKEN=<token>                         # Personal access token
MM_REDACT=false                          # Optional: disable redaction
```

### Config File
```bash
mm config --init  # Creates ~/.config/mattermost-cli/config.toml
```

```toml
# ~/.config/mattermost-cli/config.toml
url = "https://mattermost.example.com"
token = "your-personal-access-token"
redact = true
```

## Entry Points

| Task | File | Function |
|------|------|----------|
| CLI parsing | `src/index.ts` | `program.parse()` |
| Config loading | `src/config.ts` | `loadConfigFile()` |
| List channels | `src/cli.ts` | `listChannels()` |
| Fetch DMs | `src/cli.ts` | `fetchDMs()` |
| Fetch one thread | `src/cli.ts` | `fetchThread()` |
| Date formatting | `src/utils/date.ts` | `formatDate()`, `formatRelativeTime()` |
| Thread grouping | `src/utils/threading.ts` | `groupIntoThreads()` |
| Secret detection | `src/preprocessing/secrets.ts` | `detectSecrets()` |
| API requests | `src/api/client.ts` | `MattermostClient.request()` |
