# OpenAI relogin offline validation — 2026-11-25

Validation used Go 1.25.0, `GOPROXY=off`, and a fresh temporary `HOME` created with `mktemp -d /tmp/goaster-relogin-validation-XXXXXX`; `GOPATH=/root/go` supplied the existing dependency cache. Retained-upload tests used that isolated HOME. No real authentication, model calls, deployment, cloud, GitHub, or issue operations were performed.

Final commands and outcomes:

- `go test ./...` — passed.
- `go vet ./...` — passed.
- `go build ./...` — passed.
- `go test -race ./internal/chat ./internal/telegram ./cmd/goaster-agent -run 'Test(Relogin|CredentialWriterCrossProcessLock|OpenAIAuthenticatorLogin|Login|PrivateOwnerLogin|StopCancelsPrivateOwnerLogin)' -count=1` — passed for all three packages.
- `git diff --check` — passed.

Offline regressions exercise login while credential lookup proceeds, stale 401 handling after replacement, verification URL and code validation, credential writer serialization across processes, and existing control endpoint and Telegram cancellation and authorization tests. The first full run exposed an altered expired-credential diagnostic; the original diagnostic was restored before the final passing run.
