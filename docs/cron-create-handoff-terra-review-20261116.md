# Cron create handoff: penultimate review

Date: 2026-11-16

Verdict: changes requested. The production scheduler wiring regresses ordinary cron, and a scheduled migration can receive a false retry notice.

## Findings

1. **Recurring ordinary cron jobs stop after their first successful run.** `cmd/goaster-agent/generic_cron.go:89` now claims every job, including ordinary recurring jobs. The claim sets `Enabled = false` and clears `NextRun` in `internal/scheduler/scheduler.go:954`; after a successful execution, `internal/scheduler/scheduler.go:1362` treats that claim-owned disabled state as a user-disabled job. Reproduce by adding an ordinary `every_seconds` job without `max_runs`, allowing one successful run, and inspecting it: it is disabled with no next run. The production lifecycle test at `cmd/goaster-agent/generic_cron_test.go:94` uses `max_runs: 1`, so it cannot catch this. Restore ordinary recurring scheduling after a successful claim while preserving explicit concurrent disable and crash protection; add a production-wired recurring-job test.

2. **Scheduled turns can no longer manage ordinary cron jobs, and some ordinary jobs get migration-specific failure handling.** `internal/chat/cron_tool.go:123` rejects every scheduled `add`, `update`, `remove`, enable, or disable operation, regardless of whether the job is a migration. Existing ordinary jobs that manage cron through their scheduled instructions now fail. Separately, `cmd/goaster-agent/generic_cron.go:116` and `:192` classify every private 1800-second, `max_runs: 1` job as migration, even if it never calls the migration tool; a transient `ErrSessionBusy` on such an ordinary job is marked review-required at `:145` rather than retried. Reproduce with an ordinary scheduled cron-management task, or a private 1800-second one-run reminder while its conversation is busy. Preserve ordinary cron behavior without relying on schedule shape as proof that a migration was attempted; apply restrictions and terminal handling only where needed for the authorized migration workflow.

3. **The owner can be told a retry will happen when the job actually stops.** `cmd/goaster-agent/main.go:335` reports the result immediately inside the migration callback; `internal/telegram/migration.go:239` says a no-capacity request will be retried in 30 minutes. The session can subsequently fail, make a second tool call, time out, or fail to enqueue its reply; `cmd/goaster-agent/generic_cron.go:144` and `:165` then stop the job for review instead of rearming it. Reproduce with explicit `not_created/no_capacity` followed by a provider/model error, as in `TestGenericCronRealSessionMigrationErrorAfterNoCapacityRequiresReview`, using the production outcome reporter: the owner receives the retry promise despite the stopped job. Report the retry only after successful durable rearm, or phrase the early notice without promising a retry and send an accurate final status.

4. **Outcome delivery failures produce duplicate diagnostics.** `internal/telegram/migration.go:214` returns a `deliveryError` after `sendMessage` has logged its failure, and `:220` logs preflight failures before returning the same marker. `cmd/goaster-agent/main.go:338` unconditionally logs that error again. Reproduce by making the scheduled outcome Telegram request fail or the paired recipient unavailable: two ERROR records are emitted for the same failure. Honor the existing `DiagnosticLogged` marker at the caller, preserving the original cause at the owning boundary.

## Review and validation

Reviewed the uncommitted source, documentation and tests, including production scheduler/session/tool registration, claim and crash recovery, owner checks, split handoff, and the optional exact-name Web contract. The real-session tests exercise `chat.NewSession` and `ReplyScheduled` with an offline fake provider and migration callback; they are useful integration coverage but do not exercise the live Web or Telegram services. The model following visible cron instructions is the explicitly approved trust boundary; no additional approval binding is requested here. No new production pointer declarations were identified in the diff.

Offline `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./...` passed, as did `git diff --check` before this report was written. The passing tests do not cover the recurring-job regression above. No live service, credentials, cloud, deployment, GitHub, or cron daemon was used.

## Disposition

All four findings addressed: the durable claim is acquired lazily inside the
once-only migration callback, scheduled cron management remains available until
a migration starts, no-capacity notices do not promise a retry, and the outcome
report boundary honors DiagnosticLogged. Regression coverage and current offline
validation are recorded in cron-create-handoff-validation-20261116.md.
