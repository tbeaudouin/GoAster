# Baseline test maintenance, November 23, 2026

Repaired five tests without changing runtime implementation:

- Memory propagation and installation now check the selected, nonempty starter content instead of obsolete phrases. Existing history, discovery, file-permission, and user-memory preservation checks remain.
- Planning checks cover the project-specific directory, same-task reuse, plan-only authorization boundary, and already-approved coordinated workflow.
- Feature inspiration explicitly says to copy no code. Its private `/env` check tolerates Markdown backticks and still requires no secrets in chat.
- GitHub authentication guidance uses focused policy checks instead of outdated whole-document snapshots. Coverage includes the fixed login command, account verification, private session and returned session ID, key restrictions, no secret input, user browser verification, session cleanup, and single-approval setup. The separate exact login-command configuration test remains unchanged.

## Validation

The implementation helper and parent independently passed these offline commands using `GOPROXY=off GOSUMDB=off /usr/local/go/bin/go`:

- `go test ./... -count=1`, with no test exclusions.
- `go vet ./...`.
- `go build ./...`.
- `go test -race ./cmd/goaster-agent ./internal/artefacts ./internal/command -count=1`.

The five focused tests, formatting, and `git diff --check` also passed. Final review identified missing browser-verification and session-cleanup assertions; these were added before the final full-suite run.

These checks do not exercise a live agent, real GitHub login, or deployed migration cron. No deployed runtime or existing cron configuration was changed.
