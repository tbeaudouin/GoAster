# Final cron create handoff review

Date: 2026-11-16

Verdict: **changes required**. The ordinary-cron regressions identified by Terra are fixed, but the retry decision does not observe every migration-tool rejection, diagnostic ownership is lost at the final cron boundary, and the checked-in Web contract does not express the requested admission order.

## Findings

### 1. A rejected second migration call can leave automatic retry armed

Locations: `internal/chat/migration_tool.go:115` and `:122`; `cmd/goaster-agent/generic_cron.go:121`, `:138`, and `:180`.

The once-only callback clears retry on a second invocation, but parsing and action rejection return before that callback. These rejections are JSON tool results with a nil Go error. A successful session therefore retains the first call's no-capacity retry decision even though a subsequent migration-tool invocation failed. This contradicts the requested terminal treatment of errors and the documented repeated-call safeguard. It does not demonstrate duplicate creation in the same turn; it demonstrates an automatic later attempt after a terminal tool error.

Minimal offline reproduction using the existing real-session harness in `cmd/goaster-agent/generic_cron_test.go:412`:

1. Have the scheduled create callback return `not_created/no_capacity`.
2. Invoke the registered migration handler once with `scheduledMigrationToolArguments`.
3. Invoke it again with `{"action":"create","server_type":7}` (or a string action other than `create`). Observe `invalid_arguments` with nil error.
4. Return a successful model reply and let enqueue succeed.
5. Inspect the job: the unchanged `retry=true` path returns `scheduler.ErrRetry`, leaving the job enabled for another 1800-second attempt rather than stopped for review.

This is a source-traced reproduction, not an additional test written or run during this read-only review. The existing second-call test uses valid arguments twice and consequently reaches the callback twice; it misses this path. Minimal correction: let the transport-owned per-turn decision observe rejected migration invocations too, preserving ordinary cron behavior and the approved conversational trust model. No new scheduler or approval layer is needed.

### 2. Final cron handling still duplicates already-owned ERROR diagnostics

Locations: `cmd/goaster-agent/generic_cron.go:183`; `internal/migration/oneshot.go:186` and `:233`; `internal/telegram/migration.go:200` and `:227`.

The final `review` branch emits a new generic `agent.cron.migration` ERROR for results whose failure was already diagnosed by migration or Telegram. The result struct carries no diagnostic-ownership information to this boundary. Terra's specific caller fix in `reportScheduledMigrationOutcome` is correct, but it does not prevent this later duplicate.

Minimal offline reproduction: run a qualifying scheduled turn through `migration.CreateOnce`, return a valid `not_created/provider_lookup_failure` Web response, allow outcome reporting and the model reply/enqueue to succeed. `CreateOnce` emits `migration.create`; the executor then emits `agent.cron.migration` for the same failure. Alternatively, use the split-document EOF fixture from `internal/telegram/migration_test.go:165` inside the scheduled callback: Telegram owns the transport ERROR, `CreateOnce` honors its marker, but the final cron review branch logs again. A failed outcome report similarly reaches this final branch despite the main callback honoring `DiagnosticLogged`.

Keep the durable stop/review behavior while preserving diagnostic ownership through the executor. The owning record should retain the complete cause and safe context. These duplicate records do not expose secrets, but they violate the repository's explicit one-boundary logging rule.

### 3. The Web contract still omits public admission before lookup

Locations: `docs/api-first-web-contract-20261116.md:3` and `:11`; repeated wording in `skills/goaster-migration/AUTH.md:7` and `docs/goaster-migration.md:3`.

The file currently requires exact-name lookup before “Redis” work and says lookup happens first. It contains no public-admission exception or admission-first sequence. This differs from the supplied requirement: preserve pre-existing public admission, then perform read-only exact-name lookup, then capacity/migration-record/key/create work. An admission implementation using Redis would conflict with the current blanket wording.

Reproduce by reading the cited paragraphs or searching this contract for `admission`: no such ordering is documented. Minimal correction is documentary: explicitly preserve existing public admission before lookup and narrow the later storage step to migration-record work. This review requests no new admission or policy layer. The existing `target_exists` stop-without-adoption contract is correct.

## Independent disposition of Terra findings

