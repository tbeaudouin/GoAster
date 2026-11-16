# Migration retry validation — 2026-11-16

The public migration tool remains start/status/stop. A stopped attempt with definitive not-created evidence can be followed by a fresh start. Unknown creation requires an explicit owner confirmation with a duplicate-server and extra-charge warning, passed for one call as `retry_attempt` naming the current uncertain attempt. A newer uncertain attempt requires a new confirmation; an older risk accepted for a subsequent attempt does not require confirmation again. A confirmed creation blocks further attempts. New attempts retain earlier scheduler, operation, and diagnostic records and use fresh operation IDs and idempotency; an old attempt is not re-enabled.

The scheduler checks retained migration history inside its durable add transaction, including for custom monitor IDs, and active claims block another start. Retained create-dispatch evidence is treated conservatively as unknown unless creation is confirmed, including for status and stop. Missing or invalid history fails closed. A create-dispatch marker can precede the actual request, so a crash in that gap may require confirmation even when no request was sent.

Offline validation used the already installed Go 1.25.0 toolchain directly, with `PATH=/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.0.linux-amd64/bin:$PATH` and `GOTOOLCHAIN=local`. The default Go launcher initially could not verify its toolchain with the checksum database disabled; no network was used.

- `GOPROXY=off GOSUMDB=off go test ./...` — passed for all packages (Go's normal test cache applies).
- `GOPROXY=off GOSUMDB=off go vet ./...` — passed for all packages.
- `GOPROXY=off GOSUMDB=off go test -race -count=1 ./internal/migration ./internal/scheduler ./internal/chat` — passed uncached for those three packages.
- `GOPROXY=off GOSUMDB=off go test -count=1 ./internal/migration ./internal/scheduler ./internal/chat` — passed uncached for those three packages.

These tests exercise local fixtures and scheduler state; they do not establish behavior of deployed binaries, real provider creation, paired delivery, or the counterpart GoAsterWeb API. Live compatibility and end-to-end verification with the intended deployed versions and an authorized owner remain gates before operational use. No credentials, cloud services, deployment, runtime operation, or network access were used for this validation.

## Independent acceptance

- Parent independently reran the full suite uncached: `go test -count=1 ./...`, then `go vet ./...`, then `go test -race -count=1 ./internal/migration ./internal/scheduler ./internal/chat`, using the installed toolchain and offline settings above. All passed; `git diff --check` passed.
- Terra reviewed and corrected the implementation; the subsequent final Sol read-only critical review returned SATISFIED, with no blocking correctness, security, billing, or concurrency defects found. Runtime compatibility and real provisioning remain unverified.
- Offline-only tracking: `pantry-snacks/pantryclub-frontend#344`, assigned to Thomas and verified in org ProjectV2 #2 with Status Done. This does not mark real migration acceptance complete.
