# Migration error context validation — 2026-11-16

The migration failure logger now retains only allowlisted stage, diagnostic
code, creation state, and valid local monitor ID alongside the HTTP/provider
metadata already retained. The shared logger validates those fields again, so
malformed values, response bodies, messages, secrets, and private content are
not emitted.

Valid v1 HTTP 422 envelopes retain their safe classification; malformed
envelopes fall back to status-only classification. Status-result normalization
recovers a valid top-level monitor ID into the nested migration result and its
diagnostic, including the status-error path used by the chat tool.

Status may omit `job_id`. The local authenticated runtime resolves the latest
validated retained attempt for the current protected paired recipient using
existing scheduler/history records. Missing pairing, another recipient,
corrupt history, and ambiguous history fail closed. Recovery ordering uses
only the nonzero retained creation timestamp; when the two newest valid
attempts share that timestamp, recovery stops and never uses a monitor ID as a
tie-break. A valid `history.Failure` monitor ID is retained in an omitted-ID
status error's top-level result, nested diagnostic, and bounded log only when
the validated failure attempt belongs to the current recipient. Different,
missing, invalid, or unavailable owners produce a generic persisted-state
failure without monitor or server details; invalid IDs are dropped. No provider or paid call, automatic retry, new state store,
secret exposure, or create path is involved.

Focused offline regressions cover HTTP 422 diagnostics/log metadata, malformed
envelopes, shared metadata redaction, nested ID recovery, chat status errors,
omitted-ID recovery, cross-owner rejection, equal-newest-timestamp fail-closed
recovery, and history-error monitor-ID retention in the status result/log.

Validation commands and results:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/diagnostics ./internal/migration ./internal/chat -count=1` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./internal/diagnostics ./internal/migration ./internal/chat ./internal/scheduler ./internal/telegram ./cmd/goaster-agent ./cmd/goaster-telegram` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 180s` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./internal/diagnostics ./internal/migration ./internal/chat` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/migration ./internal/chat -count=1 -run 'Test(EqualNewestMigrationAttemptsFailClosedWithoutIDTieBreak|StatusHistoryFailureRetainsValidMonitorIDInResultAndLog|StatusRecoversCurrentMonitorWithoutUserID|StatusWithoutIDDoesNotCrossPairedOwner|MigrationToolStatusErrorUsesNormalizedMonitorID)'` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./internal/migration ./internal/chat ./internal/scheduler` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./internal/migration ./internal/chat ./internal/scheduler` — passed.
- `bash packaging/test-skill-assets.sh` — passed offline.
- `git diff --check` — passed.

No network, credentials, production systems, deployment, or GitHub writes were
used. Changes are committed locally only.

Review follow-up validation (offline):

- `go test ./internal/migration -run TestStatusHistoryFailureOwnership -count=1` — passed, covering same-owner retention, cross-owner corruption, missing and invalid current owners, lookup errors, and malformed history without a verified owner.
- `go test ./... -count=1 -timeout 180s` — passed.
- `go test -race -count=1 ./internal/migration ./internal/chat ./internal/scheduler` — passed.
- `go vet ./...` — passed.
- `git diff --check` — passed.

Go commands used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`.
