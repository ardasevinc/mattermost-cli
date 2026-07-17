# Mattermost CLI v2 Contract

Status: locked for implementation on `feat/go-v2-rewrite`

This document defines the Go v2 product, safety, storage, compatibility, testing, and release contract. Implementation may refine internal structure, but changing a behavioral invariant in this document requires an explicit contract update and regression coverage.

## 1. Product identity

`mm` is a Mattermost CLI for humans and agents. It provides honest, bounded reads and real-time watch output, plus deliberately staged remote mutations.

The central v2 rule is:

> No command that merely describes or prepares an operation may mutate Mattermost. Every remote mutation must first become a persisted, inspectable stage and then be applied by a separate command.

`mm apply <stage-id>@<revision>` is the only general remote-mutation boundary. The explicit revision binds apply to what the caller inspected. There are no direct-send, direct-edit, direct-delete, direct-react, or bypass aliases.

## 2. Cutover and compatibility

- TypeScript `v1.6.0` at commit `eccfc50` is the behavioral oracle during development.
- Go is developed beside the TypeScript implementation on the rewrite branch.
- The public `mm` binary remains v1 until Go reaches full read, watch, security, formatting, and current-send feature parity and satisfies this contract's expanded messaging surface.
- Public v2 is a clean break. Command output and JSON compatibility with v1 are not required.
- The internal dual implementation is temporary verification scaffolding, not a supported hybrid product.
- At cutover, the TypeScript implementation and Vitest harness are removed. Language-neutral fixtures, scenarios, schemas, and Docker acceptance tests remain.
- The first public Go release is `v2.0.0`.

## 3. Required read and watch parity

Go v2 must preserve every user-visible capability and negative guarantee currently covered by v1 for:

- `whoami`
- `teams`
- `users`
- `channels`
- `dms`
- `group-dms`
- `channel`
- `thread`
- `search`
- `mentions`
- `unread`
- `watch`
- `config`
- `doctor`

Parity means semantic parity, not a line-for-line port. In particular, v2 must preserve:

- bounded HTTP requests, pagination, concurrency, retries, and backoff;
- deterministic global selection and ordering;
- versioned opaque cursor validation and stale-boundary recovery;
- tri-state retrieval completeness and fail-closed incomplete-empty behavior;
- bounded thread hydration, missing-root tolerance, and honest partial metadata;
- deleted-content suppression and latest-visible-state fidelity;
- safe permalink construction;
- authenticated WebSocket handshake, heartbeat, bounded reconnect, sequence-gap diagnostics, and no false REST-backfill claims;
- strict stdout/stderr separation, especially JSONL watch output;
- hostile terminal, control, Unicode bidi, Markdown, URL, and remote-value handling;
- secret masking without retaining original secret values;
- exact active Mattermost credential masking even when heuristic redaction is disabled.

## 4. Go architecture and dependencies

The implementation targets Go 1.26 or newer and prefers small, explicit packages.

Approved foundations:

- CLI: `github.com/spf13/cobra`
- HTTP: standard library `net/http`
- database API: standard library `database/sql`
- SQLite driver: `modernc.org/sqlite`
- WebSocket: `github.com/coder/websocket`
- TOML: `github.com/pelletier/go-toml/v2`

The Mattermost REST client remains local rather than importing the large Mattermost server SDK. All dependencies require a health and license check before landing.

Expected package boundaries are configuration, transport, Mattermost API, retrieval, preprocessing, formatting, schema, stage store, mutation planning, mutation execution, and command orchestration. Package boundaries may change without altering this contract.

## 5. Configuration and local state

Configuration precedence remains:

1. CLI flags
2. environment variables
3. TOML configuration
4. hardcoded defaults

Configuration path:

- `$XDG_CONFIG_HOME/mattermost-cli/config.toml` when `XDG_CONFIG_HOME` is set and absolute;
- otherwise `~/.config/mattermost-cli/config.toml` on every operating system, including macOS.

When the selected XDG path differs from the v1 path, migration behavior is mandatory and deterministic:

- if the selected path exists, it is authoritative; an additional legacy file is ignored with a narrow warning;
- if the selected path is absent and the legacy path exists, v2 reads the legacy file as a read-only fallback and warns with both paths;
- if neither exists, configuration is absent normally.

v2 never silently moves, rewrites, merges, or deletes the legacy file. `config --init` and all explicit writes target the selected v2 path.

Retention policy uses two TOML keys with integer-second values:

```toml
stage_ttl_seconds = 0
stage_prune_after_seconds = 0
```

Both values must be non-negative integers. Zero disables the corresponding policy. A wrong type, negative value, or value that cannot be represented safely is a configuration error. TTL has no daemon: a positive policy is enforced opportunistically after store recovery whenever a writable stage operation opens the database. Read-only `stage list` and `stage show` never mutate local state. A bulk prune requires either a positive configured prune age or an explicit positive `--older-than` duration; zero never means prune everything.

State path:

