# mattermost-cli v2 Product Spec

This is the concise public product spec. `V2_CONTRACT.md` is authoritative for
safety, state-machine, schema, recovery, and cutover details.

## Identity

- Binary: `mm`
- Implementation: native Go
- First public Go release: `v2.0.0`
- Supported targets: darwin/linux, arm64/amd64
- Purpose: dependable Mattermost access for humans and agents, with honest
  bounded reads and deliberate stage-first mutations

## Commands

Read-only remote commands:

```text
whoami
teams
users [query]
channels
dms
group-dms
channel <name>
thread <post-id>
search <query>
mentions
unread
watch [channel] | watch --dm <username>
config
doctor
store doctor
store migrations
schema list|show|validate
```

Mutation preparation and local lifecycle:

```text
stage send dm|group|channel
stage reply
stage post-edit
stage post-delete
stage react|unreact
stage dm-create|group-create
stage list|show|revise|cancel|prune
```

The only general remote mutation boundary:

```text
apply <stage-id>@<revision>
```

There is no immediate `send`, `edit`, `delete`, `react`, or conversation-create
alias.

## Read behavior

- Requests, pages, retries, backoff, response bodies, concurrency, and output
  size are bounded.
- Results are ordered deterministically.
- Cursor context is authenticated by a versioned opaque token and validated
  before network access.
- Completeness is tri-state: complete, truncated, or unknown.
- Empty results succeed only when emptiness is proven.
- Thread hydration is bounded to four workers and preserves partial visible
  context when completeness cannot be proven.
- Deleted posts never expose stale body, file, attachment, or reaction data.
- Watch authenticates before events, validates sequence state, bounds reconnect,
  reports gaps, and never claims REST backfill it did not perform.

## Mutation behavior

Stage creation may perform remote reads to bind the exact authenticated user,
server, channel, post, participants, and current state. It performs no remote
mutation. Persisted stages live in private local SQLite and receive an opaque ID,
monotonic revision, semantic digest, normalized plan, lifecycle, and recovery
state.

Apply:

1. reopens and validates the exact current stage revision;
2. revalidates authenticated identity and destination state;
3. transactionally claims the revision;
4. journals dispatch intent;
5. sends each prepared mutation at most once;
6. validates confirmed remote effects;
7. persists a narrow bodyless receipt and clears content when complete.

Recovery is never inferred from optimism:

- ordinary apply requires recovery `none`;
- `--resume-partial` dispatches only a suffix proven not applied;
- `--force-unknown` is an explicit caller acceptance of duplicate risk;
- confirmed remote effect plus failed local receipt/output is distinct and says
  not to retry.

## Content and attachments

Message content comes from exactly one source:

- non-TTY stdin;
- `$VISUAL`, then `$EDITOR`, in human TTY mode;
- `--message`, explicitly visible to process inspection and shell history;
- structured `mm/v2/stage-request` input.

Content must be valid UTF-8, nonblank, no more than 16,383 Unicode code points,
and no more than 65,535 bytes.

Attachment stages bind private regular files by stable filesystem identity,
length, digest, canonical metadata, and order. Apply securely reopens and copies
each source into a private unlinked descriptor while hashing and scanning it.
Only the reviewed bytes can be uploaded. A changed, replaced, linked, symlinked,
oversized, or credential-bearing source fails closed.

## Output

- TTY human mode: pretty output, color-capable
- non-TTY human mode: Markdown
- `--json`: one strict schema-identified JSON document
- `--json watch`: schema-identified JSON Lines
- diagnostics: stderr only

All remote presentation text is sanitized before output. Markdown structure and
links are escaped or admitted through a narrow safe policy. Machine schemas and
golden examples under `schemas/v2/` are release artifacts.

Exit classes:

```text
0 success or idempotent replay
2 invalid invocation/input
3 config/auth/read failure before mutation dispatch
4 dispatched mutation definitively rejected
5 partial or unknown mutation outcome
6 local state/revision/claim/target/attachment conflict
7 confirmed remote effect but failed local receipt/output; do not retry
```

## Security

- HTTPS is mandatory except canonical loopback HTTP.
- Active credentials are never emitted and cannot be disabled by `--no-redact`.
- Active credentials are forbidden in outbound text, structured fields,
  attachment metadata/path, and bytes.
- Mutation redirects are rejected.
- Unknown mutation outcomes are not automatically replayed.
- Remote response bodies and unsafe caller/remote values are not reflected in
  ordinary errors.
- Original secret values are absent from redaction provenance.
- Read commands never perform a write fallback.

## Configuration and local state

Config precedence: CLI, environment, TOML, defaults.

```text
$XDG_CONFIG_HOME/mattermost-cli/config.toml
~/.config/mattermost-cli/config.toml
```

State:

```text
$XDG_STATE_HOME/mattermost-cli
~/.local/state/mattermost-cli
```

Paths are OS-independent. Files and directories use private modes, reject unsafe
ownership/link/permission conditions, and never store the Mattermost token.

## Verification and distribution

Required gates include normal/race/fault/subprocess/schema tests, `go vet`,
Staticcheck, `govulncheck`, exact dependency-license review, module verification,
CGO-free target builds, deterministic archives, checksums, exact npm packages,
Homebrew install/test, and disposable Mattermost 11.8.3 lifecycle/read/watch
acceptance.

The v1.6.0 TypeScript oracle is frozen at tag `v1.6.0`. Its complete removal
receipt is `V1_TEST_DISPOSITION.md`.
