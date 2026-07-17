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
| binary | `mm` | same public name at cutover | verified |
| `--help` | Commander help | Cobra help, artifact-smoked | verified |
| `--version` | package version | Go build metadata, artifact-smoked | verified |
| `-t`, `--token` | credential CLI override | preserve | verified |
| `--url` | server URL CLI override | preserve | verified |
| `--json` | command JSON; watch JSONL | replace with schema-identified `mm/v2` JSON/JSONL | intentionally_changed |
| `--no-color` | disables ANSI, not output-format selection | preserve | verified |
| `-r`, `--relative` | relative timestamps | preserve | verified |
| `--no-relative` | absolute timestamps | preserve | verified |
| agent relative default | `is-ai-agent` enables relative output | preserve semantically with Go detection | verified |
| `--redact` | enable heuristic redaction | preserve | verified |
| `--no-redact` | disable heuristic redaction, never active-token masking | preserve | verified |
| `--threads` | hydrate complete visible threads | preserve | verified |
| `--no-threads` | selected seeds only except `thread` | preserve | verified |
| numeric validation | canonical positive safe integer only | preserve bounded canonical integer validation | verified (`conformance/scenarios/pairs/invalid-limit.json`, Go validation tests) |
| duration validation | `^\d+[hdwm]$` | preserve | verified |
| URL normalization | WHATWG normalization plus custom loopback test | preserve safe canonicalization; reject transport-ambiguous IPv4/backslash forms | intentionally_changed |

## Configuration

| Surface | v1 behavior | v2 disposition | Status |
| --- | --- | --- | --- |
| default path | `~/.config/mattermost-cli/config.toml` | preserve on every OS | verified |
| XDG path | ignored | use absolute `$XDG_CONFIG_HOME`, mandatory read-only v1 fallback | intentionally_changed |
| `url` | TOML server URL | preserve | verified |
| `token` | TOML PAT | preserve | verified |
| `redact` | TOML default | preserve | verified |
| `mention_names` | trimmed non-empty string array | preserve | verified |
| `MM_URL` | URL env override | preserve | verified |
| `MM_TOKEN` | token env override | preserve | verified |
| `MM_REDACT` | `false` disables; other defined values enable | preserve | verified |
| precedence | CLI, env, file, defaults | preserve | verified |
| init | non-overwriting, mode `0600` | preserve at selected v2 path | verified |
| permissions | diagnose group/other access; token exposure can be fatal | preserve/fail closed | verified |
| state path | none | XDG state with `~/.local/state` fallback | intentionally_changed |

## Read, diagnostic, and watch commands

| Command | Flags/defaults | Required v2 behavior | Status |
| --- | --- | --- | --- |
| `doctor` | global flags | same read-only readiness checks; `mm/v2/doctor` | verified |
| `config` | `--path`, `--init` | preserve plus deterministic XDG migration warning | verified |
| `whoami` | global flags | narrow validated identity | verified (`conformance/scenarios/pairs/whoami.json`, `internal/cli/identity_test.go`) |
| `teams` | global flags | validated deterministic teams | verified (`conformance/scenarios/pairs/teams.json`, `internal/cli/identity_test.go`) |
| `users [query]` | `--team`, `-l`/`--limit 20` | preserve exact directory semantics | verified |
| `channels` | `--type all`; `dm/public/private/group/all` | preserve account-wide dedupe/team identity | verified |
| `dms` | repeated `-u`/`--user`; `-l`/`--limit 50`; `-s`/`--since 7d`; `-c`/`--channel`; `--cursor` | preserve exact D-channel and aggregate semantics | verified |
| `group-dms` | `-l`/`--limit 50`; `-s`/`--since 7d`; `-c`/`--channel`; `--cursor` | preserve exact G-channel and aggregate semantics | verified |
| `channel <name>` | `--team`; `-l`/`--limit 50`; `-s`/`--since 7d`; `--cursor` | preserve team-aware resolution | verified |
| `thread <postId>` | always hydrate | preserve | verified |
| `search <query>` | `--team`; `-l`/`--limit 50` | preserve bounded search/completeness | verified |
| `mentions` | `--team`; `-l`/`--limit 50`; optional `-s`/`--since`; `--channel` | preserve aliases and resolution | verified |
| `unread` | `--team`; `--peek` | preserve metrics/sorting/fail-closed empty | verified |
| `watch [channel]` | `--team`; `--dm` | preserve auth, heartbeat, reconnect, gap diagnostics | verified |

