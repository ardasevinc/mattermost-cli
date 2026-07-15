# v1.6.0 to Go v2 Parity Matrix

Oracle: tag `v1.6.0`, commit `eccfc5029cc1a51514873b5cd5d7a4d3ded8d5cd`

This is the removal gate for the TypeScript implementation. A row may become `verified`, `intentionally_changed`, or `removed_by_contract`. Anything else blocks cutover.

## Status vocabulary

- `oracle`: behavior exists in v1 and still needs Go evidence
- `contracted`: new v2 behavior is locked but not implemented
- `scaffolded`: Go surface exists but parity evidence is incomplete
- `verified`: Go unit/conformance/E2E evidence satisfies the row
- `intentionally_changed`: v2 contract defines replacement behavior and tests prove it
- `removed_by_contract`: behavior is explicitly forbidden or superseded by v2

## Executable and global behavior

| Surface | v1 behavior | v2 disposition | Status |
| --- | --- | --- | --- |
| binary | `mm` | same public name at cutover | oracle |
| `--help` | Commander help | Cobra help, artifact-smoked | oracle |
| `--version` | package version | Go build metadata, artifact-smoked | oracle |
| `-t`, `--token` | credential CLI override | preserve | oracle |
| `--url` | server URL CLI override | preserve | oracle |
| `--json` | command JSON; watch JSONL | replace with schema-identified `mm/v2` JSON/JSONL | intentionally_changed |
| `--no-color` | disables ANSI, not output-format selection | preserve | oracle |
| `-r`, `--relative` | relative timestamps | preserve | oracle |
| `--no-relative` | absolute timestamps | preserve | oracle |
| agent relative default | `is-ai-agent` enables relative output | preserve semantically with Go detection | oracle |
| `--redact` | enable heuristic redaction | preserve | oracle |
| `--no-redact` | disable heuristic redaction, never active-token masking | preserve | oracle |
| `--threads` | hydrate complete visible threads | preserve | oracle |
| `--no-threads` | selected seeds only except `thread` | preserve | oracle |
| numeric validation | canonical positive safe integer only | preserve bounded canonical integer validation | oracle |
| duration validation | `^\d+[hdwm]$` | preserve | oracle |

## Configuration

| Surface | v1 behavior | v2 disposition | Status |
| --- | --- | --- | --- |
| default path | `~/.config/mattermost-cli/config.toml` | preserve on every OS | oracle |
| XDG path | ignored | use absolute `$XDG_CONFIG_HOME`, mandatory read-only v1 fallback | intentionally_changed |
| `url` | TOML server URL | preserve | oracle |
| `token` | TOML PAT | preserve | oracle |
| `redact` | TOML default | preserve | oracle |
| `mention_names` | trimmed non-empty string array | preserve | oracle |
| `MM_URL` | URL env override | preserve | oracle |
| `MM_TOKEN` | token env override | preserve | oracle |
| `MM_REDACT` | `false` disables; other defined values enable | preserve | oracle |
| precedence | CLI, env, file, defaults | preserve | oracle |
| init | non-overwriting, mode `0600` | preserve at selected v2 path | oracle |
| permissions | diagnose group/other access; token exposure can be fatal | preserve/fail closed | oracle |
| state path | none | XDG state with `~/.local/state` fallback | intentionally_changed |

## Read, diagnostic, and watch commands

| Command | Flags/defaults | Required v2 behavior | Status |
| --- | --- | --- | --- |
| `doctor` | global flags | same read-only readiness checks; `mm/v2/doctor` | oracle |
| `config` | `--path`, `--init` | preserve plus deterministic XDG migration warning | oracle |
| `whoami` | global flags | narrow validated identity | oracle |
| `teams` | global flags | validated deterministic teams | oracle |
| `users [query]` | `--team`, `-l`/`--limit 20` | preserve exact directory semantics | oracle |
| `channels` | `--type all`; `dm/public/private/group/all` | preserve account-wide dedupe/team identity | oracle |
| `dms` | repeated `-u`/`--user`; `-l`/`--limit 50`; `-s`/`--since 7d`; `-c`/`--channel`; `--cursor` | preserve exact D-channel and aggregate semantics | oracle |
| `group-dms` | `-l`/`--limit 50`; `-s`/`--since 7d`; `-c`/`--channel`; `--cursor` | preserve exact G-channel and aggregate semantics | oracle |
| `channel <name>` | `--team`; `-l`/`--limit 50`; `-s`/`--since 7d`; `--cursor` | preserve team-aware resolution | oracle |
| `thread <postId>` | always hydrate | preserve | oracle |
| `search <query>` | `--team`; `-l`/`--limit 50` | preserve bounded search/completeness | oracle |
| `mentions` | `--team`; `-l`/`--limit 50`; optional `-s`/`--since`; `--channel` | preserve aliases and resolution | oracle |
| `unread` | `--team`; `--peek` | preserve metrics/sorting/fail-closed empty | oracle |
| `watch [channel]` | `--team`; `--dm` | preserve auth, heartbeat, reconnect, gap diagnostics | oracle |