- **Ordinary recurring cron:** fixed. The executor no longer durably claims every ordinary job. `TestGenericCronOrdinaryRecurringRunsTwice` exercises two actual scheduler/session runs and confirms two delivered replies, enabled state, run count two, and a next run.
- **Ordinary self-management and busy retry:** fixed at the reviewed boundary. Scheduled cron mutations remain available until an actual migration starts; `TestScheduledCronManagementAndMigrationGate` covers ordinary add/disable and the later gate. The production-shaped busy test preserves ordinary retry even for a private 1800-second, one-run reminder. The self-management test is a handler test, not a complete model-driven production integration test; source inspection confirms the gate is conditional on migration having started.
- **Premature retry promise:** fixed. The no-capacity private message states only the observed outcome. A subsequent session/delivery failure can stop the job without contradicting that message.
- **Outcome-report duplicate at the main caller:** fixed locally through `DiagnosticLogged`; finding 2 identifies a separate remaining duplicate at the final cron boundary. The report's blanket “all four findings addressed” disposition is therefore insufficient as an end-to-end diagnostic verdict.

## Other reviewed requirements

The design remains instructions-led ordinary cron: one generated fixed name, exact requested type/locations in its message, `every_seconds=1800`, and `max_runs=1`. There is no new migration scheduler, typed approval record, or custom schedule action. Matching request terms is the explicitly accepted model/conversation trust boundary; this review does not demand independent binding or stronger authorization.

The lazy durable claim is acquired before calling the billable callback, verifies the saved message, schedule, private origin and run limit, and rejects changed or disabled jobs. The scheduler holds its existing execution lock across the turn. Persisted running work is stopped for review on recovery, including a crash before lazy claim, after claim, during create/handoff, or before completion persistence. Failed completion persistence leaves the conservative running state rather than replaying paid work. A successful durable no-capacity rearm schedules the next ordinary interval without consuming the one terminal run. The snapshot/restart test supports the interrupted paid-claim path. No crash/replay blocker was found in these claim mechanics.

The genuine offline integration uses the real scheduler, `chat.NewSession`, `ReplyScheduled`, and the registered migration handler, with fake model/provider and create callbacks. It verifies that scheduled execution does not invoke the immediate-create operation. Production registers tools before starting the scheduler and calls `CreateOnce` through the scheduled callback. These tests establish session/tool wiring, not live model compliance or live Web/Telegram acceptance.

Only exact `not_created/no_capacity` callback results request migration retry; target-exists and delivered success stop normally. Unknown, lookup failure, partial handoff, report failure, repeated valid calls, and post-tool session/delivery errors stop. Finding 1 limits the claim that *all* subsequent migration-tool errors disarm that retry.

Production supplies the split paired-recipient callback. It sends a separate URL message and an `id_ed25519` document whose content is the generated private-key bytes alone. Pairing is checked before each part. Partial delivery retains created state and stops; no setup/key recovery is claimed for an existing target. The URL/key/token are absent from model-facing results and cron persistence. Diagnostics use bounded allowlisted metadata and safe cause traversal rather than arbitrary remote content. No new secret disclosure was found; finding 2 concerns duplicate logging.

No new production pointer declaration, allocation, or receiver was found. A literal claim of zero pointer additions across tests would be inaccurate: added tests reuse pointer-based APIs, including `&migrationToolTestRegistrar{}` at `internal/chat/migration_tool_test.go:50` and JSON unmarshal address operands. No additional pointer authorization was sought or inferred by this review.

The legacy dedicated migration scheduler remains unstarted by production, and old protected migration records are untouched. Immediate creation remains available. The optional exact-name Web behavior is a contract for a different repository; its implementation cannot be verified here, and exact-name lookup is not represented as an atomic provider deduplication guarantee.

## Validation and review limits

Read `agents.md` first; no root or nested `AGENTS.md` or `CLAUDE.md` was found. Reviewed all 27 modified tracked files and the three existing untracked review/validation documents. Independently ran:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./...` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 -timeout=180s ./internal/migration ./internal/scheduler ./internal/chat ./internal/telegram ./cmd/goaster-agent` — passed.
- `bash packaging/test-skill-assets.sh` — passed.
- `git diff --check` — passed before this report was written.

