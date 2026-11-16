# Cron create handoff: design and validation

## Scope and trust boundaries

The migration skill remains instructions-led. It generates one random
goaster-migration-[0-9a-f]{16} name, puts that exact name plus the approved
type and locations in an ordinary cron message, and requests
every_seconds=1800 with max_runs=1. There is no migration-specific
scheduler, schedule action, immutable typed job, persisted approval record, or
model-supplied owner/destination. The generic scheduler stores the message
and transport-captured private origin; the message is visible guidance, not a
cryptographic binding of the tool arguments.

At execution, the production scheduler calls the real chat.Session.ReplyScheduled.
The registered goaster_migration tool receives an optional callback in a private,
threadless, 1800-second, one-run context. The callback claims and rechecks the
saved job immediately before the paid create, and permits one create attempt.
Ordinary turns do not acquire a durable execution claim and retain cron management.
After a migration call starts, cron mutations in that turn are rejected. A second migration-tool call returns already_attempted, clears any
retry decision, and is durably stopped. Only the exact result
creation_state=not_created plus code=no_capacity returns the scheduler
retry signal. target_exists and delivered success complete normally;
unknown, partial/incomplete handoff, lookup failure, reporting failure, and
other non-capacity outcomes stop with durable review. Session/provider,
delivery, timeout, and post-tool errors also take precedence over retry.

The Web counterpart is outside this repository. Its contract requires the
public admission and rate bookkeeping first, then the same exact name to be
looked up read-only before capacity, migration-record state, key, or billable
create work; an
existing target is not adopted and does not imply setup or private-key
recovery. The Telegram handoff sends the setup URL and key-only id_ed25519
attachment separately, revalidating the paired owner before each part.
Secrets, setup URLs, private keys, request bodies, and credentials are not
returned to the model, persisted in the cron job, or emitted in diagnostics.

## Offline evidence

All checks below were run on 2026-11-16 with Go 1.25.9 from
/usr/local/go/bin/go, GOTOOLCHAIN=local, GOPROXY=off, and GOSUMDB=off:

- GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./... — passed.
- GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./... — passed.
- GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 -timeout=180s ./internal/migration ./internal/scheduler ./internal/chat ./internal/telegram ./cmd/goaster-agent — passed.
- GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./cmd/goaster-agent -run 'TestGenericCronRealSessionMigration|TestGenericCronProductionLifecycle|TestGenericCronShutdown' — passed.
- bash packaging/test-skill-assets.sh — passed.
- git diff --check — passed.

The real-session integration coverage is in
cmd/goaster-agent/generic_cron_test.go:

- repeated identical requests retry only for explicit no capacity;
- target exists, delivered success, unknown, partial handoff, and lookup
  failure do not replay, with uncertain/incomplete results held for review;
- a repeated migration-tool call cannot create twice or rearm the job;
- a model/provider error after no capacity overrides retry;
- a session timeout stops without invoking the create callback;
- the registered tool is reached through a real chat.NewSession and
  ReplyScheduled, while immediate operations.Create is asserted unused;
- existing ordinary-cron lifecycle, delivery, and scheduler interrupted-claim
  tests remain passing.

No live model, provider, Web API, Telegram API, cloud account, deployment,
cron daemon, credentials, or GitHub operation was used. The Web exact-name
precheck and live split delivery remain contract-level/offline-test evidence,
not production acceptance.

## Terra review disposition

The callback now claims lazily and checks the actual saved job, preserving ordinary
recurring jobs and busy retries. No-capacity notices state only the observed
outcome; later model or delivery failures cannot make them promise a retry.
Outcome reporting honors diagnostic ownership. Offline regression tests cover
two recurring executions, cron management, busy retries, stale or disabled
claims, interrupted paid claims, and the neutral no-capacity notice.

The post-fix full offline test and vet runs, focused cron race run, packaging
check, and broad migration/scheduler/chat/Telegram/agent race rerun passed. The
first broad race run failed in `TestSchedulerLoopWakesAndExecutesDueJob`: its
immediate restart reported `cron scheduler is already running`. The same test
passed in the subsequent broad race run; the intermittent failure remains an
observed limitation of that validation run.

## Final Sol disposition validation

The three final Sol findings were addressed and re-reviewed offline. Real-session
rejected-second-call cases cover malformed arguments, wrong action, and a
cancelled context after no capacity. Full scheduled diagnostic-chain tests use
CreateOnce with fake HTTP and the actual split Telegram document-EOF fixture;
each checks one owning ERROR and absence of secrets. Public admission and rate
bookkeeping precede the read-only exact-name lookup in the contract and skills.
The initial focused race run found a race in the new HTTP fixture counters; after
using atomic counters, the full offline test, vet, focused race, and packaging
checks passed. This supplements rather than erases the historical failure above.
