# Migration cleanup final review — 2026-11-17

Verdict: **SATISFIED** for the current source flow, with the live gaps below.
Independently read `agents.md`, `CONTRIBUTING.md`, the Terra review, and the
working-tree cleanup against HEAD. No blocking source regression remains.

## Current-flow findings

- Production registration supplies immediate and scheduled `CreateOnce`.
  The tool accepts only create, requires type and exact lowercase hexadecimal
  name, rejects unknown arguments, and obtains private owner identity from
  transport context. Request validation precedes credential and HTTP effects.
  The wire contains only provider token, exact name/type, optional strict
  locations, and public key; the fresh owner secret is the bearer credential.
  `TestOneShotOffline` checks wire fields, unchanged locations, matching keys,
  separate handoff, and private-material exclusion from the result.
- Credential loading fails closed on unreadable/malformed protected settings,
  including when a process token exists. This review restored that explicit
  fallback-bypass assertion in
  `TestLoadHetznerTokenFailsClosedForMissingMalformedAndUnreadableSettings`.
- Found and fixed an active diagnostic omission: local create failure codes
  were absent from the logging allowlist, and missing/invalid/unreadable
  credential causes appeared only as generic error types. Added five fixed
  local codes and three exact credential descriptions to bounded allowlists;
  arbitrary error text remains excluded. Before the fix,
  `TestOneShotCredentialFailureDiagnostics` failed for all three credential
  cases. It now verifies one ERROR with code, distinct safe cause, retained
  permission-denied cause, and no HTTP or delivery. No retry, recovery,
  storage, or approval mechanism was added.
- `TestOneShotRejectsAmbiguousResponses` now exercises the active create path:
  valid no-capacity survives; duplicate top-level fields, unknown fields,
  case aliases, trailing JSON, oversized bodies, and mismatched existing
  target names produce unknown/unusable with no handoff. Synthetic secret
  values remain absent from results and diagnostics.
  `TestOneShotRemoteFieldsRedacted`, `TestOneShotBoundaries`, and shared
  diagnostics tests additionally cover remote-field redaction, lost response,
  wrong origin/secret/name, and diagnostic ownership.
- Generic cron checks the unchanged saved job and persists its execution claim
  before create. A private, threadless 1800-second job with `max_runs: 1`
  permits one call per turn. Only `not_created` plus `no_capacity` rearms.
  `TestGenericCronRealSessionMigrationRepeatsOnlyNoCapacity` verifies repeated
  exact request terms; `TestGenericCronRealSessionMigrationTerminalResultsDoNotReplay`
  covers success, existing target, uncertainty, partial delivery and lookup
  failure. Second/rejected calls, session errors, timeout, changed/stopped
  jobs, and crash claims have dedicated tests in the same file.
  `TestGenericCronMigrationCrashClaimDoesNotReplay` and scheduler durable-claim
  tests verify stopped review after an interrupted claim. Corrected the
  generic-cron document: delivered success and existing target complete
  normally; they do not require review.
- Split Telegram delivery revalidates the paired owner before the URL and
  again before the key-only `id_ed25519` attachment. Recipient-change tests
  reject delivery before either part; the transport EOF test confirms one
  create, one URL send, one failed key upload, preserved EOF causes, one safe
  diagnostic, and no recreation.
- Cleanup searches found no surviving implementation/reference of removed
  lifecycle APIs. An exported function/type scan from six removed non-test
  Go files searched 30 names: only the intentional startup absence assertion
  and unrelated standard-library `strconv.Quote` calls matched. Broader route,
  field, adapter and handoff-name searches matched only absence/forbidden
  assertions. All 25 deleted Markdown filenames have zero surviving filename
  references. Packaging checked local links and the installed skill tree.
  Surviving migration documents describe current behavior; historical test
  runs are not used as evidence for this verdict.

## Checks performed here

Local toolchain: `go version go1.25.9 linux/amd64`. All Go commands below used
`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go`.

| Command suffix / command | Result |
| --- | --- |
| `test ./internal/migration -run TestOneShotCredentialFailureDiagnostics -count=1` | Failed before diagnostic fix, as described above |
| `test ./internal/migration ./internal/diagnostics -count=1` | Passed after diagnostic fix |
| `test ./... -count=1 -timeout 300s` | Passed with all final Go changes |
| `vet ./...` | Passed |
| `build ./...` | Passed |
| `test -race ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent ./internal/scheduler ./internal/diagnostics -count=1 -timeout 300s` | Passed |
| `sh packaging/test-skill-assets.sh` | Passed, including local Markdown targets |
| `git diff --check` | Passed |

## Explicit live gaps

No external network, real credential, provider, Web, Telegram, SSH, deployment,
commit or publication operation was performed; tests use synthetic values,
injected clients and local HTTP servers. The separate Web implementation and
its admission, exact-name lookup ordering, capacity and architecture/image
validation were not verified here. No live provisioning, browser setup or
end-to-end delivery was exercised. Existing binaries, installed user-owned
skills and older installation files were not updated or validated. Approval
and reuse of approved name/type/locations remain instruction-led, without an
independently persisted approval binding. Repository caller review does not
establish compatibility for external consumers of removed exported APIs.
