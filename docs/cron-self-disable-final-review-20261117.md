# Final cron self-disable review — 2026-11-17

**Verdict: changes requested — one introduced medium concurrency finding remains.**
The prior findings 1 and 3 are resolved within the approved migration scope.
Self-disable is self-only and durable, and finalization honors it even when
no-capacity returns `ErrRetry`. Executor lifetime enforcement has the race below.
No runtime code was changed by this review.

## Finding

1. **Medium — Self-disable authorization can survive its executor through an
   in-flight call.** `internal/scheduler/scheduler.go:1065` checks the execution
   owner's `done` channel before acquiring `s.mu` and the durable state lock
   (`:1072–1078`). After those waits, `:1083` checks only running status and
   origin; it does not revalidate the live executor or its execution instance.
   `executeClaim` closes `owner.done` at `:1363` without synchronizing that
   closure with this mutation. A concurrent call retaining the execution
   context can pass the live check, pause, let the executor return, then resume
   and persist self-disable while finalization is pending. If delayed until a
   later execution of the same recurring job, running status and origin again
   match, allowing the old invocation to disable that later execution. No
   other job ID is needed for this race.

   Serialize lifetime revocation with the mutation authorization, ensuring a
   call cannot commit against an expired or different execution instance.
   Add a controlled concurrency regression covering a call paused after the
   initial check, executor completion, and a subsequent run of the same ID.
   A check made only before lock acquisition does not establish this guarantee.
   This is a static interleaving finding, not a reproduced exploit. It requires
   a concurrent retained-context call; the current production provider invokes
   tools synchronously. The existing stale-context assertion
   (`internal/chat/cron_tool_test.go:692`) starts its call after completion and
   does not exercise this interleaving. Race-detector success alone would not
   exclude this logical authorization race.

## Confirmed behavior and prior follow-up

- The runtime supplies the job ID in `executeAgentCronJob`; ID injection alone
  cannot authorize a call. Scheduled tool definitions and dispatch both check
  genuine scheduled context, and the handler repeats the check. Arguments must
  be an empty object. The scheduler selects the target from its private owner,
  verifies the scheduler instance, and checks persisted chat/type/topic in the
  mutation transaction. No introduced path was found for selecting another ID
  or disabling another origin through `cron_disable_self`. Calls first made
  after executor completion are rejected; the in-flight exception is above.
- Migration-capable scheduled turns reject `cron add` before and after create.
  Ordinary scheduled management retains its existing behavior. This resolves
  prior finding 1 for the approved scope without broadening the restriction to
  every scheduled job.
- `SelfDisabled` is persisted alongside `Enabled=false` and `NextRun=nil`,
  without clearing running status or the execution claim prematurely.
  Finalization reads that persisted marker and prevents retry rearming for
  both claimed migrations and ordinary runs. Recovery of interrupted runs
  remains disabled and review-required. Prior finding 3 is resolved; no
  introduced path was found for finalization to reverse successful self-disable.
- Only the exact top-level `not_created/no_capacity` pair takes the quiet
  migration retry path: scheduled outcome reporting and queued model replies
  are skipped, and the existing recurring job is reused. Session failures after
  an attempted create still take the review path. Terminal results, repeated
  create calls, uncertain outcomes, and interrupted claims retain their existing
  no-automatic-replay behavior. Existing session-busy retries remain distinct
  from provider capacity results.
- The bundled workflow has four steps, one approval for immediate create and
  capacity-only retries, literal approved retry fields, and split private
  handoff. Minor wording corrections now explicitly allow token-safe read-only
  direct provider API availability checks through shell and require checking
  latest official public rough pricing before approval, best effort with an
  explicit unknown result if unavailable. Pricing stays informational with no
  target/cap/billing expansion or authenticated API pricing requirement.

## Scope, limits, and checks

Prior Terra findings 2 and 4 are pre-existing approved design limits, not new
regressions or outstanding demands from this review: approval, literal approved
values, and one-approval/one-job admission remain instruction-led. Private,
threadless 1800-second recurring jobs with `max_runs: 1` still acquire migration
capability by their existing shape, including ordinary legacy jobs with that
shape. This review does not certify structured approval enforcement or require
a new approval system, workflow marker, or custom scheduler.

Read `agents.md`, the Terra review, validation follow-up, current diff, and
relevant execution/claim/recovery/dispatch paths. No CLAUDE variants were found
inside this repository. All review work stayed in the requested repository;
packaging temporary files were also kept there and removed.

This reviewer reran `packaging/test-skill-assets.sh` and `git diff --check` after
wording edits; both passed. The parent independently reports full offline tests,
vet, focused race tests, and packaging passed with `/usr/local/go/bin/go`, as
recorded in `docs/cron-self-disable-validation-20261117.md`. Those Go checks were
not rerun here, and the lifetime race has no new executable reproduction.

No network, GitHub, cloud, secrets, deployed runtime, or real cron was accessed;
no commits were made. Public endpoint/pricing freshness and live provider or
Telegram behavior were not independently verified. The pricing requirement was
reviewed as workflow text, not exercised as a migration approval request.
