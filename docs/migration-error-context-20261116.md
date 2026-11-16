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
corrupt history, and ambiguous history fail closed. No provider or paid call,
automatic retry, new state store, secret exposure, or create path is involved.

Focused offline regressions cover HTTP 422 diagnostics/log metadata, malformed
envelopes, shared metadata redaction, nested ID recovery, chat status errors,
omitted-ID recovery, and cross-owner rejection.

Validation commands and results:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/diagnostics ./internal/migration ./internal/chat -count=1` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./internal/diagnostics ./internal/migration ./internal/chat ./internal/scheduler ./internal/telegram ./cmd/goaster-agent ./cmd/goaster-telegram` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 180s` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./internal/diagnostics ./internal/migration ./internal/chat` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` — passed.
- `bash packaging/test-skill-assets.sh` — passed offline.
- `git diff --check` — passed.

No network, credentials, production systems, deployment, or GitHub writes were
used. Changes are committed locally only.
