# Migration cleanup validation — 2026-11-17

## Current behavior

`goaster_migration` exposes only `action: "create"`, with required exact
`server_name` and `server_type` fields and optional strict `locations`. The
immediate and waiting paths use the same one-shot create request. Waiting uses
ordinary generic cron at 1800 seconds with `max_runs: 1`, one claimed create
call per turn, and rearming only for `creation_state: "not_created"` with
`code: "no_capacity"`.

Public catalogue discovery and rough pricing are informational normal
search/fetch work. The protected provider token is used for the create request;
public admission, exact-name lookup, capacity, provider sequencing, and
architecture/image validation remain Web-owned. The paired private split
handoff, recipient revalidation, secret redaction, fail-closed credential
loading, generic cron claim/replay guards, installer allowlist, and existing
scheduler persistence remain active.

## Cleanup and safety

Dead migration-specific lifecycle runtime and scheduler wiring, obsolete public
request/result fields and quote/resource helpers, the combined handoff path,
and lifecycle-only tests and review documents were removed. Generic scheduler
storage was not changed. No persisted records were deleted, and this runtime
does not read, migrate, or resume files from older installations.

## Validation

All commands passed offline:

```sh
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go build ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent ./internal/scheduler ./internal/diagnostics -count=1 -timeout 300s
sh packaging/test-skill-assets.sh
git diff --check
```

The race and package checks use local fakes only. No provider, Telegram, Web,
SSH, deployment, cloud, production, or GitHub operation was performed.