Bare `mm config` must also preserve the human status surface: selected path, file existence, URL configured, token configured, and permission warning without exposing values.

## v1 write surface and v2 replacement

| v1 surface | v1 behavior | v2 disposition | Status |
| --- | --- | --- | --- |
| `send dm <username>` | stdin then immediate remote write | forbidden; replace with `stage send dm`, then revision-bound `apply` | removed_by_contract |
| `send group <channel-id>` | stdin then immediate remote write | forbidden; replace with `stage send group`, then revision-bound `apply` | removed_by_contract |
| `--dry-run` | bodyless read-only destination preview | `stage ... --dry-run`, unpersisted and bodyless | intentionally_changed |
| message stdin | exact UTF-8, non-TTY | preserve plus editor/`--message` human options | intentionally_changed |
| bounds | 16,383 code points and 65,535 bytes | preserve before persistence/dispatch | oracle |
| DM creation | conditional direct-channel creation then post | explicit compound staged plan | intentionally_changed |
| group target | exact existing type-G ID | preserve plus explicit group-create stages | intentionally_changed |
| post attempt | one client dispatch, no automatic replay | preserve per attempt; explicit recovery modes only | oracle |
| receipt | narrow, no message body | preserve under `mm/v2/apply-receipt` | oracle |

## New v2 mutation surface

| Surface | Contract gate | Status |
| --- | --- | --- |
| `stage send dm/group/channel` | exact online binding, no remote mutation | contracted |
| `stage reply` | bind exact root/channel | contracted |
| `stage post-edit` | own-post/state binding | contracted |
| `stage post-delete` | own-post/state binding | contracted |
| `stage react/unreact` | exact post/emoji/current-user state | contracted |
| `stage dm-create/group-create` | exact canonical participant set | contracted |
| attachments | path + digest, private apply-time spool | contracted |
| `stage list/show/revise/cancel/prune` | revision/history/privacy contract | contracted |
| `apply <id>@<revision>` | CAS claim and ordinary recovery `none` | contracted |
| `--resume-partial` | only proven-not-applied suffix | contracted |
| `--force-unknown` | explicit duplicate-risk attempt | contracted |
| structured requests | versioned JSON plus idempotency key | contracted |
| SQLite store | migrations, WAL, permissions, recovery journal | contracted |

## Output and presentation

| Behavior | v1 oracle | v2 disposition | Status |
| --- | --- | --- | --- |
| TTY human output | pretty/color-capable | preserve semantic content | oracle |
| non-TTY human output | Markdown independent of color | preserve | oracle |
| explicit JSON | unversioned command shape | schema-identified v2 envelope | intentionally_changed |
| watch JSON | JSONL stdout | schema-identified JSONL stdout | intentionally_changed |
| diagnostics | stderr | preserve; schema errors in machine mode | oracle |
| empty messages | `No messages found.` | preserve human meaning | oracle |
| disabled-redaction warning | stderr | preserve | oracle |
| latest visible post state | edits/deletes/system/pin/files/attachments/reactions | preserve | oracle |
| Markdown safety | escape remote structure and unsafe links | preserve | oracle |
| timestamps | absolute/relative and edit metadata | preserve | oracle |
| retrieval metadata | selected/visible counts and completeness | preserve under v2 schema | oracle |

## Critical negative guarantees

Every row requires a named Go regression test and, where applicable, a language-neutral scenario.

