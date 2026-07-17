# Agent Guide

Repository guide for agents working on `mattermost-cli` v2.

## Product invariant

`mm` reads and watches Mattermost, but remote mutations are always two-step:

1. `mm stage ...` resolves and persists an inspectable local intent without a
   remote mutation.
2. `mm apply <stage-id>@<revision>` compare-and-swaps and dispatches that exact
   reviewed revision.

Never add an immediate send/edit/delete/react bypass. Never automatically retry
an uncertain mutation.

## Structure

```text
cmd/
  mm/                    native CLI entry point and signal handling
  conformance/           language-neutral fake-server runner
internal/
  api/                   bounded HTTP, retry, redirect, and no-replay policy
  apply/                 remote mutation execution
  cli/                   Cobra commands and output orchestration
  config/                XDG TOML configuration and permission checks
  mattermost/            narrow validated REST and watch operations
  messageinput/          bounded exact UTF-8 message input
  normalization/         post normalization and deleted-content suppression
  output/                human Markdown/pretty and machine envelopes
  presentation/          sanitization, redaction, credential ownership
  retrieval/             bounded pagination, cursors, hydration, completeness
  schema/                embedded strict machine-schema registry
  serverurl/             canonical safe URL policy
  stagecontent/          stdin/editor/message acquisition
  stagecursor/           deterministic local-stage cursors
  stageinput/            attachment identity, scanning, and spooling
  stageoutput/           stage receipt construction and validation
  stagerequest/          strict structured mutation request decoding
  stagestore/            SQLite migrations, CAS, journal, retention, recovery
  staging/               read-only resolution and stage planning
  transport/             WebSocket transport
schemas/v2/              checked-in JSON Schemas and golden examples
tests/e2e/               disposable Mattermost acceptance tests and Compose file
scripts/
  e2e/                   Go-native Docker conductor
  licenses/              exact reviewed dependency-license gate
  release/               deterministic native archives and Homebrew formula
  npm-package/           exact launcher/platform npm packages
  install.sh             checksum-verifying installer
```

## Working conventions

- Go 1.26+; format with `gofmt`.
- Keep protocol and security behavior in narrow packages, not Cobra handlers.
- Use the local Mattermost REST client; do not add the server SDK.
- Centralize machine types in `internal/output` and schemas under `schemas/v2`.
- Every structured request/output is self-identifying and schema-validated.
- Preserve exact content bytes after valid UTF-8 and bound checks.
- Validate canonical identities before presentation sanitization.
- Keep reads bounded, deterministic, and honest about incomplete results.
- Before adding a module, review maintenance and license posture, then update
  `scripts/licenses/main.go` intentionally.

## Core gates

```bash
just gate
just docker-e2e
```

Useful focused commands:

```bash
go test ./internal/cli
go test -race ./internal/stagestore ./internal/apply
go vet ./...
go run honnef.co/go/tools/cmd/staticcheck@2026.1 ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
go run ./scripts/licenses
go test -tags=e2e -run '^$' ./tests/e2e
```

`just gate` runs formatting, all normal and race tests, vet, Staticcheck,
`govulncheck`, license and module checks, native build, tagged E2E compile/vet,
four CGO-free cross-builds, and `git diff --check`.

`just docker-e2e` starts pinned Mattermost 11.8.3 and Postgres images on a
loopback-only dynamic port, creates only fake users, runs lifecycle/read/watch
acceptance, and verifies complete resource teardown. It never touches a
configured workplace server.

## Security rules

Never:

- emit, log, or persist the active Mattermost credential or a guessable digest;
- retain original secret values in redaction provenance;
- reflect remote response bodies, reason phrases, parser details, or untrusted
  path values in ordinary errors;
- follow redirects for mutations;
- replay a write after dispatch unless the explicit recovery contract proves a
  suffix was not applied or the caller accepts unknown duplicate risk;
- turn unknown completeness into an empty/complete claim;
- make a read command create a DM or perform any write fallback;
- echo staged outbound content in list output or ordinary receipts.

Always:

- register active credentials only for the owning invocation and release them;
- reject that credential in outbound body, structured fields, attachment
  metadata/path, and bytes before persistence and again before dispatch;
- bind full normalized server URL, authenticated user, destination, revision,
  and semantic digest before apply;
- journal dispatch intent before handing a mutation to transport;
- distinguish definitive rejection, partial/unknown outcome, local conflict,
  and confirmed-effect receipt/output failure;
- add regression coverage for bugs and contract changes.

## Configuration and state

Priority: CLI flags, environment, TOML, defaults.

```text
$XDG_CONFIG_HOME/mattermost-cli/config.toml
~/.config/mattermost-cli/config.toml
```

State:

```text
$XDG_STATE_HOME/mattermost-cli
~/.local/state/mattermost-cli
```

Both XDG variables must be absolute when set. Configuration and state paths are
the same on macOS and Linux. State is private SQLite plus its journals; stage
inspection is read-only unless the command explicitly mutates local lifecycle.

## Change routing

- CLI grammar/orchestration: `internal/cli/`
- HTTP/retry/no-replay: `internal/api/`
- Mattermost endpoint binding: `internal/mattermost/`
- Retrieval/cursor/completeness: `internal/retrieval/`, `internal/cursor/`
- Human/machine output: `internal/output/`, `schemas/v2/`
- Redaction/sanitization: `internal/presentation/`
- Stage planning: `internal/staging/`
- SQLite lifecycle/recovery: `internal/stagestore/`
- Apply execution: `internal/apply/`
- Attachments: `internal/stageinput/`
- Distribution: `scripts/release/`, `scripts/npm-package/`, workflows

The frozen TypeScript implementation is not present on v2. Consult tag
`v1.6.0`, `docs/V1_PARITY_MATRIX.md`, and `docs/V1_TEST_DISPOSITION.md` when
historical oracle evidence matters.
