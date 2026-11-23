# Cron validation and diagnostics

Offline reproduction found that an add with `every_seconds` and `at_seconds: 0` was rejected as two schedules. Adds treat zero durations, null schedule fields and the empty cron string as unused placeholders; negative durations and multiple real schedules are rejected. Updates retain explicit values. Pure scheduler validation checks schedule syntax and duration bounds before mutation, without creating a job.

The parser rejects malformed, duplicate, unknown and wrongly typed fields with fixed messages while retaining decoder causes. Definite pre-mutation validation failures allow a model correction; remaining calls in the batch are skipped. Scheduler mutation failures retain their causes and stop the turn. Diagnostics use fixed action, stage and reason categories and exclude argument values. Scope and self-disable checks remain enforced.

Offline checks (Go at `/usr/local/go/bin/go`):

- `/usr/local/go/bin/go test ./internal/chat ./internal/scheduler`
- `/usr/local/go/bin/go test ./internal/chat -run 'TestCron|TestHandler' -count=1`
- `/usr/local/go/bin/go test -race ./internal/chat -run 'TestCron|TestHandler' -count=1`
- `/usr/local/go/bin/go test -race ./internal/scheduler -count=1`
- `/usr/local/go/bin/go vet ./...`
- `/usr/local/go/bin/go build ./...`

The fake-provider tests exercise correction, skipped batch calls, and fatal scheduler failures with preserved causes and redacted diagnostics. No live scheduler, provider, migration, Telegram or cloud service was contacted. The full suite was not run; the five reported baseline failures have not been independently verified here. Scheduler validation dependent on current persisted job state remains at the scheduler boundary and stops the turn if it fails.

Final-review follow-up: scheduler validation causes are retained for invalid cron fields and oversized durations on add and update, while the outward message and diagnostics remain fixed and no mutation is invoked. The following checks passed offline:

- `GOPROXY=off /usr/local/go/bin/go test ./internal/chat ./internal/scheduler`
- `GOPROXY=off /usr/local/go/bin/go test -race ./internal/chat -run 'TestCron|TestHandler' -count=1`
- `GOPROXY=off /usr/local/go/bin/go test -race ./internal/scheduler -count=1`
- `GOPROXY=off /usr/local/go/bin/go vet ./...`
- `GOPROXY=off /usr/local/go/bin/go build ./...`

Final acceptance: Terra review followed by final Sol re-review returned SATISFIED after fixing schedule-validation cause retention. Parent independently reran `go test ./internal/chat ./internal/scheduler ./internal/diagnostics ./internal/telegram`, `go vet ./...`, `go build ./...`, and `git diff --check` on the final code; all passed. The screenshot's exact historical arguments and deployed revision were not inspected, so the reproduced placeholder mismatch is a confirmed source bug, not proof of that incident's exact cause. Source-only delivery: no running-agent upgrade, live retry job, migration or server creation.