- `$XDG_STATE_HOME/mattermost-cli` when `XDG_STATE_HOME` is set and absolute;
- otherwise `~/.local/state/mattermost-cli` on every operating system, including macOS.

The state directory is created with mode `0700`. The SQLite database and other private state files are created with mode `0600`. v2 refuses unsafe ownership, symlink, non-regular-file, or permission conditions when they could expose credentials or staged content. Unsupported permission semantics must be diagnosed honestly rather than claimed secure.

The database uses schema migrations, foreign keys, bounded busy handling, WAL where supported, integrity diagnostics, and best-effort secure deletion. Filesystem snapshots, backups, swap, and privileged local access are explicitly outside the confidentiality guarantee.

No token, token digest suitable for offline guessing, original secret captured by redaction provenance, remote error body, or shell command is stored in the stage database. Staged outbound bodies may contain caller-supplied heuristic secrets because redaction is display-only; they are protected as plaintext staged content under the retention boundary in section 14. The active Mattermost credential remains forbidden.

## 6. Machine contracts

Every structured input, JSON output, and JSONL event is self-identifying with a checked-in schema identifier under `mm/v2`.

Examples:

- `mm/v2/stage-request`
- `mm/v2/stage`
- `mm/v2/apply-receipt`
- `mm/v2/error`
- `mm/v2/watch-event`
- command-specific read envelopes under `mm/v2/<command>`

Checked-in JSON Schemas and golden examples are part of the release contract. Unknown fields may be added only where the schema explicitly permits them. Field removal, meaning changes, or type changes require a new schema identifier.

Thread envelopes never invent a root from zero-value or incomplete remote shape. `data.root` is the proven canonical root or `null`; rootless and otherwise unbound partial posts are retained in `data.unboundPosts`, with non-complete retrieval and partial visible-thread metadata.

`--json`, `--from-json`, and JSONL watch mode imply machine mode. Machine mode never prompts, launches an editor, emits ANSI, or mixes diagnostics into stdout. For a one-shot command that fails before any successful envelope emission, stdout is empty and one schema-valid `mm/v2/error` object is written to stderr. Watch diagnostics are JSONL error/diagnostic envelopes on stderr after any prior events. A low-level partial stream write can make byte-perfect recovery impossible; it is classified honestly and never followed by another object pretending the stream remained valid. Human and machine surfaces use the same underlying operations and validation.

Exit classes are stable:

- `0`: completed successfully, including an already-satisfied idempotent intent;
- `2`: invalid invocation or local input;
- `3`: configuration, authentication, authorization, or read failure before a mutation request is dispatched;
- `4`: a dispatched remote mutation was definitively rejected, including 401/403 from that mutation endpoint;
- `5`: remote mutation is partial or unknown and must not be ordinarily retried;
- `6`: local state, revision, claim, attachment-drift, or target-drift conflict;
- `7`: intended remote effect was confirmed but local receipt output or persistence failed; do not retry.

## 7. Staging grammar

Human-oriented creation commands follow one grammar:

```text
mm stage send dm <username>
mm stage send group <channel-id>
mm stage send channel <channel> [--team <team>]
mm stage reply <post-id>
mm stage post-edit <post-id>
mm stage post-delete <post-id>
mm stage react <post-id> <emoji>
mm stage unreact <post-id> <emoji>
mm stage dm-create <username>
mm stage group-create <username>...

mm stage list
mm stage show <stage-id>
mm stage revise <stage-id>
mm stage revise <stage-id> --revive
mm stage cancel <stage-id>
mm stage prune [--older-than <duration>]
mm stage prune <stage-id>@<revision> [--abandon-recovery] [--request-id <id>]

mm apply <stage-id>@<revision>
mm apply <stage-id>@<revision> --resume-partial
mm apply <stage-id>@<revision> --force-unknown
```

Exact flags and aliases may be added where they do not weaken the grammar. A top-level `send`, `edit`, `delete`, `react`, or conversation-creation command that bypasses staging is forbidden.

Every stage-creation action supports `--dry-run`. It performs the same read-only resolution, validation, plan construction, and safe preview but does not persist local state and cannot be applied. Structured requests express this as `persist: false`. This is v2's non-persisting replacement for v1 send dry-run.

Dry-run does not require or read message content, launch an editor, hash attachments, or persist a caller request ID. It previews target resolution and the possible remote-step shape without claiming content validation. This preserves v1's bodyless destination preview.

Operational inspection surfaces are also required:

```text
mm store doctor
mm store migrations
mm schema list
mm schema show <schema-id>
mm schema validate <schema-id>
```

They are read-only unless a future contract explicitly stages a repair operation.

Top-level posts may target any exact joined conversation: direct, group direct, public channel, or private channel. Name-based public/private channel resolution is team-aware and fails on ambiguity. Exact channel IDs remain supported.

## 8. Content input

Message, reply, and post-edit stages accept exactly one content source:

- piped stdin, preserved as exact valid UTF-8 bytes;
- `$VISUAL`, then `$EDITOR`, only when stdin is a TTY and human mode is active;
- explicit `--message <text>`, documented as visible to shell history and process inspection.

Structured agent creation is also supported:

```text
mm stage --from-json
```

It consumes one versioned `mm/v2/stage-request` object from stdin. It is the canonical structured agent input, while human subcommands remain first-class rather than wrappers with weaker behavior. Persisted structured requests require a caller-generated `requestId`. Uniqueness is scoped to the normalized server base URL and authenticated user. An identical replay returns the existing stage and receipt; reuse with different semantic content is a local conflict. Human commands may supply the same behavior through `--request-id`.

Stage-create replay equality is caller-intent equality, not equality of the resolved remote snapshot. The digest domain is `mm/v2/stage-request/caller-intent/v1` and binds the operation, unresolved caller target, nullable body and emoji, and ordered normalized attachment metadata. It excludes request ID, server scope, resolved remote facts, file bytes and hashes, sizes, and detected MIME. An identical replay authenticates, loads the original store-authoritative destination and plan, validates content input where applicable, and performs no target, reaction, or file read. Different caller intent conflicts even if it would currently resolve to the same remote target.

Revision replay uses the same fail-closed model with a distinct `mm/v2/stage-revise-request/caller-intent/v1` digest domain. It binds the immutable operation, stage ID, expected revision and semantic digest, revive intent, nullable replacement body, and nullable ordered attachment metadata. Null body or attachment metadata preserves the stored value; an empty attachment list clears it. The digest excludes attachment file bytes and derived file facts, allowing an identical retry to return its durable receipt before reopening a missing or changed source file.

Migration 3 deliberately tombstones pre-caller-intent `mm/v2/stage-request` receipts as `mm/v2/legacy-stage-request-conflict`. Migration 4 likewise tombstones pre-caller-intent `mm/v2/stage-revise-request` receipts as `mm/v2/legacy-stage-revise-conflict`. Those older digests cannot prove caller-intent equality, so they conflict rather than replay, resolve remote state, or reopen source files. Cancel receipts are unchanged. This v2 database format remains development-only and has not shipped; the explicit tombstones preserve deterministic local upgrade behavior without claiming false replay compatibility.

Structured apply is also first-class:

```text
mm apply --from-json
```

It consumes one `mm/v2/apply-request` containing `requestId`, `stageId`, `revision`, expected semantic digest, and exactly one `recoveryMode`: `ordinary`, `resume_partial`, or `force_unknown`. Identical replay returns the existing attempt receipt without dispatch. Reuse of a request ID with different content conflicts. Flags and structured recovery mode cannot be combined.

Apply replay equality uses the digest domain `mm/v2/apply-request/caller-intent/v1`. It binds the stage ID, exact revision, expected semantic digest, and recovery mode, and excludes the request ID and server scope. Human `--request-id` and structured apply therefore share one replay identity without allowing a caller-generated key to alter the intent it names.

Every machine-issued local state mutation, including revise, cancel, revive, and destructive prune, has a versioned request schema and required caller request ID with the same identical-replay/conflicting-reuse semantics. Human subcommands may opt into those semantics with `--request-id`.

Input methods are mutually exclusive. Empty or whitespace-only content is rejected. UTF-8 failure is fatal. The current Mattermost limits remain locally enforced before any remote mutation: at most 16,383 Unicode code points and 65,535 UTF-8 bytes, unless a verified server capability establishes a lower bound.

The active Mattermost credential is rejected in outbound text, stored paths, filenames, attachment metadata supplied by the caller, structured mutation fields, and attachment bytes before persistence or remote dispatch. Attachment scanning is exact-byte streaming with boundary-safe chunk handling and is repeated against the apply-time spool.

## 9. Stage identity and revision binding

Every stage has:

- an opaque high-entropy stage ID;
- an immutable creation timestamp;
- a monotonically increasing revision;
- an operation type;
- the complete normalized Mattermost API base URL, including any base path, and authenticated Mattermost user ID;
- canonical destination, participant, post, and channel IDs as applicable;
- a normalized operation plan;
- a digest of every semantically relevant field;
- lifecycle state and timestamps;
- zero or more independently journaled apply attempts.

Stage creation is online when remote identity or target resolution is required. Resolution is read-only. The exact target and authenticated account are bound at creation. Apply revalidates that same identity and refuses if credentials now represent another user, the complete API base URL changed, a verified stable server identifier changed when available, access changed, or the target's identity/type no longer matches.

Revising a stage creates a new revision and marks older revisions superseded. Apply requires `<stage-id>@<revision>` and transactionally compare-and-swaps that exact current revision plus its semantic digest into an applying claim. Structured apply requests also carry the expected digest. Human `stage show` prints the exact apply reference. A concurrent revise produces a local conflict rather than applying unreviewed content. Concurrent processes cannot both ordinarily apply one revision.

Only operations with mutable composition may be revised. Top-level posts and replies may replace body and attachments; post edits may replace body only. Delete, react, unreact, and conversation-resolution stages reject revision instead of creating meaningless no-op revisions.