Bare `mm config` must also preserve the human status surface: selected path, file existence, URL configured, token configured, and permission warning without exposing values.

## v1 write surface and v2 replacement

| v1 surface | v1 behavior | v2 disposition | Status |
| --- | --- | --- | --- |
| `send dm <username>` | stdin then immediate remote write | forbidden; replace with `stage send dm`, then revision-bound `apply` | removed_by_contract |
| `send group <channel-id>` | stdin then immediate remote write | forbidden; replace with `stage send group`, then revision-bound `apply` | removed_by_contract |
| `--dry-run` | bodyless read-only destination preview | `stage ... --dry-run`, unpersisted and bodyless | intentionally_changed |
| message stdin | exact UTF-8, non-TTY | preserve plus editor/`--message` human options | intentionally_changed |
| bounds | 16,383 code points and 65,535 bytes | preserve before persistence/dispatch | verified |
| DM creation | conditional direct-channel creation then post | explicit compound staged plan | intentionally_changed |
| group target | exact existing type-G ID | preserve plus explicit group-create stages | intentionally_changed |
| post attempt | one client dispatch, no automatic replay | preserve per attempt; explicit recovery modes only | verified |
| receipt | narrow, no message body | preserve under `mm/v2/apply-receipt` | verified |

## New v2 mutation surface

| Surface | Contract gate | Status |
| --- | --- | --- |
| `stage send dm/group/channel` | exact online binding, no remote mutation | verified |
| `stage reply` | bind exact root/channel | verified |
| `stage post-edit` | own-post/state binding | verified |
| `stage post-delete` | own-post/state binding | verified |
| `stage react/unreact` | exact post/emoji/current-user state | verified |
| `stage dm-create/group-create` | exact canonical participant set | verified |
| attachments | path + digest, private apply-time spool | verified |
| `stage list/show/revise/cancel/prune` | revision/history/privacy contract | verified |
| `apply <id>@<revision>` | CAS claim and ordinary recovery `none` | verified |
| `--resume-partial` | only proven-not-applied suffix | verified |
| `--force-unknown` | explicit duplicate-risk attempt | verified |
| structured requests | versioned JSON plus idempotency key | verified |
| SQLite store | migrations, WAL, permissions, recovery journal | verified |

## Output and presentation

| Behavior | v1 oracle | v2 disposition | Status |
| --- | --- | --- | --- |
| TTY human output | pretty/color-capable | preserve semantic content | verified |
| non-TTY human output | Markdown independent of color | preserve | verified |
| explicit JSON | unversioned command shape | schema-identified v2 envelope | intentionally_changed |
| watch JSON | JSONL stdout | schema-identified JSONL stdout | intentionally_changed |
| diagnostics | stderr | preserve; schema errors in machine mode | verified |
| empty messages | `No messages found.` | preserve human meaning | verified |
| disabled-redaction warning | stderr | preserve | verified |
| latest visible post state | edits/deletes/system/pin/files/attachments/reactions | preserve | verified |
| Markdown safety | escape remote structure and unsafe links | preserve | verified |
| timestamps | absolute/relative and edit metadata | preserve | verified |
| retrieval metadata | selected/visible counts and completeness | preserve under v2 schema | verified |

## Critical negative guarantees

Every row requires a named Go regression test and, where applicable, a language-neutral scenario.

