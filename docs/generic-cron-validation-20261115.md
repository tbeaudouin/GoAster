# Generic cron validation - 2026-11-15

## Scope

The generic cron tool runs durable scheduled chat turns with transport-captured chat and topic identity. Management is scoped to the origin, and replies are persisted in a private outbox for authenticated Telegram delivery and acknowledgement. Conversation sessions retain the existing per-chat persistence identity; deliveries retain the originating topic. Delivery is at least once: a crash or failure after sending and before acknowledgement can repeat a reply, including earlier chunks of a partially sent reply.

Migration monitoring retains its separate scheduler and store. The `goaster_migration` actions are `estimate`, `quote`, `availability`, `start`, `status`, and `stop`. Scheduled turns cannot invoke `start`, which requires an interactive private conversation and the existing approval workflow. Both scheduler executor forms receive the execution claim context.

## Offline validation

Commands executed from the repository root:

- `GOPROXY=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s`
- `GOPROXY=off /usr/local/go/bin/go vet ./...`
- `GOPROXY=off /usr/local/go/bin/go test -race ./cmd/goaster-agent ./internal/chat ./internal/scheduler ./internal/telegram -count=1 -timeout 300s`

Coverage includes execution claims, scheduled migration safeguards, origin isolation and spoof rejection, scheduler and outbox persistence, interrupted shutdown review, and Telegram topic delivery and acknowledgement. These are offline tests; no live provider, Telegram, cloud, deployment, or scheduling acceptance was performed.

## Independent review

- Terra then Sol final review: SATISFIED after execution-claim and scheduled-migration permission fixes.
- Independently repeated full suite, vet and focused race suite passed.
- Sol also observed an intermittent existing scheduler Stop/restart ordering failure in a race run; its scheduler rerun passed. The unchanged Stop implementation clears started after closing done. This pre-existing flake remains a follow-up, not a claim of universal race-test stability.