`stage list` and ordinary receipts omit message bodies and attachment contents. `stage show` is an explicit content-revealing operation and may display the staged body. Human output warns accordingly; machine output includes content only for that explicit command.

## 10. Attachment binding

Attachments are path-backed, not copied into SQLite or managed blob storage.

At staging time v2 accepts at most five ordered attachments, matching the maximum file-ID set accepted by a Mattermost post. It records, at minimum:

- caller-provided path;
- canonical path information needed for later safe reopening;
- filename used remotely;
- byte length;
- detected or explicit media type;
- cryptographic content digest.
- local file identity needed to reject replacement even when replacement bytes are identical.

Apply securely reopens and rehashes the file. Missing, inaccessible, non-regular, replaced, symlink-swapped, resized, or digest-mismatched files cause a local conflict before upload. v2 never silently uploads bytes different from the staged revision.

Development databases upgraded from a pre-identity schema cannot safely infer this binding. Their existing attachment revisions remain inspectable but ineligible for apply until an ordinary revision securely rebinds the attachment sources.

To close the hash-to-upload race without retaining attachment copies between commands, apply copies every securely opened source into a private `0600` spool file while hashing it before the first remote dispatch. Only a complete spool whose identity, digest, and length match the staged revision may be uploaded. Each completed spool is unlinked while its descriptor remains open; upload reads that descriptor, not the original path. Thus path-backed staging remains low-storage at rest while dispatched bytes are an immutable snapshot of the reviewed file.

Spools are execution-only snapshots and never durable recovery artifacts. Process exit closes their descriptors and leaves no pathname to reconcile. A later explicit recovery securely reopens and respools every staged source before any new dispatch; if any exact bound source is unavailable or changed, recovery fails closed until the source is restored or recovery is explicitly abandoned. A contiguous prefix of directly validated upload steps from terminal attempts on the same stage revision and semantic digest may recover from its journaled file IDs after fresh remote metadata revalidation. Each ordinal binds directly to the attempt that dispatched and validated that upload; reused provenance cannot chain through another recovery attempt. Only the proven-not-applied suffix is uploaded, in original ordinal order, and every reused or newly uploaded file is revalidated again immediately before the final post dispatch.

Attachments are non-empty and limited to five per post. Server per-file upload limits are checked before upload when discoverable; a dispatched `413` is a definitive rejection. Checked arithmetic caps an attempt's aggregate spool bytes at 512 MiB, and apply refuses to begin unless the private state filesystem can retain that snapshot while preserving a 64 MiB free-space reserve. Upload and post creation are separate remote substeps and therefore use the compound-operation journal.

## 11. Supported remote mutation lifecycle

The first complete v2 supports the Mattermost message lifecycle, not server administration:

- create top-level posts in all joined conversation types;
- create replies;
- upload and attach files;
- edit the authenticated user's posts;
- delete the authenticated user's posts;
- add and remove the authenticated user's reactions;
- create or resolve direct conversations;
- create group direct conversations from an exact participant set.

Explicitly excluded from v2.0:

- public/private channel creation, archival, membership, or moderation;
- team, user, role, permission, bot, webhook, or server administration;
- scheduled server-side sending;
- automatic content generation or autonomous recipient selection.

Stage creation captures enough current remote state to make later drift visible:

- edit/delete bind author, post ID, channel ID, update timestamp, and relevant content digest;
- reply binds root/channel identity;
- react/unreact bind post/channel/emoji and current-user reaction state;
- group creation binds a deduplicated canonical participant-ID set;
- channel sends bind channel ID, type, team identity where applicable, and membership/access.

The closed destination binding carries explicit nullable `postState` and `reactionPresent` fields. `postState` is present only for edit/delete and contains the exact author ID, positive Mattermost `update_at` millisecond value, and a lowercase SHA-256 content digest. `reactionPresent` is present only for react/unreact and records whether the authenticated user already has that exact emoji reaction. Every other operation emits these fields as `null`; absence is not used to mean unknown.

The post content digest is SHA-256 over canonical UTF-8 JSON with the fixed field order `message`, `fileIds`, `rootId`, `type`. Normally `message` is the exact string. If it contains an exact active Mattermost credential, `message` is instead an object containing only the ordered non-credential text fragments; neither the credential nor a verifier for its value enters the digest. This exceptional representation deliberately makes credential values indistinguishable while remaining type-distinct from every ordinary message string. The exact positive `update_at` binding remains mandatory, including for credential-elided content, so edit/delete can remediate a leaked credential without accepting later target drift. File IDs preserve the server's validated order because attachment order is user-visible. Arbitrary props, presentation metadata, and reactions are excluded from this digest.

