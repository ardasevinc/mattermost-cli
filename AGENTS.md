# Agent Guide

Codebase guide for AI agents working on this project.

## Project Structure

```
src/
├── index.ts              # CLI entry point (commander setup)
├── cli.ts                # Command handlers (listChannels, fetchDMs)
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
└── formatters/
    ├── json.ts           # JSON output
    ├── markdown.ts       # Markdown output (for pipes/LLMs)
    └── pretty.ts         # Terminal output (colors, grouping)
```

## Key Flows

### CLI → API → Output
```
index.ts (parse args)
    → cli.ts (listChannels/fetchDMs)
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
3. Add test in `src/preprocessing/secrets.test.ts`

### Add a new output format
1. Create `src/formatters/newformat.ts`
2. Export from `src/formatters/index.ts`
3. Wire up in `cli.ts` output logic (~line 180)

### Add a new API endpoint
1. Add function in appropriate `src/api/*.ts` file
2. Use `getClient().get<T>()` or `.post<T>()`
3. Add types to `src/types.ts` if needed

## Code Conventions

- **Bun, not Node** - use `bun test`, `bun run`, Bun APIs
- **No dotenv** - Bun auto-loads `.env`
- **Types in types.ts** - keep interfaces centralized
- **Singleton client** - use `getClient()` after `initClient()`

## Testing

```bash
bun test                    # Run all tests
bun test secrets            # Run tests matching "secrets"
```

Test files live next to source: `foo.ts` → `foo.test.ts`

## Security Rules

**Never:**
- Log or print `MM_TOKEN` (not even in errors)
- Store original secret values in output (we removed `originalText` and `redactions.original` for this reason)
- Make write operations in read commands (we removed POST fallback in `getDMChannelWithUser`)

**Always:**
- Redact before output
- Use partial masking (show prefix/suffix for context)

## Environment

```bash
MM_URL=https://mattermost.example.com   # Server URL
MM_TOKEN=<token>                         # Personal access token
```

## Entry Points

| Task | File | Function |
|------|------|----------|
| CLI parsing | `src/index.ts` | `program.parse()` |
| List channels | `src/cli.ts` | `listChannels()` |
| Fetch DMs | `src/cli.ts` | `fetchDMs()` |
| Secret detection | `src/preprocessing/secrets.ts` | `detectSecrets()` |
| API requests | `src/api/client.ts` | `MattermostClient.request()` |
