# Migration capacity-retry validation — 2026-11-16

This change addresses finding 1 only in the GoAster source repository. It does
not inspect or modify the separately coordinated GoAsterWeb review for finding
2.

The existing pre-create monitor keeps its persisted three-check availability
budget. A valid successful response with structurally valid `locations: []`
still means no capacity, keeps the 30-minute monitor enabled, and resets the
budget. Malformed provider location data is an invalid response and stops the
monitor; it is never treated as empty capacity.

HTTP availability retries require a decoded, exact v1 error envelope whose
actual status matches the envelope status and whose fields form an explicit
transient combination:

- `provider_timeout` at `stage=provider` with no `provider_code`, for HTTP
  500/502/503/504;
- `unavailable` with `resource_unavailable` at `stage=provider`, or with no
  provider code at `stage=service`, for HTTP 500/502/503/504; and
- `rate_limited` with `rate_limit_exceeded` at `stage=provider`, or with no
  provider code at `stage=service`, for HTTP 429.

Provider `provider_dns_failure`, `provider_tls_failure`, and
`provider_transport_failure` envelopes stop on the first check, even when the
envelope and gateway status are otherwise valid: those categories do not
establish a temporary cause. Direct GoAsterWeb HTTP-client transport failures
remain retryable when they establish a timeout (`context.DeadlineExceeded` or
`Timeout()`) or temporary network error (`Temporary()`).

Provider `unauthorized`, `forbidden`, and `invalid_input` causes; storage or
create stages; integrity/invalid responses; unknown codes; malformed or
unknown bodies; and plain status-only gateway 502/503/504 responses stop
conservatively. No create request is automatically retried, and existing
bounded logs/privacy and retained diagnostic budget behavior remain in place.

## Changed files

- `internal/migration/runtime.go` — explicit trusted-envelope retry gate and
  existing persisted budget/rearm/reset path.
- `internal/migration/diagnostic.go` — persisted retry state and safe bounded
  diagnostic support already used by the monitor.
- `internal/migration/capacity_retry_regression_test.go` — offline regressions
  for trusted envelope decoding/classification, conflicting status/stage/code/
  provider causes, first-check termination of DNS/TLS/unknown provider
  transport failures with zero retries and create calls, malformed locations,
  budget persistence and reset, stop races, and retained creation evidence.
- `skills/goaster-migration/SKILL.md` — source-scheduler policy and terminal
  classification documentation.
- `docs/migration-capacity-retries-validation-20261116.md` — this record.

## Offline results

All Go commands used the installed local toolchain with module and network
access disabled. `git diff --check` ran locally:

```text
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -timeout=120s -count=1 ./internal/migration -run 'TestAvailability'
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -timeout=120s -count=1 ./internal/migration
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -timeout=120s -count=1 ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./internal/migration ./internal/scheduler ./internal/chat
git diff --check
```

Results:

- focused `TestAvailability` regressions — passed;
- `go test ./internal/migration` — passed;
- `go test ./...` — passed for all packages;
- `go vet ./...` — passed;
- migration/scheduler/chat race suite — passed; and
- `git diff --check` — passed after the final documentation update.

The focused capacity regressions are
`TestAvailabilityRetryBudgetPersistsAcrossRestartAndResetsOnSuccess`,
`TestAvailabilityHTTPEnvelopeClassificationRequiresConsistentTransientEnvelope`,
`TestAvailabilityTerminalFailuresStopWithoutRetry`,
`TestMalformedAvailabilityLocationsStopWithoutRetry`,
`TestAvailabilityIntegrityFailureStopsWithoutRetry`,
`TestAvailabilityRetryStopRaceKeepsTruthfulPendingState`, and
`TestAvailabilityDoesNotRetryAfterDispatchOrUnknownEvidence`.

The final-review correction was verified offline with:

```text
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -timeout=120s -count=1 ./internal/migration -run 'TestAvailabilityHTTPEnvelopeClassificationRequiresConsistentTransientEnvelope|TestAvailabilityTerminalFailuresStopWithoutRetry'
GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -timeout=120s -count=1 ./internal/migration ./internal/scheduler
git diff --check
```

Both Go test commands and `git diff --check` passed after the final-review
correction.

No provider, cloud, live Redis, Telegram, deployment, GitHub, credentials,
secrets, commits, or other repository was accessed.

## Parent final acceptance, November 16, 2026

- Independently passed both repositories: `GOPROXY=off GOSUMDB=off GOTOOLCHAIN=local /usr/local/go/bin/go test -count=1 -timeout=5m ./...`, `go vet ./...`, and `git diff --check`.
- Independently passed GoAster `go test -race -count=1 -timeout=5m ./internal/migration ./internal/scheduler ./internal/chat`; Web `go test -race -count=1 -timeout=5m ./internal/app`.
- Both paired local contracts reviewed. Web final Sol SATISFIED; GoAster final Terra then Sol acceptance SATISFIED after narrowing DNS/TLS/generic provider transport errors to terminal. Earlier blocked review records are historical and superseded by final acceptance.
- Offline acceptance tracking: `pantry-snacks/pantryclub-frontend#349`, org ProjectV2 #2 `Status=Done` independently verified. This is one coordinated offline acceptance run, not live migration success.
- No existing-agent upgrade/restart, real provider/Redis/Telegram calls, paid server creation, or live end-to-end migration. Main publication may trigger Web automatic deployment; rollout completion remains unverified.