Reply staging accepts a live accessible root or reply. It binds the selected post ID and the canonical root ID; targeting a reply requires a fresh root read proving a live root with an empty `root_id` in the same channel. Reply and reaction staging may target any otherwise valid live accessible post. Edit and delete additionally require an ordinary user post (`type` empty) authored by the authenticated user. An edit whose desired body already equals the freshly revalidated current body completes as already satisfied without a write. A target deleted after staging is drift, not already satisfied.

Reaction state comes from the authoritative fresh post-reactions endpoint, not cached or embedded post metadata. The response must be complete, exact-post-bound, duplicate-free, and contain only canonical user/post/emoji identities. Both present and absent states are therefore positive facts. React and unreact use conditional plans and complete without a write when revalidation shows the desired state already holds.

Apply re-fetches relevant state. Changed, deleted, re-authored, moved, inaccessible, or ambiguously resolved targets fail closed. Already-satisfied reaction state succeeds without a write and is reported as such.

## 12. Compound operation semantics

One stage may represent multiple remote API steps required by one reviewed user intent, including conversation creation, file upload, and post creation.

`stage show` exposes the ordered plan and conditional steps. Apply journals before and after every substep. A later failure never erases known earlier effects.

Rules:

- read-only preparation may use bounded retry policy;
- a remote mutation is never automatically replayed after dispatch;
- redirects are rejected for mutation requests;
- timeout, transport failure, malformed success, unexpected identity, and unvalidated success are `unknown`;
- a known response proving rejection is `rejected`;
- known completed early steps followed by rejection are `partial` if externally visible residue remains;
- any unknown substep stops the plan immediately;
- safe known completed results, such as a validated created channel or uploaded file ID, may be reused by an explicit partial resume or uncertainty override;
- uncertain non-idempotent substeps are never silently reused or replayed.

The CLI never claims distributed exactly-once delivery. Under normal process control it makes at most one dispatch attempt for each substep attempt. The journal records `dispatch_intent` before handing work to the transport, then `response_validated`, `rejected`, or `outcome_unknown` when observation permits. A crash between those records is recovered conservatively and never converted into proof that a request was or was not dispatched.

## 13. Stage and attempt states

Stage lifecycle and remote attempt outcome are separate axes.

Stage lifecycle states:

- `open`: the current revision may be eligible for apply or explicit recovery;
- `applying`: one process holds the transactional claim for one exact revision;
- `completed`: the intended effect is confirmed or already satisfied;
- `canceled`: local eligibility was explicitly revoked;
- `expired`: TTL policy revoked eligibility;
- `pruned`: sensitive local content was removed while required audit tombstones remain.

Revision states are `current` or `superseded`. Apply attempt outcomes are `succeeded`, `already_satisfied`, `rejected`, `partial`, or `unknown`. Cancel, expiry, and prune never overwrite or reinterpret an attempt outcome.

Each apply attempt has its own ID, pending-post ID where applicable, plan snapshot, substep journal, start/end timestamps, and outcome.

Every stage also has an aggregate recovery requirement derived monotonically from its complete attempt history:

- `none`: ordinary apply is safe if all other eligibility checks pass;
- `resume_partial`: confirmed reusable effects exist and every remaining effect is proven not applied;
- `force_unknown`: at least one prior effect may have occurred and duplicate risk remains;
- `forbidden`: the stage is completed or its lifecycle no longer permits application.

`force_unknown` dominates `resume_partial`, which dominates `none`. A later rejected attempt never erases uncertainty from an earlier attempt. Only confirmed completion or explicit lifecycle closure changes recovery to `forbidden`.

The store separately tracks retained recovery material as `none`, `resume_partial`, or `force_unknown`. Public recovery answers whether and how a stage may be applied; retained recovery material answers whether pruning would deliberately destroy evidence or inputs needed for partial/unknown recovery. Cancel changes public recovery to `forbidden` but does not erase retained recovery material. Confirmed completion clears it. Explicit recovery abandonment clears it only after an append-only audit tombstone is durable.

Normal transitions are:

| Event | Lifecycle after event | Attempt outcome | Recovery requirement |
| --- | --- | --- | --- |
| create or safe revise | `open` | unchanged | inherited from complete history |
| ordinary claim | `applying` | pending | unchanged |
| local failure proven before dispatch | `open` | no attempt or `rejected` | prior requirement |
| all intended effects validated | `completed` | `succeeded` or `already_satisfied` | `forbidden` |
| definitive rejection with no externally visible residue | `open` | `rejected` | prior requirement |
| confirmed residue, all remaining effects proven not applied | `open` | `partial` | max(prior, `resume_partial`) |
| any uncertain effect | `open` | `unknown` or uncertainty-bearing `partial` | `force_unknown` |
| cancel eligible open stage | `canceled` | unchanged | `forbidden` |
| expire eligible inactive stage | `expired` | unchanged | `forbidden` |
| prune eligible inactive stage | `pruned` | unchanged | `forbidden` |

An interrupted or stale `applying` claim creates an `unknown` attempt outcome unless the journal proves no remote mutation dispatch was handed to the transport. The stage returns to `open` with aggregate `force_unknown`. Lease expiry never makes a mutation ordinarily replayable.

