# Environment guidance validation — 2026-11-15

## Scope

This change validates two local behaviors:

- Migration token lookup distinguishes an absent `HCLOUD_TOKEN` from malformed settings, an invalid saved token, or a non-regular settings path. A missing token produces the safe tool result `configuration_required` / `missing_hcloud_token`, with instructions to add it through `/env`. A new monitor whose first execution reaches missing-token lookup is stopped for manual review. The guidance makes no claim about prior monitor activity. Settings read failures fail closed even when a process token is present.
- Telegram `/get-env` sends the saved environment bytes unchanged. If the saved file does not exist, it sends an empty document named `goaster.env`. A malformed existing file or a failure after the initial file check returns the unavailable message.

Tests use temporary home, settings, scheduler, and task directories with synthetic credentials. The migration start regression uses injected callbacks and stops before any HTTP request. The full suite's HTTP coverage uses in-process test servers and injected clients; it makes no live provider, Telegram, or other remote service calls. No cloud resources, deployment paths, runtime configuration, GitHub operations, or real secrets were used.

## Checks

All checks used the cached Go 1.25.0 toolchain with `GOPROXY=off` and `GOSUMDB=off`, so they could not download modules or use the checksum service.

- `go test ./internal/chat ./internal/migration ./internal/telegram` — passed
- `go test ./...` — passed
- `go vet ./...` — passed

## Independent review

The missing-token response no longer claims that no provider create request was sent, because a previous monitor may have acted. The missing-file handling is confined to the initial file check, so later read errors fail closed. A regression also checks that malformed saved settings cannot be bypassed with a process token.

The independent run used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./...` and `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...`. The default Go launcher first failed before compilation because offline checksum verification of its toolchain was unavailable; the installed toolchain was then invoked directly.
