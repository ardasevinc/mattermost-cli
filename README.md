# mattermost-cli

`mm` is a native Mattermost CLI for agents and humans. It provides bounded,
honest reads and live watch output, redacts secrets by default, and makes every
remote mutation a persisted stage that must be reviewed and applied separately.

The sharp edge is intentionally blunt:

```text
describe intent -> stage locally -> inspect exact revision -> apply explicitly
```

There is no immediate `send`, `edit`, `delete`, or `react` command.

## Install

Supported release targets are macOS and Linux on arm64 or amd64.

```bash
# Homebrew
brew install ardasevinc/tap/mattermost-cli

# npm migration path (installs the exact native binary package)
npm install -g mattermost-cli

# Go
go install github.com/ardasevinc/mattermost-cli/v2/cmd/mm@v2.0.0

# checksum-verifying installer
curl -fsSL https://raw.githubusercontent.com/ardasevinc/mattermost-cli/main/scripts/install.sh | sh
```

The installer defaults to `~/.local/bin`. Set `MATTERMOST_CLI_INSTALL_DIR` to
choose another directory or `MATTERMOST_CLI_VERSION=v2.0.0` to pin a release.

From source:

```bash
git clone https://github.com/ardasevinc/mattermost-cli
cd mattermost-cli
go build -o ./bin/mm ./cmd/mm
```

## Configure

Configuration precedence is CLI flags, environment variables, TOML, then
defaults.

```bash
mm config --init
```

This creates `~/.config/mattermost-cli/config.toml` on every OS unless an
absolute `XDG_CONFIG_HOME` is set.

```toml
url = "https://mattermost.example.com"
token = "your-personal-access-token"
redact = true
mention_names = ["Arda", "arda.sevinc"]

# Optional local-stage retention policy. Zero disables each policy.
stage_ttl_seconds = 0
stage_prune_after_seconds = 0
```

Environment alternatives:

```bash
export MM_URL="https://mattermost.example.com"
export MM_TOKEN="your-personal-access-token"
```

Verify setup without mutation:

```bash
mm config
mm doctor
mm whoami
```

## Read Mattermost

```bash
mm teams
mm users arda --team core
mm channels --type all

mm dms --since 24h
mm dms --user alice --limit 100
mm dms --channel <channel-id> --cursor <opaque>

mm group-dms --since 7d
mm channel general --team core --since 24h
mm thread <post-id>
mm search "deployment" --team core
mm mentions --team core --since 24h
mm unread --team core --peek 5

mm watch general --team core
mm watch --dm alice
```

History selection is deterministic and bounded. JSON envelopes expose whether
retrieval is complete, truncated, or unknown; an unproven empty result fails
closed instead of pretending that nothing exists. Visible threads are hydrated
by default and can be disabled with `--no-threads`.

Output modes:

- TTY: pretty, optionally colored output
- non-TTY: Markdown suitable for pipes and agents
- `--json`: versioned `mm/v2/*` JSON, or JSON Lines for `watch`

## Stage and apply mutations

Creating a stage performs read-only identity and destination binding plus a
local SQLite write. It does not mutate Mattermost.

```bash
# stdin avoids shell-history/process-list exposure
printf '%s' '**hello** from a reviewed stage' | mm stage send dm alice

# channel and group destinations
printf '%s' 'deploy complete' | mm stage send channel releases --team core
printf '%s' 'group update' | mm stage send group <group-channel-id>

# other supported operations
printf '%s' 'thread reply' | mm stage reply <post-id>
printf '%s' 'corrected text' | mm stage post-edit <post-id>
mm stage post-delete <post-id>
mm stage react <post-id> eyes
mm stage unreact <post-id> eyes
mm stage dm-create alice
mm stage group-create alice bob
```

Every creation command supports `--dry-run`, which resolves and previews the
plan without reading content or writing local state. Human callers may also use
`--message`, with the explicit caveat that it is visible to shell history and
process inspection. Create/reply stages support repeatable `--attachment` paths.

Review and apply the exact revision printed by stage creation:

```bash
mm stage list
mm stage show <stage-id>
mm apply <stage-id>@<revision>
```

`stage show` deliberately reveals retained message content and attachment
paths. `stage list` and ordinary receipts do not.

Management:

```bash
printf '%s' 'revised text' | mm stage revise <stage-id>
mm stage cancel <stage-id>
mm stage prune --older-than 720h
mm store doctor
mm store migrations
```

Apply is exact-revision compare-and-swap. Concurrent callers cannot both claim
one revision. A definitively rejected request may become eligible for
`--resume-partial`; an uncertain outcome requires explicit `--force-unknown`
and warns about duplicate risk. Neither mode is automatic.

Machine callers use versioned requests and caller-generated replay keys:

```bash
mm stage --from-json < stage-request.json
mm apply --from-json < apply-request.json
mm schema list
mm schema show mm/v2/stage-request
mm schema validate mm/v2/stage-request < stage-request.json
```

Checked-in schemas and examples live under `schemas/v2/`.

## Safety model

- The active Mattermost credential is never emitted, even with `--no-redact`.
- Outbound text, structured fields, attachment paths/metadata, and attachment
  bytes containing that credential are rejected before persistence or dispatch.
- Read commands never create conversations or perform write fallbacks.
- Mutation redirects are rejected and uncertain writes are never replayed
  automatically.
- Remote bodies, reason phrases, and parser details are not reflected in errors.
- Terminal controls and Unicode bidi hazards are made visible or removed.
- A confirmed remote effect followed by local receipt/output failure has its own
  exit class and explicitly says not to retry.

Local stages are stored in SQLite under `$XDG_STATE_HOME/mattermost-cli` or
`~/.local/state/mattermost-cli`, with private directory/file modes, migrations,
integrity diagnostics, durable attempt journals, and append-only retention
events. Plaintext staged content exists until completion or explicit retention
cleanup; privileged local access, backups, snapshots, swap, and filesystem
forensics are outside the confidentiality guarantee.

## Development

Go 1.26 or newer is required.

```bash
just gate           # format, unit, race, vet, staticcheck, vuln, license, build
just docker-e2e     # disposable Mattermost 11.8.3 + Postgres acceptance
just release-artifacts 2.0.0 <commit> /tmp/mm-release
```

The normal gate includes all Go tests, race detection, `go vet`, Staticcheck,
`govulncheck`, exact dependency-license review, module verification, tagged E2E
compile/vet, and CGO-free cross-builds for all supported targets. The Docker
suite uses only fake local users and proves teardown leaves no project-scoped
containers, volumes, or networks.

The frozen TypeScript v1.6.0 oracle remains available at tag `v1.6.0`. Its
file-by-file disposition is recorded in `docs/V1_TEST_DISPOSITION.md`; the v2
contract is `docs/V2_CONTRACT.md`.

## License

MIT