Revise is refused while `applying`, while recovery is `resume_partial`, or after `completed`, `canceled`, or `pruned`. Partial recovery is bound to the exact immutable attachment ordinals that produced its confirmed effects; it must be completed or explicitly abandoned before composition can change. An expired stage may be revised only with `stage revise <id> --revive`, which atomically returns it to `open` with a new revision. Revision never clears unknown recovery history: revising after an unknown attempt carries `force_unknown` to the new revision. Revised content is therefore still gated by the unresolved risk of prior effects.

Ordinary `mm apply <id>@<revision>` requires the exact current revision, lifecycle `open`, and recovery requirement `none`.

`mm apply <id>@<revision> --resume-partial` requires recovery `resume_partial`. It preserves the prior attempt, claims the exact same revision and semantic digest, assigns fresh idempotency/pending IDs to new substep attempts where applicable, and continues only effects proven not applied. Attachment recovery reuses only a contiguous direct validated-upload prefix after exact remote file metadata revalidation; no uncertain or already-reused source step qualifies. A definitively rejected request is proven not applied and may be retried this way. Resume is forbidden when any attempt in the stage's history remains uncertain.

`mm apply <id>@<revision> --force-unknown` requires aggregate recovery `force_unknown`. It:

- emits an unmistakable duplicate/side-effect warning in human mode;
- requires the flag in machine mode without prompting;
- preserves all earlier attempts immutably;
- creates a new attempt and new pending-post ID where applicable;
- reuses only effects that were previously validated and remain safe;
- records that the caller knowingly accepted duplicate risk.

## 14. Retention and confidentiality

Staged bodies are plaintext SQLite data protected by local filesystem permissions. This is a deliberate portability choice, not encryption at rest.

After confirmed success:

- message/edit body content is cleared from active stage storage;
- local attachment paths and caller-only sensitive composition metadata are cleared when no longer needed;
- destination IDs, digests, timestamps, attempt journal, and narrow Mattermost receipts remain for audit;
- confirmed remote content is never copied back into the receipt record.

Rejected, partial, unknown, and unapplied stages retain the minimum content required for inspection and deliberate recovery until eligible cleanup.

TTL is configurable and disabled by default. Age is measured from `stages.updated_at`, which advances on revisions and attempt activity. Opportunistic expiry applies only to inactive open stages whose public recovery and retained recovery material are both `none`; it never races an applying claim or expires a recovery-eligible stage. Expiry changes lifecycle eligibility, retains staged content, records an append-only retention event, preserves prior attempt outcomes, and does not pretend secure erasure. Reviving an expired stage records a corresponding retention event.

Cancel is refused while applying. On an open stage it revokes future apply without rewriting history or discarding retained recovery material.

Bulk `stage prune` is an explicitly human maintenance action. It atomically selects only completed, canceled, and expired stages older than one fixed explicit or configured cutoff, with no claim and no retained recovery material. It has no caller request ID, emits a bounded count-only result, and fails closed if no positive age exists. Structured machine prune is always exact and replayable.

Exact `stage prune <id>@<revision>` bypasses the age threshold because the exact reference is deliberate intent, but still refuses applying or claimed stages and requires the current revision and semantic digest to remain unchanged. Ordinary exact prune accepts only completed, canceled, or expired stages with no retained recovery material. Removing retained staged content and source bindings from a stage with `resume_partial` or `force_unknown` material requires the exact reference plus `--abandon-recovery`; this makes the stage `pruned`, public recovery `forbidden`, clears the retained-material marker, and preserves an append-only audit tombstone stating that recovery was deliberately abandoned. No durable attachment spool exists outside SQLite source bindings.

Every exact structured prune uses `mm/v2/stage-prune-request`, includes caller request ID, stage ID, expected revision, expected digest, and the abandonment choice, and reuses the stored stage's server/user replay scope. Its narrow result reuses `mm/v2/stage-receipt` with action `pruned`. Bulk human prune emits `mm/v2/stage-prune-result` with the fixed cutoff, pruned count, and recorded timestamp; it never emits an unbounded stage list.

## 15. Receipts, errors, and output failure

Mutation receipts are narrow local projections. They never include full message bodies, attachment bytes, raw Mattermost objects, arbitrary response fields, remote response bodies, credentials, or original secret values.

Receipts include only fields needed to establish:

- schema and operation type;
- stage ID, revision, and attempt ID;
- destination and authenticated identity in sanitized narrow form;
- each planned substep's known state;
- direct reused-upload provenance as `reusedFrom.attemptId` and the identical source ordinal when partial recovery did not redispatch that upload;
- canonical post/channel/file identifiers when validated;
- server creation/update timestamps when validated;
- overall `succeeded`, `rejected`, `partial`, `unknown`, or `already_satisfied` outcome;
- stable `recovery` value: `none`, `resume_partial`, `force_unknown`, or `forbidden`.

`stage list`, `stage show`, apply receipts, and structured errors expose the same recovery enum. Human output renders the exact next safe command when recovery is available.

