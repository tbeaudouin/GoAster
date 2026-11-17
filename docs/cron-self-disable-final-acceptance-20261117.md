# Final cron self-disable acceptance — 2026-11-17

**SATISFIED.** No concrete introduced blocking finding remains in the reviewed scope. The executor-lifetime finding in the prior final review is resolved by the Terra follow-up. This is acceptance of the current local change within its documented design, not deployment approval or certification of live behavior.

## Exact reviewed scope

Reviewed the working-tree diff against HEAD `5d9356e74d9b7f6326a591f33afd74bb780d1ca0`, with no staged changes. The reviewed tracked diff contains these 21 files:

- `README.md`
- `cmd/goaster-agent/generic_cron.go`
- `cmd/goaster-agent/generic_cron_test.go`
- `cmd/goaster-agent/main.go`
- `docs/api-first-migration-20261116.md`
- `docs/cron-create-handoff-validation-20261116.md`
- `docs/generic-cron-validation-20261115.md`
- `docs/goaster-migration.md`
- `internal/chat/cron_tool.go`
- `internal/chat/cron_tool_test.go`
- `internal/chat/openai.go`
- `internal/chat/provider.go`
- `internal/chat/tools.go`
- `internal/scheduler/retry_regression_test.go`
- `internal/scheduler/scheduler.go`
- `packaging/test-skill-assets.sh`
- `skills/goaster-migration/AUTH.md`
- `skills/goaster-migration/META.md`
- `skills/goaster-migration/SKILL.md`
- `skills/hetzner-auth/AUTH.md`
- `skills/hetzner-auth/SKILL.md`

SHA-256 of `git diff --binary HEAD --` for those paths in the order above: `8697d585fc79b4f24073841e5d0a2c1ae2959c61bc329aeac5a626e08dbc5991`.

Also read `agents.md`, the untracked Terra review, validation record, prior final review, and especially `docs/cron-self-disable-terra-followup-20261117.md`. Repository-local discovery found no additional agents.md or CLAUDE.md/CLAUDE.local.md variants. Relevant unchanged claim, execution, recovery, session, origin, and durable-lock paths were inspected. This acceptance document is the only file written by this review.

## Acceptance reasoning

- **Lifetime and same-ID reuse:** `DisableOwnExecution` obtains the private owner and checks scheduler identity and origin validity. Its shared transaction helper rechecks that exact owner's `done` channel after acquiring both `s.mu` and the durable state lock, before changing the job. Executor completion closes that channel under `s.mu`. Revocation and the successful save are therefore serialized; a retained old owner cannot disable a later execution of the same ID. Running status alone is no longer the lifetime check.
- **Controlled regression:** `TestSelfDisableRejectsOwnerPausedUntilLaterRun` captures the owner after the initial liveness check, pauses before entering the same transaction helper used by the public method, completes the first run, starts a later run of the same ID, then resumes. It requires `ErrExecutionClaimUnavailable` and checks that the later job stays enabled. This deliberately tests the authorization interleaving through the helper; it does not instrument a blocked public-method call or depend on race-detector success as proof.
- **Lock ordering:** self-disable takes mutex then durable state lock and never takes `owner.state`. Scheduled claiming takes `owner.state` before the mutex. Completion releases the mutex after closing `done` and before receiving from `owner.state`, avoiding the corresponding lock inversion. No introduced cycle was found with the existing separate run/state locks.
- **Durable self-disable wins retry:** the transaction persists `SelfDisabled=true`, `Enabled=false`, and `NextRun=nil`, preserving running status and the execution claim. Finalization reloads this marker and suppresses retry rearming for both ordinary and claimed runs. The marker is cleared as part of finalization without enabling the job. Interrupted-run recovery still disables and requires review. Tests inspect persistence during execution, ordinary/claimed `ErrRetry`, reload and later due runs; the generic-cron regression covers exact no-capacity followed by self-disable.
- **Genuine scheduled-only exposure:** production definitions and dispatch use `definitionsFor(ctx)` and `handlerFor(name, ctx)`. Both require the scheduled-turn marker plus the runtime-injected ID matching a live private scheduler owner. The handler repeats the check, accepts only an empty JSON object, and delegates target selection to the scheduler, which checks persisted chat/type/topic atomically. ID injection alone does not authorize self-disable; stale contexts and supplied target IDs are rejected.
- **Existing workflow boundaries:** migration-capable scheduled turns reject cron add before and after create; ordinary scheduled management retains its existing behavior. Only the exact top-level `not_created/no_capacity` result takes the quiet capacity retry path. Attempted-create session failures and uncertain outcomes retain review handling. The four-step workflow, literal retry instructions, public rough-price guidance, and private handoff guidance remain aligned with the documented scope.

Approval, matching create arguments to approved literals, one-approval/one-job admission, and migration capability admission by the existing private/threadless/1800-second/one-run job shape remain documented pre-existing instruction-led design limits. Legacy ordinary jobs of that shape can still qualify. No new structured approval state, job marker, or custom scheduler is required by this acceptance, and those limits are not reclassified as introduced blockers.

## Checks and limitations

This review independently ran only the read-only `git diff --check`, which passed. The controlled regression and other tests were inspected, not executed. Terra's follow-up reports successful offline full tests, vet, focused race tests, packaging checks, and diff checks; those results are attributed to that record and were not independently rerun here.

All work stayed in the requested repository. No code edits, commits, GitHub, network, cloud, secrets, runtime state, deployed process, real cron, provider, or Telegram access occurred. No tests or packaging scripts were executed by this reviewer. Live integration, installed-skill rollout, public endpoint/pricing freshness, and actual provider or Telegram delivery remain unverified. Acceptance applies to the exact reviewed local snapshot and static concurrency reasoning above.
