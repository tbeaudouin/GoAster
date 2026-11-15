# Migration diagnostics validation — 2026-11-15

## Changes

- The model-facing migration tool exposes start, status and stop. The skill directs public pricing and discovery to public informational sources; availability used for creation remains authoritative inside the monitor.
- Migration results include bounded structured stage, code, fixed safe cause, HTTP status and allowlisted provider code when known, monitor ID, result, creation and monitoring states, and fixed next action. Runtime failure boundaries retain wrapped causes for safe ERROR logging. Status reads and validates retained scheduled diagnostics, including fixed cause and action text.
- Machine Web errors accept only a bounded, exact v1 JSON envelope with the specified fixed code and stage sets, unique fields, valid status and a small fixed provider-code set. Unrecognized or legacy bodies fall back to HTTP classification without inferring provider authentication. Post-dispatch failures remain creation-unknown regardless of the envelope. No upstream body or arbitrary provider text is exposed.
- Successful creation remains created when private paired delivery fails. Atomic scheduler claims and stop coordination, dispatch markers and manual review prevent automatic duplicate create; only no-capacity checks rearm. No-capacity scheduled checks no longer append repeated diagnostics. Diagnostic selection uses sequence order even if timestamps disagree.
- Added offline fixtures for provider envelope authentication, DNS, timeout and TLS classification, malformed/duplicate/injected envelopes, local transport classification, persisted free-text validation, scheduled failure status, and scheduled delivery failure with retained creation and no repeat. Existing offline coordination tests cover in-progress stop and lost responses.

## Validation

All commands used `GOPROXY=off GOSUMDB=off` and `/usr/local/go/bin/go`:

- `go test ./...` — passed before the final fixed-message-only diagnostic change; `go test ./internal/migration` passed after it.
- `go vet ./...` — passed after the final diagnostic change.
- `go test -race ./internal/migration ./internal/chat` — passed after the final diagnostic change.
- `git diff --check` — passed before this report.

No external provider, cloud, GitHub, real secrets, or deployment was used. Existing repository tests include loopback `httptest` servers; the new Web fixtures use an in-process HTTP doer.

## Gaps and tradeoffs

Protected diagnostic storage still uses exclusive append-only records. `protectedfile` currently provides exclusive publication and identity-checked removal, but no atomic protected replacement; replacing an existing file by remove-then-write could lose the last failure on interruption or race. A safe single-record implementation needs an atomic replacement primitive with equivalent ownership, symlink, durability and concurrency guarantees. Normal no-capacity retries no longer grow this history, but other state transitions can still create records; concurrent writers can fail closed on a sequence collision. A future protected atomic replacement can remove this complexity and bound retained records.

The counterpart implementation and its exact provider-code vocabulary were unavailable for integration testing. Only a small fixed set of provider codes is accepted, so other codes fall back to HTTP classification. Real counterpart compatibility and live provider behavior remain unverified. Coverage is focused rather than exhaustive for every possible failure stage and scheduler/storage fault.

## Final-review blocker fixes — 2026-11-15

The blocked independent review remains in `migration-diagnostics-final-review-20261115.md`. Follow-up changes accept the optional provider code only within the exact validated envelope and require its HTTP status to match the actual response. Terminal execution diagnostics are written while the execution claim is held; coordinated stop reloads validated history afterward, preserves established creation certainty, and does not append another identical stopped state. A corrupt history does not prevent coordinated stop: the result reports the verified stopped state, unknown creation certainty, and the history error. Repeated start reads the validated retained diagnostic and original request terms.

Regression coverage includes omitted, malformed, duplicate and unexpected envelope fields and mismatched status; publication before claim completion; stop-time history changes and repeated stops; malformed history; and terminal repeated starts after failure and successful creation. The stop interleaving test injects terminal creation evidence immediately before the coordinated stop, reproducing the stale-read ordering deterministically.

Final offline validation, with `GOPROXY=off GOSUMDB=off` and `/usr/local/go/bin/go`:

- `go test ./...` — passed.
- `go vet ./...` — passed.
- `go test -race ./internal/migration ./internal/chat ./internal/scheduler` — passed.
- `git diff --check` — passed before this documentation addition.

No live services, cloud, secrets, deployment, GitHub, or commit were used for this follow-up.
