# Telegram `send_file`

GoAster now registers a model-facing `send_file` tool in the production agent. It accepts a path inside the private GoAster artefact workspace and an optional safe basename. It reads one regular file, including binary ZIP files, with an 8 MiB bound and sends it with Telegram `sendDocument` to the transport-bound current chat and forum topic.

The model cannot select a chat, topic, bot token, or arbitrary filesystem path. Symlinks, workspace escapes, directories, FIFOs, other non-regular files, unsafe filenames, and oversized files are rejected. Telegram errors are bounded and redacted; the tool reports success only after one confirmed API response and never retries an uncertain send. This capability does not automatically export memory or change the private-only policy for environment and migration material.

Offline validation plan:

- mocked chat-tool execution covers production registration behavior, binary bytes, private/group/topic binding, missing or terminal context, forged destination fields, file validation, safe feedback, and no replay;
- mocked Telegram transport covers exact multipart recipient/thread fields, non-OK, malformed, HTTP, transport, missing-token, and oversize failures with redacted diagnostics;
- full offline tests, vet, and focused race tests use the repository's installed Go toolchain with network access disabled.

Remaining live acceptance gaps: a deployed paired bot has not been used to send a known ZIP in a private chat, ordinary group, and forum topic, and no live Telegram response or deployment was accessed by this change.

## Recorded offline result, November 18, 2026

PASS using `/usr/local/go/bin/go` (Go 1.25.9) with `GOPROXY=off GOSUMDB=off`:

- `go test ./...`
- `go vet ./...`
- `go test -race ./internal/chat ./internal/telegram ./cmd/goaster-agent ./internal/diagnostics`
- `git diff --check`

Independent Terra review fixed duplicate argument keys, explicitly empty filenames, and missing/invalid Telegram message confirmation; all parent checks above passed afterward. Final read-only Sol review: SATISFIED, no actionable security or correctness blockers. Source inspection compared PicoClaw's `pkg/tools/fs/send_file.go` and its Telegram document/topic path without modifying PicoClaw.

Telegram must return a positive message ID, not merely `ok: true`, before success is reported. Existing environment-download mocks now return valid Telegram Message-shaped results. No automatic uncertain-send retry or broad recovery mechanism was added.

Next live acceptance, separately authorized: deploy the validated GoAster binary and send a harmless ZIP to the paired user's private chat and an authorized group/forum topic. Memory export/import remains unexecuted, and sensitive exports require private delivery rather than a group/topic.
