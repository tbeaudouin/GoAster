# Cron self-disable validation — 2026-11-17

Implemented the runtime-bound `cron_disable_self` capability and aligned the
bundled migration workflow with the four-step private journey. The capability
accepts only `{}`, is exposed only in a live scheduler-backed scheduled turn,
and disables the internally bound job after checking the persisted
conversation/topic origin. It does not accept a target ID or alter another
job. The existing claim, recurring rearm, `max_runs: 1`, interruption review,
and durable state paths remain in use.

Changed files:

- `internal/scheduler/scheduler.go`
- `internal/chat/cron_tool.go`
- `internal/chat/tools.go`
- `internal/chat/openai.go`
- `internal/chat/provider.go`
- `cmd/goaster-agent/generic_cron.go`
- `cmd/goaster-agent/main.go`
- `internal/chat/cron_tool_test.go`
- `cmd/goaster-agent/generic_cron_test.go`
- `skills/goaster-migration/SKILL.md`
- `skills/goaster-migration/AUTH.md`
- `skills/goaster-migration/META.md`
- `skills/hetzner-auth/SKILL.md`
- `skills/hetzner-auth/AUTH.md`
- `README.md`
- `docs/goaster-migration.md`
- `docs/api-first-migration-20261116.md`
- `docs/generic-cron-validation-20261115.md`
- `docs/cron-create-handoff-validation-20261116.md`
- `packaging/test-skill-assets.sh`
- `docs/cron-self-disable-validation-20261117.md`

Reviewed without edits: `skills/goaster-env/SKILL.md`; its protected
environment and missing-token guidance already matched this workflow.

Checks passed:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./...`
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...`
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/scheduler ./internal/chat ./cmd/goaster-agent -count=1`
- `packaging/test-skill-assets.sh`
- `git diff --check`

Focused tests cover normal-turn hiding/rejection, runtime ID injection and
anti-spoofing, conversation/topic ownership, self-disable after migration
create begins, stale-context rejection, self-only persistence/restart, quiet
same-job no-capacity retries, terminal/uncertain/crash no-replay behavior, and
the existing private handoff path.

Gaps: validation is offline and uses local scheduler, model, HTTP, Telegram,
and delivery fakes. It does not contact a provider, Telegram, Web, cloud,
deployment, network service, or real cron, and no GitHub operation or test
issue was created.

## Review follow-up and scope

A successful self-disable is persisted separately from the temporary execution
claim, and takes precedence over retry finalization for claimed migrations and
ordinary scheduled jobs. Interrupted runs retain the existing review behavior.
Migration-capable scheduled turns cannot add jobs before or after a create
attempt, preventing recursive scheduling through the cron tool. Ordinary
scheduled turns retain their existing cron management capabilities; this guard
uses the existing migration capability context and does not classify every
scheduled turn as a migration.

Findings 2 and 4 describe pre-existing, intentionally instruction-led approval
and design, and legacy admission based on the existing job shape. Approval,
matching requested values to approved values, and the one-approval/one-job
workflow remain instruction-led rather than structured durable guarantees.
The source of migration capability preexists this follow-up: private recurring
1800-second, one-run jobs are admitted by the existing shape. Compatibility
with those legacy jobs is preserved, including possible admission of ordinary
jobs with that shape. No structured approval state, custom scheduler, or
migration workflow marker was introduced. Review this follow-up for regressions
against the requested scope, rather than treating those existing design choices
as new guarantees.

Regression checks include a persisted self-disable during execution, retry
finalization and restart for both ordinary and claimed runs, a no-capacity
migration followed by self-disable, and cron add before and after migration
create alongside ordinary scheduled management. The offline checks listed
above were rerun for this follow-up.
