# v1.6.0 Test Disposition

This inventory is the file-by-file removal receipt for the frozen TypeScript
suite at tag `v1.6.0` (`eccfc5029cc1a51514873b5cd5d7a4d3ded8d5cd`).
All 34 Vitest and disposable-server test files have a verified Go replacement
or an explicitly changed v2 mutation contract with verified replacement tests.

The mappings are intentionally many-to-many. Go v2 tests behavior at package,
schema, subprocess, fault, race, and real-server boundaries instead of
preserving the old file layout.

| Frozen v1 file | Disposition | Verified Go evidence |
| --- | --- | --- |
| `tests/api/channels.test.ts` | verified | `internal/mattermost/channels_test.go`, `internal/retrieval/{channel,dms,group_dms}_test.go`, `internal/cli/identity_test.go` |
| `tests/api/client.test.ts` | verified | `internal/api/client_test.go`, especially redirect, retry, bounded-body, timeout, and credential-erasure cases |
| `tests/api/messages.test.ts` | verified | `internal/mattermost/posts_test.go`, `internal/normalization/post_test.go`, `internal/output/{markdown,pretty}_test.go` |
| `tests/api/paths.test.ts` | verified | `internal/serverurl/url_test.go`, `internal/api/client_test.go` |
| `tests/api/posts.test.ts` | verified | `internal/mattermost/posts_test.go`, `internal/retrieval/{channel,thread,search}_test.go` |
| `tests/api/retrieval.test.ts` | verified | `internal/retrieval/*_test.go`, `internal/cli/{channel,dms,group_dms,search,thread,mentions,unread}_test.go` |
| `tests/api/url.test.ts` | intentionally changed per `V2_CONTRACT.md` section 16 | `internal/serverurl/url_test.go`, including ambiguous URL rejection and base-path preservation |
| `tests/api/websocket.test.ts` | verified | `internal/transport/websocket_test.go`, `internal/mattermost/watch_test.go`, `internal/cli/watch_test.go`, `tests/e2e/go-watch-live_test.go` |
| `tests/channels-type-validation.test.ts` | verified | `internal/cli/identity_test.go`, `internal/mattermost/channels_test.go` |
| `tests/cli-empty-reads.test.ts` | verified | command tests under `internal/cli/*_test.go` plus strict empty examples under `schemas/v2/examples/` |
| `tests/cli-failure-propagation.test.ts` | verified | `internal/cli/runtime_test.go`, `internal/cli/root_test.go`, per-command CLI failure tests |
| `tests/cli-metadata.test.ts` | intentionally changed per `V2_CONTRACT.md` section 6 | `internal/output/machine_test.go`, `internal/schema/{read,unread,watch}_test.go`, CLI command tests |
| `tests/cli-output.test.ts` | verified | `internal/output/{markdown,pretty,machine}_test.go`, `internal/cli/*_test.go` |
| `tests/cli-retrieval.test.ts` | verified | `internal/cli/{channel,dms,group_dms,search,thread,mentions,unread}_test.go`, `internal/retrieval/*_test.go` |
| `tests/config-doctor.test.ts` | verified | `internal/config/*_test.go`, `internal/doctor/doctor_test.go`, `internal/cli/{config,doctor,runtime}_test.go` |
| `tests/cursor-cli.test.ts` | verified | `internal/cursor/cursor_test.go`, `internal/cli/{channel,dms,group_dms}_test.go` |
| `tests/cursor-commander.test.ts` | verified | Cobra argument tests in `internal/cli/{channel,dms,group_dms,root}_test.go` |
| `tests/cursor.test.ts` | verified | `internal/cursor/cursor_test.go`, including canonical spellings and fuzzing |
| `tests/e2e/send-live.e2e.ts` | removed by contract; immediate send replaced by stage/apply | `tests/e2e/{go-live,go-concurrent-live,go-unknown-live,go-lifecycle-live,go-conversation-live}_test.go` |
| `tests/formatters/headers.test.ts` | verified | `internal/output/{markdown,pretty,model}_test.go` |
| `tests/formatters/watch.test.ts` | verified | `internal/output/watch_test.go`, `internal/schema/watch_test.go`, `internal/cli/watch_test.go` |
| `tests/group-dms.test.ts` | verified | `internal/mattermost/channels_test.go`, `internal/retrieval/group_dms_test.go`, `internal/cli/group_dms_test.go` |
| `tests/identity-teams.test.ts` | verified | `internal/mattermost/{users,teams}_test.go`, `internal/cli/identity_test.go`, paired `teams.json` and `whoami.json` scenarios |
| `tests/input.test.ts` | verified | `internal/messageinput/input_test.go`, `internal/stagecontent/content_test.go`, `internal/stagerequest/request_test.go` |
| `tests/preprocessing/post.test.ts` | verified | `internal/normalization/post_test.go`, `internal/mattermost/post_presentation_test.go` |
| `tests/preprocessing/sanitize.test.ts` | verified | `internal/presentation/sanitize_test.go`, `internal/presentation/fuzz_test.go` |
| `tests/preprocessing/secrets.test.ts` | verified | `internal/presentation/{patterns,sanitize,fuzz}_test.go`, credential-erasure tests across CLI/API/conformance packages |
| `tests/send-commander.test.ts` | removed by contract; direct send grammar is forbidden | `internal/cli/{stage_create,apply,root}_test.go`, `internal/stagerequest/request_test.go` |
| `tests/send.test.ts` | removed by contract; stage/apply provides the mutation boundary | `internal/staging/*_test.go`, `internal/stagestore/*_test.go`, `internal/apply/*_test.go`, `internal/cli/{stage_create,stage_manage,apply}_test.go` |
| `tests/users.test.ts` | verified | `internal/mattermost/users_test.go`, `internal/cli/identity_test.go` |
| `tests/utils/date.test.ts` | verified | `internal/output/date_test.go` |
| `tests/utils/threading.test.ts` | verified | `internal/output/threading_test.go`, `internal/retrieval/thread_test.go` |
| `tests/utils/unread.test.ts` | verified | `internal/output/unread_test.go`, `internal/retrieval/unread_test.go`, `internal/cli/unread_test.go` |
| `tests/validation.test.ts` | verified | `internal/messageinput/input_test.go`, `internal/cursor/cursor_test.go`, `internal/stagerequest/request_test.go`, CLI validation tests |

## Preserved acceptance facts

- Short Markdown and a 16,383-code-point near-limit message are stored and read
  back byte-for-byte by `tests/e2e/go-live_test.go`.
- Each normal or concurrent apply produces exactly one intended remote effect.
- Crash-after-acceptance remains `force_unknown`; ordinary replay is refused.
- Full post, attachment, reaction, edit, and delete lifecycle behavior runs
  against disposable Mattermost 11.8.3.
- Read, cursor, search, thread, and live-watch behavior runs against that same
  disposable server.

The frozen v1 suite remains reproducible from tag `v1.6.0`; it is no longer
required in the v2 working tree after this receipt and the parity matrix close.
