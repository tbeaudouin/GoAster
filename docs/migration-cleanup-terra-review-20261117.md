# Migration cleanup review — 2026-11-17

Verdict: No concrete regression found in the reviewed repository diff. No production code or tests were changed in this review. This is an offline source and test review; it does not establish the behavior of the separately deployed Web service or existing installations.

## Evidence and boundaries

- At HEAD, the migration scheduler constructor, operations adapter, and startup function were definitions without production callers. The active registration in `cmd/goaster-agent/main.go` already supplied `CreateOnce`; the removed lifecycle implementation and its storage were reachable through those unused adapters or tests, not the repository's production entry points. The current registration retains immediate and scheduled one-shot creation. This conclusion is scoped to callers in this repository, not external consumers of exported Go APIs or old binaries.
- `internal/chat/migration_tool.go` exposes only create, requires action, server type and exact name, rejects unknown arguments, and requires a private conversation. `internal/migration/migration.go` validates the fixed lowercase hexadecimal name and request fields before effects. Approval and reuse of the exact approved terms are instruction-led, not independently bound to a persisted approval record; the skill states that limitation.
- Generic cron retains the matching execution claim, single call per scheduled turn, and retry only for `not_created` plus `no_capacity` in `cmd/goaster-agent/generic_cron.go`. Its real-session tests cover retry, terminal outcomes including same-name and lookup failures, repeated calls, errors, timeout, changed or stopped jobs, and crash recovery. Scheduler tests cover durable claims. The exact name is carried in the job message and request; enforcement of the original conversational approval depends on the model following the instructions.
- `CreateOnce` checks the paired recipient before creating and again before delivery; Telegram's split delivery checks the recipient between the URL and private-key messages. Existing one-shot and Telegram tests exercise mismatches, partial delivery, failure boundaries and redaction. The removed combined-handoff tests exercised the removed path; active split-handoff and transport diagnostic tests remain.
- The one-shot request sends the exact type and name to Web, and validates matching labels in successful and `target_exists` responses. Tests exercise target-exists, lookup failure and unusable responses. The Web contract assigns public admission, provider exact-name lookup and architecture/image validation to Web. The deleted architecture tests covered the removed source-side lifecycle, not the active one-shot implementation. This repository does not contain the Web implementation, so its provider ordering, architecture validation and deployed behavior cannot be verified here; no network or provider calls were made.
- Diagnostics retain bounded allowlists and tests cover secret-bearing remote fields and transport errors. Removed diagnostic tests were tied to the deleted lifecycle; active one-shot and transport redaction coverage remains.
- Searched surviving tracked and untracked content for removed lifecycle symbols and historical document references. Matches were the intentional absence assertion and current descriptions of the absence of a separate scheduler. A local Markdown link scan found no missing relative targets; the packaging skill-asset check also passed. This is a search-based assessment, not proof against every possible unlinked historical reference.

## Verification

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s` — passed.
- `sh packaging/test-skill-assets.sh` — passed offline.
- `git diff --check` — passed before this review document was written.
- An initial test attempt using `go` from PATH failed before running tests because it is Go 1.22.2 and the module requires Go 1.25; the explicitly located local toolchain passed above.

No credentials, network, provider, deployment, or Git write operations were used.