Passing coverage does not exercise finding 1's rejected-second-call variant or finding 2's complete scheduled diagnostic chain. The prior validation document's intermittent scheduler restart failure was not observed in this run; it remains accurately recorded historical evidence.

Only this final review document was written. No source or tests were edited, no external services or credentials were used, and no real cron, cloud/provider operation, deployment, GitHub action, commit, or push was performed.

## Sol findings disposition — 2026-11-16

**SATISFIED on offline re-review.** The scheduled transport callback now observes every migration-tool rejection before return. Malformed arguments, wrong action, and a cancelled second invocation after exact no-capacity all clear retry and stop for review without a second create. Locally generated invalid, repeated, and claim failures own their diagnostics; the final review conversion does not re-log failures already owned by CreateOnce, Telegram, or outcome reporting. Full scheduler/session diagnostic tests run actual CreateOnce against fake HTTP, including the actual split Telegram handoff document-EOF path, and assert one ERROR and no secret material. The existing split-handoff helper is exposed for this offline transport fixture. The newly added chat registrar test uses a value-based channel registrar.

Active contract, skill, and summary wording now preserves public admission and rate bookkeeping first, followed by read-only exact-name lookup before capacity, migration-record state, key, and billable create work; packaging asserts the skill wording. The Web implementation remains outside this repository and is not verified here.

Full offline `go test -count=1 ./...`, `go vet ./...`, the requested migration/scheduler/chat/Telegram/agent race suite, `bash packaging/test-skill-assets.sh`, and `git diff --check` passed after the fix. The first race run exposed unsynchronized counters in the newly added fake HTTP fixture; atomic counters fixed the fixture and the full focused race suite passed on rerun. No live actions or external services were used.

## Final independent narrow verification — 2026-11-16

**Verdict: SATISFIED.** Independently read the current boundary source and related tests after the fixes; the three original blocking findings above are resolved. Earlier findings, reproductions, and full-review details are retained as historical evidence.

- **Rejected second calls disarm retry:** `executeMigrationTool` routes every rejection through the scheduled transport callback, including parse failures, wrong action, and cancellation. The callback checks `called` first, clears `retry`, and sets review without another create. `TestGenericCronRejectedSecondMigrationCallDisarmsRetry` exercises all three after exact no-capacity through the real scheduler/session/registered handler, checks one create and disabled review state, and advances another interval to verify no replay.
- **One owning ERROR through the actual chain:** `TestScheduledMigrationDiagnosticOwnership` runs the real scheduler, session, registered tool, `CreateOnce`, and outcome wrapper against local fake HTTP. Provider lookup failure produces one `migration.create` ERROR; actual split Telegram handoff document EOF produces one `telegram.api` ERROR. Both stop for review and check diagnostic secrecy. Source confirms Telegram retains the joined EOF cause and marks ownership, CreateOnce honors it, and final cron review conversion emits no duplicate. Outcome reporting likewise honors `DiagnosticLogged`; its marker test and Telegram outcome source confirm the report-failure path without claiming a full-chain outcome-failure fixture.
- **Existing public admission precedes lookup:** the Web contract, migration summary, and bundled skill/AUTH explicitly preserve existing public admission and rate bookkeeping first, then read-only exact-name lookup before capacity, migration-record state, key, or billable create work. This verifies the repository contract; the separate Web implementation remains outside scope.

Quick regression verification found no issue in the simple lazy claim or ordinary cron path. The durable matching claim remains immediately before create; changed/stopped jobs cannot create, and interrupted claims do not replay. Ordinary recurrence runs twice, same-shape busy jobs retry, and ordinary cron management remains available until migration starts. Instructions still generate the fixed name before adding ordinary cron; only explicit `not_created/no_capacity` rearms a migration attempt, while uncertainty and terminal results stop. No additional approval, binding, scheduler, or admission requirement was introduced by this review.

Independent validation: `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 -timeout=180s ./cmd/goaster-agent ./internal/chat ./internal/migration ./internal/telegram ./internal/scheduler` — all five packages passed. `git diff --check` passed. Read root `agents.md` first; no nested AGENTS/CLAUDE case variants were found. Only this conclusion was appended; no source/test edits, live services, real credentials, external systems, GitHub actions, commits, or pushes were used.
