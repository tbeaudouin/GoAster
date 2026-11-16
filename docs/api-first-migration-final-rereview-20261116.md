# API-first migration final re-review — 2026-11-16

**BLOCKED — two prior blockers are closed; the diagnostics blocker is only partially closed. One concrete Telegram delivery regression remains.**

Read `agents.md` (the only agents/CLAUDE variant found inside this repository), the [prior blocked report](api-first-migration-final-review-20261116.md), the [Terra review and follow-up](api-first-migration-terra-review-20261116.md), and the [Web contract](api-first-web-contract-20261116.md). Reviewed the whole current tracked diff against HEAD `1298ea2`, all six pre-existing untracked files, and the relevant runtime, shared diagnostics, Telegram, skill-loading/installation, and packaging dependencies. Review commands ran from the specified repository root. Only this report was added; no application code or tests were edited. No external network, GitHub, real credentials, or production services were used. HTTP tests use local synthetic servers.

| Prior blocker | Re-review disposition |
| --- | --- |
| 1. Active legacy guidance | Closed. Bundled META/AUTH/SKILL and README consistently describe one-shot private creation, fresh conversational approval after a duplicate-charge warning, ephemeral handoff, and read-only ordinary cron. README explains that existing user-owned installed skills are not overwritten and require owner reconciliation. The loader's concatenated AUTH/SKILL guidance is now consistent for new installations. |
| 2. Runtime diagnostics | Partially closed. Create transport EOF now joins `io.ErrUnexpectedEOF` with the original cause, producing one ERROR with `creation_state=unknown` and `code=response_unavailable`. Service errors now log allowlisted HTTP status, code, stage, creation state, and provider code. Ordinary Telegram failures preserve their underlying cause and avoid duplicate records. However, the new ownership wrapper suppresses the caller's diagnostic even when Telegram's logger silently skipped an unexpected HTTP EOF; see below. |
| 3. Package asset validation | Closed. The offline package check passes and validates source and installed META/AUTH/SKILL separately, including owner privacy, one-shot approval, duplicate warning/fresh approval, no secret persistence, no scheduled creation, and rejection of retired guidance. Existing allowlist and Markdown-link validation remain. |

**Remaining P2 release blocker: lost Telegram upload response produces zero ERROR diagnostics after confirmed creation.**

The concrete trigger is a successful create followed by Telegram closing the `sendDocument` HTTP connection before returning a response, without user cancellation. The returned transport error wraps `io.EOF`.

1. `callMultipart` at `internal/telegram/adapter.go:1165–1167` passes that cause through `fmt.Errorf("Telegram transport: %w", err)` to `telegramFailure`.
2. `telegramFailure` at `adapter.go:167` calls `diagnostics.Error`, but `internal/diagnostics/diagnostics.go:42` and `261–264` recursively unwrap it to EOF and emit nothing. This is failed document delivery, not normal end-of-input.
3. The changed `internal/telegram/migration.go:114–115` unconditionally returns `deliveryError`, whose `DiagnosticLogged()` marker at line 98 asserts diagnostic ownership regardless of whether a record was emitted.
4. The new caller guard at `internal/migration/oneshot.go:212–214` sees that marker and skips its ERROR. `CreateOnce` returns a normal tool result with `creation_state=created`, `handoff=failed`, and `code=handoff_failed`; there is no returned Go error for an outer boundary to log.

Consequently the complete active path emits **zero** ERROR records for this failure. In the prior snapshot, the generic delivery error caused the caller to emit a record; removing that fallback without classifying Telegram's HTTP EOF creates this regression. The created state and no-retry behavior remain correct, but the explicit `agents.md` runtime logging requirement is unmet.

Required closure: classify this unexpected HTTP EOF at the owning delivery boundary while retaining the wrapped cause and producing exactly one bounded, safe ERROR. Preserve normal EOF and expected cancellation exceptions. Add offline coverage of the actual Telegram delivery EOF path and its interaction with the caller's ownership guard. The existing Telegram regression uses `errors.New("private-transport-detail")`, not EOF; the one-shot ownership test uses a synthetic pre-marked error. Neither exercises this combination.

This finding is established by the source path above and the existing component tests; no new test was injected or code changed to claim an end-to-end reproduction.

No further concrete critical issue or requirements drift was established. Production `serve` registers `CreateOnce` without starting the retired migration scheduler. The active tool requires interactive private context and a positive owner ID; pairing is checked before dispatch and delivery. The request still matches the one-shot Web contract, forwards strict optional locations, generates matching ephemeral Ed25519 material and a fresh bearer secret, and has no automatic create retry or persistence. Confirmed creation survives handoff failures, uncertain responses remain unknown, reflected request credentials are scrubbed, and setup origin/fragment checks protect private delivery. Shared provider guidance retains external-state checks for other workflows and makes account inspection optional for migration. Notification-only cron and lack of automatic eventual creation remain the accepted conservative scope; no persisted approval or retry-history gate is newly required.

Validation used the installed `/usr/local/go/bin/go` 1.25.9 with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

| Check | Result |
| --- | --- |
| `go test ./...` | Passed |
| `go vet ./...` | Passed |
| `go test -race -count=1 -timeout=180s ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent` | Passed |
| Uncached `TestOneShotBoundaries` and `TestOneShotRemoteFieldsRedacted` | Passed, including create EOF diagnostic/context assertions |
| Uncached `TestMigrationDeliveryPreservesLoggedCause`, `TestErrorSkipsExpectedCancellationAndEOF`, and `TestMigrationDiagnosticMetadataAllowlist` | Passed; these confirm ordinary delivery ownership/cause preservation and the shared EOF suppression policy |
| `TMPDIR="$PWD" sh packaging/test-skill-assets.sh` | Passed |
| `git diff --check` | Passed |

The prior blocked report remains unchanged, SHA-256 `2f06781f87678781003e56b8b372fc8efa8a5ae80f62ed8e833bb4545f773483`. These offline checks do not verify the separate live Web implementation, Telegram delivery, or deployed user-owned skills. **Final disposition: BLOCKED pending the single delivery-diagnostic fix above.**
