# Cron create handoff: first review

Date: 2026-11-16

## Review scope

I read the repository guidance in agents.md; no nested AGENTS.md,
agents.md, CLAUDE.md, or case variant was present in the repo root or
nested source directories. I reviewed the uncommitted ordinary-cron,
migration-tool, Web-contract, Telegram handoff, scheduler, documentation, and
test changes. The review stayed in this worktree and used only offline fakes.

The target design is deliberately narrow: the existing generic cron store and
claim loop carry a normal chat message; an eligible message can cause one
exact-name migration create through the already registered tool. There is no
custom migration scheduler, migration-specific scheduler record, new schedule
tool, immutable typed job, live provider, or automatic replay of an uncertain
paid action.

## Findings and corrections

### Startup registration race — fixed

The scheduler was started before the production migration tool was registered.
A persisted due job could therefore enter ReplyScheduled before
goaster_migration existed, consume its one run, and fail closed without the
intended callback. Production serve now registers the cron and migration
tools before starting the generic scheduler. A source-order regression test
guards this boundary.

### Missing real session/provider path — fixed

The existing generic cron session stub verified delivery but did not establish
the scheduled-turn context or provider tool boundary. The new offline harness
uses the real scheduler, chat.NewSession, ReplyScheduled, a fake
HistoryProvider/tool registrar, and a fake model callback that invokes the
registered migration handler. It also supplies a fake immediate create
operation and asserts that the scheduled path never uses it.

### Retry uncertainty after model/tool failure — fixed

Retry is now an exact callback result, not a broad session-success default. A
second migration-tool call clears retry and returns already_attempted. Any
session/provider, timeout, delivery, or model error after the first call is
wrapped as scheduler review-required, so a model that swallows or surfaces a
tool failure cannot rearm a possibly uncertain turn. Unknown, partial,
lookup-failure, outcome-reporting-failure, and other non-capacity migration
results stop; uncertain or incomplete outcomes are durably held for review.

### Split handoff and owner change — retained and covered

Production one-shot creation uses separate URL-message and key-only attachment
delivery. The paired owner is checked before the URL and again before the key;
the second part is not sent after an owner change. Handoff failures preserve
confirmed creation and never trigger automatic recreation. Diagnostics retain
wrapped causes while excluding secrets, prompts, tool output, request bodies,
setup URLs, and credentials.

No additional blocker was found in the final review. Historical migration
monitor code and records remain in the repository for compatibility, but the
production entry point does not start that scheduler and does not mutate old
records.

## Outcome matrix

| Result or boundary | Scheduler outcome | Automatic create replay |
| --- | --- | --- |
| not_created + no_capacity | enabled, next ordinary 1800-second run | allowed for the same exact request |
| target_exists | completed and disabled | no |
| created + delivered handoff | completed and disabled | no |
| unknown, partial handoff, lookup/reporting failure, other result | failed, disabled, review required | no |
| second tool call | failed, disabled, review required | no |
| session/provider/timeout/delivery error | failed, disabled, review required | no |
| persisted running claim after crash/restart | failed, disabled, review required | no until explicit re-enable |

## Evidence

The exact commands and the real-session test names, trust boundaries, and
offline limitations are recorded in
docs/cron-create-handoff-validation-20261116.md. The final offline suite,
vet, focused race runs, skill packaging test, and git diff --check all passed
on 2026-11-16. No real model/network/provider/Telegram/cloud/deploy/cron
acceptance or commit/push was performed.