| Guarantee | Status |
| --- | --- |
| active Mattermost credential is never emitted, even with `--no-redact` | verified (N1) |
| outbound staged content containing the active credential is rejected before persistence/network | verified (N2) |
| attachment paths, metadata, and bytes containing the active credential are rejected | verified (N3) |
| outbound bodies never appear in ordinary receipts/errors | verified (N4) |
| exact username/current-user/channel-type/participant validation completes before post dispatch | verified (N5) |
| remote response bodies/reason phrases/parser details are never reflected | verified (N6) |
| mutation redirects are rejected | verified (N7) |
| uncertain mutation requests are never automatically replayed | verified (N8) |
| confirmed effect plus local receipt/output failure says do not retry | verified (N9) |
| reads retry only bounded safe failures | verified (N10) |
| HTTP is allowed only for transport-canonical loopback; unsafe or parser-ambiguous URL components fail closed | intentionally_changed |
| complete normalized base path is preserved and stage-bound | verified (N11) |
| read commands never create DMs or perform POST fallback | verified (N12) |
| dry-run performs no local persistence or remote mutation | verified (N13) |
| malformed identity/team/channel/retrieval payloads fail closed | verified (N14) |
| unknown completeness never becomes confirmed empty/exhausted | verified (N15) |
| malformed/context-mismatched cursors fail before fetching | verified (N16) |
| explicit cursor plus `--since` conflicts | verified (N17) |
| deleted posts never leak stale content | verified (N18) |
| terminal/control/bidi hazards are visible or removed safely | verified (N19) |
| overlapping redaction matches never re-append plaintext | verified (N20) |
| truncated private-key blocks fail closed without plaintext leakage | verified (N21) |
| public redaction positions remain UTF-16 offsets; v2 heuristic mask previews use whole Unicode scalar values | intentionally_changed |
| request JSON has no trailing newline and preserves JSON.stringify HTML/U+2028/U+2029 wire behavior | verified (N22) |
| invalid UTF-8, whitespace-only, oversized, and unintended TTY input fail before work | verified (N23) |
| watch validates events/sequences and bounds reconnect | verified (N24) |
| watch auth failure stops reconnect and releases credentials/timers | verified (N25) |
| thread hydration concurrency is at most four | verified (N26) |
| `--no-threads` avoids hydration except explicit thread | verified (N27) |
| stage apply is exact-revision CAS-bound | verified (N28) |
| concurrent apply cannot double-claim | verified (N29) |
| prior uncertainty cannot be erased by rejection or revision | verified (N30) |
| stale applying claim cannot become ordinary replay | verified (N31) |
| path attachment mutation cannot change uploaded bytes | verified (N32) |
| lifecycle cleanup cannot erase attempt truth | verified (N33) |
| Docker harness refuses non-loopback and always tears down | verified (N34) |

Named regression evidence:

- **N1-N4:** `TestPreprocessHeuristicRedactionCanBeDisabledWithoutDisablingCredentialMasking`, `FuzzPreprocessNeverEmitsExactActiveCredential`, `TestStructuredStageRejectsActiveCredentialBeforePersistence`, `TestScanFileBinaryAndCredential`, `TestStageSchemasRejectContradictionsAndLeaks`.
- **N5-N10:** mutation contract tests in `internal/mattermost/*_mutations_test.go`, `TestMutationRedirectDoesNotReplay`, `TestMutationNeverReplaysAndClassifiesOutcomes`, `TestApplyConfirmedEffectOutputFailureExitsSevenAndDoesNotRetry`, and read retry tests in `internal/api/client_test.go`.
- **N11-N17:** `TestNormalizeCanonicalizesWithoutLosingBasePath`, read-only DM tests in `internal/mattermost/channels_test.go`, `TestStageSendDryRunSkipsContentAndPersistence`, command-specific malformed-payload tests, `TestDecodeChannelHistoryRejectsInvalid`, and CLI cursor/since conflict tests.
- **N18-N23:** `TestNormalizePosts`, `TestSanitizeControlsMakesTerminalHazardsVisible`, `TestPreprocessNeverReappendsOverlappingPlaintext`, private-key cases in `internal/presentation/patterns_test.go`, request-body wire tests in `internal/api/client_test.go`, and `internal/messageinput/input_test.go`.
- **N24-N27:** `internal/mattermost/watch_test.go`, `internal/cli/watch_test.go`, `TestHydrateVisibleThreadsReusesCompleteRootsAndBoundsConcurrency`, and command `--no-threads` tests.
- **N28-N33:** `TestReviewedStateCASAndApplying`, `TestSimultaneousMutationCAS`, recovery-history and interrupted-apply tests in `internal/stagestore/apply_test.go`, `TestSnapshotRejectsReplacementDriftAndCredentialBytes`, and retention rollback/audit tests in `internal/stagestore/retention_test.go`.
- **N34:** the Go-native Docker launcher tests its non-loopback refusal and verifies no labeled container, volume, or network survives cleanup; the complete disposable suite has also passed end-to-end.

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

