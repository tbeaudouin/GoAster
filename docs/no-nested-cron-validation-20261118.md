# No-nested-cron validation — 2026-11-18

The scheduled `cron` schema omits `add` while retaining list/get/update/remove/enable/disable. The handler rejects `add` when the scheduler reports a live execution owner, including direct handler dispatch. The existing migration-only restrictions, conversation ownership filtering, persistence, and scheduled self-disable path remain unchanged. Ordinary conversation schemas and handlers retain `add`.

Review context: scheduled authorization is based on the scheduler's private live execution context, not a model-supplied value. The live runtime was not upgraded; no deployment or external provider/network changes were made.

Validation:

- `git diff --check` — pass
- `go test ./...` — pass
- `go vet ./...` — pass
- `go test -race ./internal/chat -run 'TestCron|TestScheduledCron'` — pass
- Packaging checks — not run; no packaging files changed

No commit was created and nothing was pushed.

## Independent parent acceptance, November 18, 2026

Using `/usr/local/go/bin/go` (Go 1.25.9), `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

- `go test -count=1 ./...`: PASS, all packages, TMPDIR unset.
- `go vet ./...`: PASS.
- `go test -race -count=1 ./internal/chat ./internal/scheduler`: PASS.
- `TMPDIR=/root/tmp sh packaging/test-skill-assets.sh`: PASS.
- `git diff --check`: PASS.
- Independent Terra then Sol reviews: SATISFIED for the production cron schema/handler boundary.

An initial attempt used the system Go 1.22.2 with automatic toolchains disabled and stopped before tests. A second attempt placed test temporary files under `/root/tmp`; the existing privilege-switch fixture could not access that root-only path. The final full run used the installed Go 1.25.9 and the default system test scratch directory and passed. Neither failure required a source change.

This prevents nested creation through the GoAster cron tool and command handler. It is not an operating-system shell sandbox. No real cron was created, and no running agent, installed skills, cloud resource, or deployment was changed.
