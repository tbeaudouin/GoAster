# send_file 50 MB source validation

The tool and independent Telegram transport accept up to 50,000,000 bytes. Generic file-tool limits remain 8 MiB. Oversize tool feedback advises a user-chosen transfer service or, if no suitable service or skill exists, creating a skill. Offline tests cover files above 8 MiB, the exact boundary, rejection before delivery, safe diagnostics, and transport size validation without public Telegram calls.

With `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` and `/usr/local/go/bin/go`, the following passed on this source change:

- `go test ./internal/chat ./internal/telegram -run 'TestSendFile|TestTelegramSendFile' -count=1`
- `go test ./... -count=1`
- `go vet ./...`
- `go build ./...`

The initial invocation using `go` on PATH failed before tests because it is Go 1.22.2 and the module requires Go 1.25. No deployment, restart, live Telegram delivery, or rollout verification was performed; those remain outside this source-only validation.