| Guarantee | Status |
| --- | --- |
| active Mattermost credential is never emitted, even with `--no-redact` | oracle |
| outbound staged content containing the active credential is rejected before persistence/network | oracle |
| attachment paths, metadata, and bytes containing the active credential are rejected | contracted |
| outbound bodies never appear in ordinary receipts/errors | oracle |
| exact username/current-user/channel-type/participant validation completes before post dispatch | oracle |
| remote response bodies/reason phrases/parser details are never reflected | oracle |
| mutation redirects are rejected | oracle |
| uncertain mutation requests are never automatically replayed | oracle |
| confirmed effect plus local receipt/output failure says do not retry | oracle |
| reads retry only bounded safe failures | oracle |
| HTTP is allowed only for loopback; unsafe URL components fail closed | oracle |
| complete normalized base path is preserved and stage-bound | oracle |
| read commands never create DMs or perform POST fallback | oracle |
| dry-run performs no local persistence or remote mutation | oracle |
| malformed identity/team/channel/retrieval payloads fail closed | oracle |
| unknown completeness never becomes confirmed empty/exhausted | oracle |
| malformed/context-mismatched cursors fail before fetching | oracle |
| explicit cursor plus `--since` conflicts | oracle |
| deleted posts never leak stale content | oracle |
| terminal/control/bidi hazards are visible or removed safely | oracle |
| overlapping redaction matches never re-append plaintext | oracle |
| truncated private-key blocks fail closed without plaintext leakage | oracle |
| invalid UTF-8, whitespace-only, oversized, and unintended TTY input fail before work | oracle |
| watch validates events/sequences and bounds reconnect | oracle |
| watch auth failure stops reconnect and releases credentials/timers | oracle |
| thread hydration concurrency is at most four | oracle |
| `--no-threads` avoids hydration except explicit thread | oracle |
| stage apply is exact-revision CAS-bound | contracted |
| concurrent apply cannot double-claim | contracted |
| prior uncertainty cannot be erased by rejection or revision | contracted |
| stale applying claim cannot become ordinary replay | contracted |
| path attachment mutation cannot change uploaded bytes | contracted |
| lifecycle cleanup cannot erase attempt truth | contracted |
| Docker harness refuses non-loopback and always tears down | oracle |

## Test-file disposition

| v1 domain | Vitest files | Go destination |
| --- | --- | --- |
| API | `tests/api/{channels,client,messages,paths,posts,retrieval,url,websocket}.test.ts` | transport/API/retrieval/websocket unit + conformance |
| CLI | `tests/{channels-type-validation,cli-empty-reads,cli-failure-propagation,cli-metadata,cli-output,cli-retrieval,config-doctor,cursor-cli,cursor-commander,cursor,group-dms,identity-teams,input,send-commander,send,users,validation}.test.ts` | command subprocess + package tests |
| preprocessing | `tests/preprocessing/{post,sanitize,secrets}.test.ts` | preprocessing fixture parity + fuzz |
| formatters | `tests/formatters/{headers,watch}.test.ts` | golden human/schema tests |
| utilities | `tests/utils/{date,threading,unread}.test.ts` | Go unit tests |
| E2E | `tests/e2e/send-live.e2e.ts` | parameterized language-neutral Docker E2E, then Go-native runner |

The E2E disposition specifically preserves verbatim short Markdown DM and near-limit long Markdown group storage/readback, final-newline and Unicode fidelity, and exactly one resulting post.

Removal requires every v1 test file to link to one or more verified Go tests/scenarios in this table or a finer generated inventory.

## Build, CI, and release disposition

| v1 gate | Go v2 replacement | Status |
| --- | --- | --- |
| Biome check | `gofmt`/`goimports` plus static analysis | oracle |
| TypeScript typecheck | Go compile and `go vet` | oracle |
| 517 Vitest tests | Go unit/conformance/fault/race/E2E matrix | oracle |
| `bun audit` | `govulncheck` plus module/license checks | oracle |
| version invariant | Go build metadata, tag, schemas, npm shim invariant | oracle |
| Node bundle smoke | exact native archive smoke | oracle |
| exact npm tarball | exact launcher + platform package smoke | intentionally_changed |
| npm global install | npm migration-shim `mm` smoke | intentionally_changed |
| OIDC npm provenance | preserve for shim/platform packages | oracle |
| Docker Mattermost 11.8.3 | preserve and expand full lifecycle coverage | oracle |
| `RELEASE_TAG=v<version>` | preserve tag/version gate | oracle |
| release exact-tag checkout | preserve | oracle |
| already-published guard | preserve across native/npm release surfaces | oracle |

The v1 package receipt is an exact four-file allowlist: `LICENSE`, `README.md`, `dist/index.js`, and `package.json`. During migration, `prepack` still gates build plus version invariants and `prepublishOnly` still gates the full verification suite until the native/npm release pipeline replaces them with equivalent exact-artifact gates.

## Cutover proof

TypeScript removal is allowed only when:

1. no row remains `oracle`, `contracted`, or `scaffolded`;
2. every intentional change cites `docs/V2_CONTRACT.md` and passing v2 evidence;
3. every Vitest file has a recorded Go/conformance disposition;
4. the full Go gate, race gate, fault suite, disposable Mattermost E2E, archive smokes, Homebrew smoke, and npm migration smoke pass;
5. release artifacts are generated from the reviewed commit and their schemas/checksums are clean.
