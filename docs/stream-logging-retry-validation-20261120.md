# Stream logging and retry validation — 2026-11-20

Validation used `PATH=/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64/bin:$PATH GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`.

- `go test ./... -count=1 -timeout=90s`: PASS.
- `go vet ./...`: PASS.
- `git diff --check`: PASS.

Offline tests cover three attempts per individual model request, including five consecutive requests each succeeding on its third attempt and four tool actions executed once. The separate one-time 401 refresh retains its own bounded request attempts. Tests also cover malformed bodies with transient read errors, incomplete streams, lifecycle and unknown SSE events, terminal errors followed by final/tool events, diagnostic redaction, cyclic errors, and complete responses followed by a read failure. The latter is returned without replay and emits an ERROR diagnostic.

No live provider, OAuth, Telegram, credentials, or external network was used. Protocol compatibility is validated against local fixtures, not live provider traffic.

## Final critical review

Verdict: **SATISFIED** after narrow fixes. The full-repository test/vet results above are the inherited parent validation; the final edits were revalidated in all four affected packages below.

Confirmed defects and fixes:

- Request references now accept only `chat-` plus 24 lowercase hexadecimal digits, or the deliberate 16-digit fixture format. Arbitrary words, wrong lengths, nonhex characters, and control injection are rejected. This validates shape, not provenance.
- Tool progress above 64 is retained. Counts remain nonnegative machine integers with bounded decimal output; legitimate long tool loops no longer lose their counts or appear to have started no tools.
- Unrecognized provider type/code values remain non-retryable after redaction. They cannot become absent fields and authorize a transient stream retry.
- HTTP retry history retains the local request reference when a later attempt succeeds.
- Every Telegram `error` event is terminal, including legacy or unrecognized error payloads. Unrecognized details produce a fixed safe message; later final/tool events cannot replace it.
- Completed responses with trailing read failures still return once without replay. Their ERROR diagnostics retain available model, phase, HTTP status, and upstream request ID.

Each defect was reproduced with a failing focused regression before its fix. Direct tests cover reference validation/log redaction, stream boundary messages and preserved causes, unknown provider fields/messages, progress at 65/64, successful retry correlation, terminal legacy/unrecognized errors, Telegram reference redaction, and trailing-read context. No new pointers or recovery paths were introduced by these fixes.

Final checks, with the offline environment at the top of this document:

- `go test ./internal/diagnostics ./internal/chat ./cmd/goaster-agent ./internal/telegram -count=1 -timeout=90s`: PASS (all four packages).
- `go vet ./internal/diagnostics ./internal/chat ./cmd/goaster-agent ./internal/telegram`: PASS.
- `go test -race ./internal/diagnostics ./internal/chat ./cmd/goaster-agent ./internal/telegram -run 'TestRemoteMetadataAllowlist|TestRequestFailureDiagnosticIncludesUnderlyingErrorAndContext|TestCodex.*(Retry|Retries|Premature|Incomplete|Lifecycle|Malformed|Completed)|TestAgent.*(Stream|Reference)|TestTelegram(PreservesSafeModelStreamClassificationAndReference|TerminalErrorStopsLaterFinalAndToolEvent)' -count=1 -timeout=90s`: PASS (all four packages).
- `git diff --check`: PASS.

Remaining rollout gap: live provider lifecycle variants, real Retry-After behavior, OAuth refresh, and deployed agent/Telegram correlation have not been exercised. No live/cloud calls, real credential access, commits, or pushes were performed during this review. Tests used temporary credential fixtures and local HTTP servers/transports.

## Independent final acceptance, November 20, 2026

After the final review fixes, the parent independently reran the complete offline suite on the final source tree:

- `go test ./... -count=1 -timeout=120s`: PASS across all packages.
- `go vet ./...`: PASS.
- `go test -race ./internal/chat ./internal/telegram ./internal/diagnostics ./cmd/goaster-agent -count=1 -timeout=180s`: PASS across all four affected packages.
- `git diff --check`: PASS.
- Final critical review: SATISFIED after verified fixes. Earlier test failures, the cyclic error inspection stall, and the erroneous cross-tool dispatch cap were corrected and the final tests rerun.

Environment: cached Go 1.25.0 toolchain with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`. These tests use local fixtures, not real provider requests. The three-attempt limit is per individual model request; the existing single credential-refresh path remains separate. No whole-task or completed-tool replay was introduced. Running-agent upgrade, restart, live provider reproduction and real Telegram delivery remain unverified and were not performed.