The finer inventory is [`V1_TEST_DISPOSITION.md`](V1_TEST_DISPOSITION.md), which maps all 34 frozen test files to verified Go evidence.

## Differential corpus

Paired fixtures use `mm/conformance-pair/v1`. Oracle and candidate run in separate isolated homes against separate sequential fake servers. Each side locks its own exact args, stdin, environment, HTTP method/URI/headers/body, stdout, stderr, and exit code. This proves preserved semantics while making contract-authorized v2 schema and exit-code changes explicit.

Current executable pairs:

- `invalid-limit.json`: canonical numeric rejection and zero network activity
- `teams.json`: authenticated request order, narrow field projection, deterministic sorting, and secret redaction
- `whoami.json`: authenticated request shape, narrow identity projection, and private-field exclusion

The sequential request transcript is the fake server's resulting state for these read-only cases. Mutation state is proved by Go fault tests and the disposable Mattermost E2E suite rather than inferred from stdout.

## Build, CI, and release disposition

| v1 gate | Go v2 replacement | Status |
| --- | --- | --- |
| Biome check | `gofmt` plus static analysis | verified |
| TypeScript typecheck | Go compile and `go vet` | verified |
| 517 Vitest tests | 818 Go unit/conformance/fault/race/E2E tests and fuzz targets across 98 files | verified |
| `bun audit` | `govulncheck` plus module/license checks | verified |
| version invariant | Go build metadata, tag, schemas, npm shim invariant | verified |
| Node bundle smoke | exact native archive smoke | verified |
| exact npm tarball | exact launcher + platform package smoke | intentionally_changed |
| npm global install | npm migration-shim `mm` smoke | intentionally_changed |
| OIDC npm provenance | preserve for shim/platform packages | verified |
| Docker Mattermost 11.8.3 | preserve and expand full lifecycle coverage | verified |
| `RELEASE_TAG=v<version>` | preserve tag/version gate | verified |
| release exact-tag checkout | preserve | verified |
| already-published guard | preserve across native/npm release surfaces | verified |

The native/npm release pipeline replaces v1 `prepack` and `prepublishOnly` with deterministic archive generation, checksum verification, exact npm tarball allowlists, artifact smokes, and OIDC provenance. Workflow syntax and action pins are verified locally; actual OIDC publication remains a release-time external acceptance check.

## Cutover proof

TypeScript removal is allowed only when:

1. no row remains `oracle`, `contracted`, or `scaffolded`;
2. every intentional change cites `docs/V2_CONTRACT.md` and passing v2 evidence;
3. every Vitest file has a recorded Go/conformance disposition;
4. the full Go gate, race gate, fault suite, disposable Mattermost E2E, archive smokes, Homebrew smoke, and npm migration smoke pass;
5. release artifacts are generated from the reviewed commit and their schemas/checksums are clean.
