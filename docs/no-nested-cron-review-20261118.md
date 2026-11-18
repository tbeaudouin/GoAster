# No-nested-cron review — 2026-11-18

**SATISFIED** — no blocking findings in the current diff for the requested cron tool boundary.

- The production scheduler supplies a live private execution owner to `executeAgentCronJob`; that path injects the matching job ID and calls `ReplyScheduled`, which adds the scheduled-turn marker. Provider requests use `definitionsFor(ctx)`, so both ordinary and migration scheduled turns receive the cron action enum without `add`. Normal requests retain `add`.
- Provider dispatch uses `handlerFor` and the registered cron handler reaches `executeCronTool`. Its normalized action check rejects forged `add` whenever `scheduler.IsScheduledExecution(ctx)` is true, before `cronToolAdd` calls `jobs.Add`. The scheduled migration gate remains in place. Repository search found no other production cron creation command path.
- Schema selection uses the stricter `genuineScheduledCronContext` (marker plus matching injected ID plus live owner); the command guard needs only the live owner. The production path supplies all three. If future executor plumbing omits the marker or ID, the schema would expose `add` even though the handler still rejects it; keep these checks aligned when changing that plumbing.
- The scheduled schema retains list, get, update, remove, enable and disable. The existing scheduled-only self-disable authorization and migration restrictions are unchanged. No unnecessary scheduler redesign was found.

Review was static, including context propagation, provider schema and dispatch, and creation call sites. A focused offline `go test ./internal/chat -run 'TestCron|TestScheduledCron' -count=1` was attempted but did not run: Go attempted toolchain verification with the checksum database disabled. The separate validation document reports earlier passing tests; those results were not independently reproduced here.
