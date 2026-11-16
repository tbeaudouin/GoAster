# Migration retry validation — 2026-11-16

The public migration tool remains start/status/stop. A stopped attempt with definitive not-created evidence can be followed by a fresh start. Unknown creation and confirmed creation both use the existing explicit owner confirmation for one call as `retry_attempt` naming the exact current prior monitor ID. A confirmed-created result warns that a server already exists; if the prior private handoff succeeded, the owner is referred to its existing setup continuation link and SSH details, while handoff failure is stated honestly and the link/key remain protected. Another server may add charges even if the prior server was manually deleted. A newer uncertain or created attempt requires a new confirmation; an older confirmation is not reused. New attempts retain earlier scheduler, operation, and diagnostic records and use fresh operation IDs and idempotency; an old attempt is not re-enabled.

The scheduler checks retained migration history inside its durable add transaction, including for custom monitor IDs, and active claims block another start. Retained create-dispatch evidence is treated conservatively as unknown unless creation is confirmed, including for status and stop. Missing or invalid history fails closed. A create-dispatch marker can precede the actual request, so a crash in that gap may require confirmation even when no request was sent.

Offline validation used the installed Go 1.25.9 toolchain directly through `/usr/local/go/bin/go`; every command set `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`. No network was used.

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./...` — passed for all packages.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` — passed for all packages.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./internal/migration ./internal/scheduler ./internal/chat` — passed uncached for those three packages.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./internal/migration ./internal/scheduler ./internal/chat` — passed uncached for those three packages.

These tests exercise local fixtures and scheduler state; they do not establish behavior of deployed binaries, real provider creation, paired delivery, or the counterpart GoAsterWeb API. Live compatibility and end-to-end verification with the intended deployed versions and an authorized owner remain gates before operational use. No credentials, cloud services, deployment, runtime operation, or network access were used for this validation.

The focused retry regression scope covers `TestConfirmedCreatedAttemptRequiresExactFreshConfirmation`, `TestCreatedHandoffFailureRequiresConfirmationBeforeFreshAttempt`, and `TestConcurrentConfirmedCreatedRetriesShareOneActiveAttempt`; these exercise successful creation, confirmed creation with failed handoff, exact fresh `retry_attempt` confirmation, stale-confirmation rejection, fresh monitor/operation IDs, retained history, and concurrent exclusion. The existing `TestUnknownRetryRequiresExactCurrentConfirmationAndFreshRecords`, `TestConcurrentConfirmedRetriesShareOneActiveAttempt`, `TestMissingPriorOperationEvidenceCannotAuthorizeRetry`, `TestCorruptPriorDiagnosticCannotAuthorizeRetry`, and `TestDispatchMarkerMakesStatusAndStopUncertain` regressions remain covered, as does `TestContractUnavailableRestartCreateOnce` for old-operation create-once and fresh-attempt behavior.

## Independent acceptance

- Parent independently reran the full suite uncached: `go test -count=1 ./...`, then `go vet ./...`, then `go test -race -count=1 ./internal/migration ./internal/scheduler ./internal/chat`, using the installed toolchain and offline settings above. All passed; `git diff --check` passed.
- Terra reviewed and corrected the implementation; the subsequent final Sol read-only critical review returned SATISFIED, with no blocking correctness, security, billing, or concurrency defects found. Runtime compatibility and real provisioning remain unverified.
- Offline-only tracking for this confirmed-created correction: `pantry-snacks/pantryclub-frontend#346`, assigned to Thomas and verified in org ProjectV2 #2 with Status Done. Earlier retry scope is tracked separately in #344. Neither marks real migration acceptance complete.