If the intended remote effect is confirmed but database receipt persistence or stdout emission fails, v2 reports a distinct confirmed-effect failure and instructs the caller not to retry. It must be testable when stdout closes or disk persistence fails.

Remote error bodies, reason phrases, headers not explicitly allowlisted, and unsanitized caller-controlled values are never reflected in errors. Explicit content-revealing commands such as `stage show` and narrow sanitized destination receipts are the documented exceptions to general output minimization.

## 16. Security invariants

The v1 security boundary survives the rewrite:

- HTTPS is required except explicitly allowed loopback HTTP for local testing.
- URLs with credentials, query strings, fragments, unsafe schemes, or ambiguous normalization fail closed.
- URL normalization is transport-aligned rather than blindly WHATWG-compatible. Safe canonicalizations such as IDNA hostnames, dot-segment resolution, lowercase hosts, and default-port removal are preserved. Host spellings that Go's dialer could interpret differently from validation, including shorthand or leading-zero IPv4, and backslash authority forms are rejected. Loopback HTTP requires `localhost` or a canonical `netip` loopback address.
- User-controlled path components are encoded.
- Mutation requests do not follow redirects.
- Active credentials are ownership-scoped and fully masked everywhere.
- Heuristic secret redaction is display-only and may be disabled; credential masking and terminal/control sanitization may not.
- Original secret values and unredacted originals never appear in redaction provenance.
- Canonical unsanitized IDs drive identity, ordering, grouping, and deduplication; sanitized copies are presentation-only.
- Go RE2 incompatibilities are implemented with candidate matching plus explicit boundary validation, not weaker regex substitutions.
- Regex and literal-match spans use UTF-8 byte offsets internally so Go slicing remains exact. Public structured-redaction `position` values are UTF-16 code-unit offsets into the final sanitized emitted text, preserving the v1 oracle contract across non-BMP text. Partial heuristic masks compute visible prefix/suffix lengths in Unicode scalar values so v2 never splits a code point; this is an intentional presentation-only change from JavaScript UTF-16 slicing.
- JSON request bodies contain no encoder-added trailing newline, disable HTML escaping, and preserve literal U+2028/U+2029 like `JSON.stringify`; string content that literally contains `\\u2028` or `\\u2029` remains escaped data.
- Files, config, database, editor temp handling, and subprocess invocation resist symlink and permission attacks within the documented local-user threat model.

## 17. Test and conformance contract

The rewrite is accepted by evidence, not source resemblance.

### 17.1 Frozen oracle

- Tag `v1.6.0` and commit `eccfc50` are immutable oracle inputs.
- Current evidence baseline: 33 Vitest files, 517 tests, shared-state `--no-isolate` pass, Biome/typecheck/build pass, dependency audit pass, exact npm tarball smoke, and disposable Mattermost 11.8.3 E2E pass.
- The existing tests are inventoried into a language-neutral parity matrix before removal.
- The matrix explicitly disposes every current command, flag, environment variable, TOML key, output mode, exit behavior, warning, agent-detected default, and release smoke. It must include `mention_names`, `MM_REDACT`, relative-time agent detection, `--dry-run`, no-color format selection, version checks, and config permission behavior even when they lack broad E2E coverage.

### 17.2 Language-neutral fake server

A fake Mattermost server and scenario manifests capture:

- exact request method, URL, headers, body bytes, count, and order;
- response, disconnect, timeout, malformed payload, redirect, and rate-limit behavior;
- stdout bytes, stderr bytes, exit class, and machine-schema validity;
- resulting server and local-store state.

Where v1 and v2 semantics intentionally match, both binaries run the same scenario. Where v2 intentionally changes output, scenarios compare normalized semantic results and enforce the new schema.

### 17.3 Go test layers

Required layers:

- table-driven unit tests for validation, formatting, preprocessing, state transitions, planners, and receipts;
- golden tests for every human format and checked-in machine schema example;
- fuzz tests for URL parsing, cursors, UTF-8/input bounds, JSON/TOML decoding, secret detection, sanitization, pagination payloads, and stage requests;
- HTTP contract tests for all Mattermost endpoints and mutation no-replay behavior;
- SQLite migration, permissions, integrity, multi-process claim, busy, and recovery tests;
- `go test -race ./...` for concurrency-sensitive packages and the full suite where practical;
- subprocess tests for signals, editor behavior, stdin, closed stdout/stderr, exit classes, and exact byte preservation;
- fault-injection tests for crash-before-dispatch, crash-after-journal, crash-after-server-acceptance, disk-full, failed commit, stale claim, changed attachment, changed destination, and failed receipt output;
- transition and race tests for show-revise-apply, revise/cancel/prune versus applying, known-partial resume, uncertainty-bearing expiry/cancel, and stale revisions;
- recovery-history tests for unknown then forced rejection, revise after partial/unknown, definitively rejected suffix resume, and the impossibility of clearing aggregate uncertainty through a later outcome;
- adversarial attachment tests for in-place writes during spool creation, inode/path replacement, truncation, symlink swaps, and active-credential bytes;
- execution-spool tests for pre-dispatch crash cleanup, stale applying retention, and successful cleanup, plus SQLite source-binding tests for explicit recovery abandonment;
- lost-output idempotency tests for stage creation and revision as well as apply receipts;
- security fixture parity for every v1 secret pattern and hostile presentation input;
- artifact tests against the exact released archives and npm platform packages.

