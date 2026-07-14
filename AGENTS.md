# Agent Guide

Codebase guide for AI agents working on this project.

## Project Structure

```
src/
├── index.ts              # CLI entry point (commander setup)
├── cli.ts                # Read command handlers and output orchestration
├── config.ts             # TOML config file loading (~/.config/mattermost-cli/)
├── cursor.ts             # Opaque deterministic history cursors
├── doctor.ts             # Read-only configuration/server/auth diagnostics
├── types.ts              # All TypeScript interfaces
├── validation.ts         # Strict CLI numeric validation
├── api/
│   ├── client.ts         # Bounded HTTP, auth, retry, rate-limit policy
│   ├── users.ts          # User fetching + caching
│   ├── channels.ts       # Channel/DM/team fetching + team resolution
│   ├── posts.ts          # Message fetching + pagination + search
│   ├── url.ts            # Server URL and permalink validation
│   └── websocket.ts      # Live post events for watch mode
├── preprocessing/
│   ├── patterns.ts       # Secret detection regex patterns
│   ├── credential.ts     # Active credential ownership and exact masking
│   ├── secrets.ts        # Detection + masking logic
│   ├── sanitize.ts       # Terminal/control/bidi sanitization
│   ├── post.ts           # Post and attachment normalization
│   └── pipeline.ts       # Presentation preprocessing entry
├── utils/
│   ├── colors.ts         # ANSI colors + username color hashing
│   ├── date.ts           # Date formatting (DD/MM, relative time)
│   ├── threading.ts      # Thread grouping + ordering
│   └── unread.ts         # Unread metrics + sorting helpers
└── formatters/
    ├── json.ts           # JSON output
    ├── markdown.ts       # Markdown output (for pipes/LLMs)
    ├── pretty.ts         # Terminal output (colors, grouping)
    └── watch.ts          # Pretty/JSONL watch events

scripts/
└── check-version.mjs     # package/lockfile/CLI version invariant

.github/workflows/
├── ci.yml                # Vitest, checks, audit, exact tarball smoke
└── publish.yml           # OIDC npm publishing of verified tarball

tests/                    # Vitest suites by API, CLI, security, formatting, and utilities
```

## Key Flows

### CLI → API → Output
```
index.ts (parse args)
    → cli.ts (whoami/teams/users/channels/dms/group-dms/channel/thread/search/mentions/unread/watch)
        → api/* (fetch data from Mattermost)
        → preprocessing/* (redact secrets)
        → formatters/* (format output)
```

### Secret Redaction Pipeline
```
cli.ts calls normalizePosts()/preprocess()
    → preprocessing/post.ts or preprocessing/pipeline.ts
        → sanitize.ts makes terminal/control/bidi characters visible
        → secrets.ts:detectSecrets() finds matches
        → secrets.ts:maskSecret() partial-masks each
        → credential.ts fully masks exact active Mattermost credentials
        → returns sanitized presentation data and redaction provenance
```

## Common Tasks

### Add a new secret pattern
1. Edit `src/preprocessing/patterns.ts`
2. Add to `SECRET_PATTERNS` array with `name` and `pattern` (regex with capture group)
3. Add tests in `tests/preprocessing/secrets.test.ts` and boundary coverage where emitted

### Add a new output format
1. Create `src/formatters/newformat.ts`
2. Export from `src/formatters/index.ts`
3. Wire it through the output selection in `src/cli.ts`

### Add a new API endpoint
1. Add function in appropriate `src/api/*.ts` file
2. Use `getClient().get<T>()` or `.post<T>()`
3. Add types to `src/types.ts` if needed

### Add a new command
1. Add handler in `src/cli.ts`
2. Add command wiring in `src/index.ts`
3. Add/adjust tests under `tests/`

## Code Conventions

- **Bun for dev** - use `bun run`; production code uses cross-runtime APIs
- **Vitest for tests** - use `bun run test` or `bun run test -- <pattern>`; do not use Bun's built-in test runner
- **No `npx` in this repo** - use `bunx`/`bun run` only
- **Biome is configured** - use `bun run lint` / `bun run check` / `bun run format`
- **No dotenv** - Bun auto-loads `.env`
- **Types in types.ts** - keep interfaces centralized
- **Singleton client** - use `getClient()` after `initClient()`

## Testing

```bash
bun run lint                # Biome lint
bun run check               # Biome full check
bun run typecheck           # Typecheck
bun run test                # Run all tests with Vitest
bun run test -- secrets     # Run tests matching "secrets"
bunx vitest run --no-isolate # Catch shared singleton/cache leakage
bun run build               # Build npm artifact
bun run verify              # Full local release gate
bun audit                   # Dependency vulnerability check
```

Test files live in `tests/` by domain.

## Security Rules

**Never:**
- Log or print `MM_TOKEN` (not even in errors or with `--no-redact`)
- Store original secret values in output (we removed `originalText` and `redactions.original` for this reason)
- Make write operations in read commands (we removed POST fallback in `getDMChannelWithUser`)

**Always:**
- Redact before output
- Use partial masking (show prefix/suffix for context)
- Fully mask exact active Mattermost credentials regardless of redaction preference
- Keep transport/API errors generic and never reflect remote response bodies
- Fail closed when retrieval completeness or required identity/team data is unknown

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
mention_names = ["Arda", "arda.sevinc"] # optional aliases used by `mm mentions`
```

## Entry Points

| Task | File | Function |
|------|------|----------|
| CLI parsing | `src/index.ts` | `program.parseAsync()` |
| Config loading | `src/config.ts` | `loadConfigFile()` |
| Health diagnostics | `src/doctor.ts` | `runDoctor()` |
| Identity | `src/cli.ts` | `showWhoAmI()` |
| Team discovery | `src/cli.ts` | `listTeams()` |
| User discovery | `src/cli.ts` | `listUsers()` |
| List channels | `src/cli.ts` | `listChannels()` |
| Fetch DMs | `src/cli.ts` | `fetchDMs()` |
| Fetch group DMs | `src/cli.ts` | `fetchGroupDMs()` |
| Fetch one channel | `src/cli.ts` | `fetchChannel()` |
| Fetch one thread | `src/cli.ts` | `fetchThread()` |
| Search messages | `src/cli.ts` | `searchMessages()` |
| Fetch mentions | `src/cli.ts` | `fetchMentions()` |
| Show unread summary | `src/cli.ts` | `showUnread()` |
| Watch live channel events | `src/cli.ts` | `watchChannel()` |
| Date formatting | `src/utils/date.ts` | `formatDate()`, `formatRelativeTime()` |
| Thread grouping | `src/utils/threading.ts` | `groupIntoThreads()` |
| Secret detection | `src/preprocessing/secrets.ts` | `detectSecrets()` |
| API requests | `src/api/client.ts` | `MattermostClient.request()` |
| History cursors | `src/cursor.ts` | `encodeChannelHistoryCursor()`, `decodeChannelHistoryCursor()` |
