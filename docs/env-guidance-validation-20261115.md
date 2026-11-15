# Environment guidance validation — 2026-11-15

## Scope

This change validates two local behaviors:

- Migration token lookup distinguishes an absent `HCLOUD_TOKEN` from malformed settings, an invalid saved token, or a non-regular settings path. A missing token produces the safe tool result `configuration_required` / `missing_hcloud_token`, with instructions to add it through `/env`. A start that reaches token lookup leaves its monitor stopped for manual review and makes no provider create request.
- Telegram `/get-env` sends the saved environment bytes unchanged. If the saved file does not exist, it sends an empty document named `goaster.env`. A malformed existing file continues to return the unavailable message.

Tests use temporary home, settings, scheduler, and task directories with synthetic credentials. The migration start regression uses injected callbacks and stops before any HTTP request. The full suite's HTTP coverage uses in-process test servers and injected clients; it makes no live provider, Telegram, or other remote service calls. No cloud resources, deployment paths, runtime configuration, GitHub operations, or real secrets were used.

## Checks

All checks used the cached Go 1.25.0 toolchain with `GOPROXY=off` and `GOSUMDB=off`, so they could not download modules or use the checksum service.

- `go test ./internal/chat ./internal/migration ./internal/telegram` — passed
- `go test ./...` — passed
- `go vet ./...` — passed