### 17.4 Disposable Mattermost acceptance

Docker E2E remains isolated from real users and creates unique fake accounts and conversations. It must prove at least:

- exact short and near-limit long Markdown storage/readback;
- final-newline and Unicode fidelity;
- stage creation causes no Mattermost mutation;
- apply creates exactly the intended post under normal success;
- DM and group-DM creation and exact participant binding;
- public/private channel sends with team-aware resolution;
- replies, edits, deletes, reactions, unreact, files, and compound plans;
- unknown-outcome behavior without automatic replay;
- explicit `--force-unknown` audit behavior;
- concurrent apply claim exclusion;
- body clearing and receipt retention after confirmed success;
- read, search, thread, cursor, and watch behavior against a real server.

No configured workplace server or real account is used by automated tests.

### 17.5 Release gates

Before v2 cutover:

- all unit, integration, conformance, fuzz-seed, race, fault, and Docker E2E gates pass;
- `go vet`, `staticcheck`, `govulncheck`, formatting, module verification, and dependency license checks pass;
- darwin arm64/amd64 and linux arm64/amd64 artifacts build reproducibly enough for checksum verification;
- each exact archive installs and runs version, help, doctor, `store doctor`, and schema smokes;
- Homebrew installation and upgrade are tested;
- npm optional-platform packages install the correct binary and verify checksum/version;
- the working tree and generated schema artifacts are clean.

## 18. Implementation sequence

Implementation proceeds in reviewable conventional commits:

1. freeze and inventory the v1 behavioral oracle;
2. add Go module, command skeleton, build metadata, and baseline gates;
3. add machine schemas and the language-neutral scenario harness;
4. port config, URL policy, transport, identity, doctor, sanitization, and redaction;
5. port read and formatting surfaces in vertical slices;
6. port cursors, pagination, thread hydration, and completeness semantics;
7. port watch and reconnect/gap behavior;
8. add SQLite migrations and offline stage management;
9. add target-bound stage creation and revision handling;
10. add apply claiming, single-step mutations, receipts, and unknown outcomes;
11. add compound conversations/files/posts and recovery journal;
12. add full message lifecycle operations;
13. complete differential, race, fault, and Docker acceptance;
14. add GitHub, Homebrew, install-script, `go install`, and npm-shim distribution;
15. remove the TypeScript implementation after every gate passes;
16. cut `v2.0.0`, verify live artifacts, and synchronize release receipts.

## 19. Distribution and migration

Primary Go v2 distribution:

- GitHub release archives for darwin arm64/amd64 and linux arm64/amd64;
- checksums and verifiable build provenance;
- Homebrew using the established patterns in Arda's current Go projects;
- install script with checksum verification;
- `go install github.com/ardasevinc/mattermost-cli/v2/cmd/mm@v2.0.0` for source-based installation.

The existing unscoped npm package remains an upgrade path for prior npm users. Its v2 package becomes a small launcher backed by platform-specific optional packages containing the exact Go release binaries. It must not fetch and execute an unverified binary during `postinstall`. npm installation continuity does not imply v1 command or schema compatibility.

Windows is not a supported v2.0 release target. The code should avoid gratuitous portability barriers, but no Windows guarantee exists until its filesystem, signal, terminal, SQLite, and artifact behavior receive native testing.

## 20. Rollback

- Before public cutover, rollback is simply continued use of tagged TypeScript `v1.6.0`.
- v2 never mutates or deletes v1 configuration during migration.
- The initial v2 database is new state and has no automatic downgrade promise.
- Release documentation preserves explicit v1.6.0 reinstall instructions during the v2 migration window.
- A failed v2 release is fixed forward or withdrawn without rewriting existing tags or release assets.

## 21. Interview decisions captured

- full public feature parity before cutover;
- no behavioral backwards-compatibility requirement;
- universal `stage` / `apply` mutation grammar;
- target binding during stage creation;
- private plaintext SQLite storage;
- confirmed-send body clearing with receipt retention;
- force of unknown outcomes only through `--force-unknown`;
- full message lifecycle, excluding administration;
- all joined conversations as top-level post targets;
- path-plus-digest attachment binding;
- explicit compound plans;
- XDG state path with no macOS application-support fallback;
- stdin, editor, and explicit `--message` human input;
- additional structured JSON agent input;
- versioned schemas for every machine surface;
- darwin/linux arm64/amd64 release matrix;
- Go-native release plus Homebrew and npm migration shim;
- configurable TTL with never-expire as the default.
