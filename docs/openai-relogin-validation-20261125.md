# OpenAI relogin offline validation — 2026-11-25

Validation used Go 1.25.0, `GOPROXY=off`, and a fresh temporary `HOME` created with `mktemp -d /tmp/goaster-relogin-validation-XXXXXX`; `GOPATH=/root/go` supplied the existing dependency cache. Retained-upload tests used that isolated HOME. No real authentication, model calls, deployment, cloud, GitHub, or issue operations were performed.

Final commands and outcomes:

- `go test ./...` — passed.
- `go vet ./...` — passed.
- `go build ./...` — passed.
- `go test -race ./internal/chat ./internal/telegram ./cmd/goaster-agent -run 'Test(Relogin|CredentialWriterCrossProcessLock|OpenAIAuthenticatorLogin|Login|PrivateOwnerLogin|StopCancelsPrivateOwnerLogin)' -count=1` — passed for all three packages.
- `git diff --check` — passed.

Offline regressions exercise login while credential lookup proceeds, stale 401 handling after replacement, verification URL and code validation, credential writer serialization across processes, and existing control endpoint and Telegram cancellation and authorization tests. The first full run exposed an altered expired-credential diagnostic; the original diagnostic was restored before the final passing run.

Final review correction: cancellation, failure, expiry, and uncertain outcomes release the adapter attempt slot when its handler exits. There is no automatic replay. A fresh, explicit bare `/login` from the paired owner in a private chat may request a new authorization; the agent's single-active lock can return busy until the previous canceled handler exits. Cancellation or a lost response can leave the prior outcome unknown. Existing credentials are not deleted, and a new attempt replaces them only after a new browser authorization completes. A terminal completion delivery failure is logged safely after the credential commit and does not report that committed authentication failed.

Correction validation: `GOPROXY=off go test -race ./internal/chat ./internal/telegram ./cmd/goaster-agent -run 'Test(Relogin|CredentialWriterCrossProcessLock|OpenAIAuthenticatorLogin|OpenAIAuthenticatorDeviceLogin|Login|PrivateOwnerLogin|StopCancelsPrivateOwnerLogin)' -count=1` passed. Offline tests cover `/stop` followed by an explicit new `/login`, an unknown result with no automatic retry and a separately requested new attempt, and redacted terminal completion-write failure after commit.

These are offline checks only: a real browser/device authorization and actual installed-service behavior remain unverified. The corrected build has not been installed or rolled out.

For this correction, `GOPROXY=off go test ./...` initially failed `TestAgentAttachmentAndEnvelopeSizeLimits` because the existing HOME had reached its retained-upload quota. Repeating `HOME="$test_home" GOPATH=/root/go GOPROXY=off go test ./...` with `test_home=$(mktemp -d /tmp/goaster-relogin-review-XXXXXX)` passed all packages. `git diff --check` passed.

Portability correction validation: `GOPROXY=off go test ./internal/chat ./internal/telegram ./cmd/goaster-agent -run 'Test(Relogin|CredentialWriterCrossProcessLock|OpenAIAuthenticatorLogin|OpenAIAuthenticatorDeviceLogin|Login|PrivateOwnerLogin|StopCancelsPrivateOwnerLogin)' -count=1` passed for all three packages; `GOOS=windows GOARCH=amd64 GOPROXY=off go build ./...` passed; and `GOPROXY=off go build ./...` passed. `CredentialStore.withLock` now uses `goaster/internal/filesync.LockFile` and `IsLockBusy`; the deferred lock-file close releases the OS lock. No live authentication, network, deployment, cloud, GitHub, or issue operations were performed.
