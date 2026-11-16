# Conversational migration retry validation — 2026-11-16

Reviewed the uncommitted retry clarification and corrected conflicting guidance: in an interactive private migration, unknown creation permits one fresh attempt after the stated warning and one affirmative answer, using the exact retained prior reference. For that narrowly confirmed fresh retry, account and handoff inspection are optional; retained-history, in-flight, and unconfirmed-stop blockers remain. Automatic replay remains prohibited.

Updated diagnostic guidance and narrowly accepted exact legacy persisted actions, normalizing them on load after validation. Regression coverage checks exact guidance, legacy loading, injected text and invalid retry-state rejection; the existing fresh-attempt tests cover confirmation and retained history. Corrected two stale packaging assertions to match the existing skill wording.

The earlier offline run used the then-selected Go 1.25.0 toolchain. The final warning-compatibility follow-up below was run with Go 1.25.9; its results supersede that earlier suite record. No live provider or deployment validation was performed.

## Follow-up resolution of the historical final-review findings

`docs/conversational-migration-retry-final-review-20261116.md` remains the
historical review, including its `NOT SATISFIED` verdict and two findings. This
follow-up records their resolution; it does not rewrite or re-review that
report.

The standing `requestUncertaintyWarning` now keeps the external-state check for
every other tool and workflow, while explicitly limiting the exception to an
interactive private `goaster_migration` NEW attempt. That exception requires
one duplicate/extra-charge warning, one owner yes, and the exact retained
reference through `retry_attempt`; it does not require an account check/report,
deletion proof, a monitor ID, or repeated confirmation. It prohibits automatic
replay and retains unavailable/corrupt history, in-flight execution,
unconfirmed stop, and unresolved authentication as blockers. The post-tool
authentication recovery warning is scoped consistently and still requires
authentication resolution. `TestModelUncertaintyInstructionRemainsAfterSessionReload`
now checks the actual provider request `Instructions` for both the generic
warning and the migration exception.

The central API contract now distinguishes prohibited replay of the same
`operation_id`/capability from a separately owner-confirmed fresh source
operation with a new operation ID/capability. Account or status-page inspection
is optional for that fresh operation and remains required only for
same-operation reconciliation. `docs/goaster-migration.md` states the same
boundary and preserves the retained-history, in-flight, unconfirmed-stop, and
authentication blockers.

The focused follow-up checks used Go 1.25.9 from `/usr/local/go/bin/go`, with
`GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` where applicable:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./internal/chat -run 'Test(ModelUncertaintyInstructionRemainsAfterSessionReload|RequestFailureDiagnosticIncludesUnderlyingErrorAndContext|MigrationToolExposesOnlySafeMonitorActions)'` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./internal/migration -run 'Test(UnknownRetryRequiresExactCurrentConfirmationAndFreshRecords|MissingPriorOperationEvidenceCannotAuthorizeRetry|CorruptPriorDiagnosticCannotAuthorizeRetry|DispatchMarkerMakesStatusAndStopUncertain)'` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./internal/chat ./internal/migration` — passed.
- `bash packaging/test-skill-assets.sh` — passed offline.
- `git diff --check` — passed.
- `rg -n -C 1 'requestMigrationRetryException|requestUncertaintyWarning|postToolAuthenticationWarning|same .*operation.*replay|fresh source operation|account.*optional|not a gate|in-flight|unconfirmed stop|unresolved authentication' internal/chat/provider.go internal/chat/request_diagnostics_test.go docs/central-machine-api-contract.md docs/goaster-migration.md` — passed; the generic external-check wording remains on other-tool/scheduled paths, while the migration exception and fresh-operation/account-optional wording appear in the intended code and linked docs.

No cloud, GitHub, secrets, deployment, live provider, provisioning, or external
network activity was used. No provisioning machinery was changed, no new
pointers were introduced, and no commit was made.

## Post-tool authentication warning compatibility follow-up

Restored the original authentication warning sentence verbatim, then appended
the narrowly scoped fresh private migration exception. Authentication
resolution, technical blockers, and the prohibition on automatic replay remain
explicit. The exact legacy warning remains recognized as a marker/message for
existing agent and Telegram flows; modified messages are still rejected.

Validated offline with `/usr/local/go/bin/go` version `go1.25.9 linux/amd64`
and `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -count=1 ./...` — passed, all packages.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race -count=1 ./cmd/goaster-agent ./internal/chat ./internal/telegram -run 'TestAgentReportsSafeSkillRecoveryInDirectAndStreamResponses|TestCodexProviderPreservesBackendErrorsThroughPublicFlows|TestTelegramDisplaysSafeSkillRecoveryForDirectAndStreamErrors'` — passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` — passed.
- `bash packaging/test-skill-assets.sh` — passed offline.
- `git diff --check` — passed.
