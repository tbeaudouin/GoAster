# API-first migration final acceptance — 2026-11-16

**SATISFIED — the sole remaining blocker in the [final re-review](api-first-migration-final-rereview-20261116.md) is closed. No remaining concrete blocker was found in the final relevant diff.**

Read `agents.md`, the only agents/CLAUDE variant found inside this repository, the prior review and re-review, Terra's follow-ups, and the Web contract. This acceptance focuses on the final Telegram delivery fix and its interaction with `CreateOnce` and shared diagnostics; it retains the prior full review's otherwise accepted findings. All commands ran from the requested repository root. Only this report was added; application code, tests, and prior reports were not edited. No external service, GitHub, real credentials, or production system was accessed. HTTP regression tests used local synthetic servers.

The fix in `internal/telegram/adapter.go` classifies an HTTP transport EOF from multipart delivery as unexpected by joining `io.ErrUnexpectedEOF` with the original error, except when cancellation is present. It preserves the original HTTP error chain rather than replacing it. `sendMigrationHandoffTo` supplies allowlisted method, handoff stage, confirmed creation state, and failure code through the multipart boundary. Telegram emits the diagnostic, and its cause-preserving `deliveryError` ownership marker lets `CreateOnce` avoid a duplicate.

The uncached `TestMigrationTransportEOFThroughCreateOnce` passed. It actually runs `CreateOnce`, receives a successful create response from a local HTTP server, uploads the private handoff through the real Telegram multipart path, and closes that HTTP connection before returning a response. Its assertions establish:

- Exactly one create request and one document upload; the result remains `creation_state=created`, `handoff=failed`, `code=handoff_failed`.
- The returned delivery error retains both original `io.EOF` and classified `io.ErrUnexpectedEOF` through `errors.Is`.
- Exactly one bounded ERROR, owned by `telegram.api`, with `method=sendDocument`, `stage=handoff`, `creation_state=created`, `code=handoff_failure`, and the unexpected EOF cause. There is no duplicate caller diagnostic.
- The diagnostic excludes the synthetic Telegram token, provider token, private-key marker, and server URL. Source inspection confirms the logger traverses the retained cause chain using safe type/fixed labels and validated metadata without copying arbitrary error messages, request bodies, handoff contents, or credential-bearing URLs.

The cancellation-with-EOF test also passed without a diagnostic or unexpected-EOF classification. Shared normal EOF and expected cancellation suppression remain intact. Inspection of the final relevant diff covered metadata forwarding on multipart failure branches, the safe delivery wrapper, the caller ownership guard, diagnostics filtering, and other document-upload callers. No concrete new regression was established.

Independent validation used `/usr/local/go/bin/go` **1.25.9**, with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

| Check | Result |
| --- | --- |
| `go test -count=1 -v ./internal/telegram -run 'Test(MigrationTransportEOFThroughCreateOnce\|MultipartCancellationWithEOFRemainsExpected\|MigrationDeliveryPreservesLoggedCause)$'` | Passed |
| Same Telegram selection with `-race -count=1 -timeout=180s` | Passed |
| `go test -count=1 -v ./internal/diagnostics ./internal/migration -run 'Test(ErrorCapturesCauseChainAndSafeContext\|ErrorRedactsCredentialsPrivateContentAndTerminalInjection\|ErrorSkipsExpectedCancellationAndEOF\|MigrationDiagnosticMetadataAllowlist\|OneShotBoundaries\|OneShotRemoteFieldsRedacted)$'` | Passed |
| `git diff --check` | Passed |

These are focused acceptance checks; the prior reports retain their broader suite, vet, and packaging results. The accepted design remains API-first one-shot creation without migration records or automatic create retries. Ordinary cron remains notification-only, with no automatic eventual creation; that disclosed gap is unchanged and is not a new blocker. Offline acceptance does not verify the separate live Web implementation, live Telegram delivery, or deployed user-owned skills.

Prior reports remain unchanged. SHA-256 of `api-first-migration-final-rereview-20261116.md`: `07d3a247282adf8a7f00e819a15b9ff783d4923af8d69e8fb93b5e0a5db8f631`; of `api-first-migration-final-review-20261116.md`: `2f06781f87678781003e56b8b372fc8efa8a5ae80f62ed8e833bb4545f773483`.
