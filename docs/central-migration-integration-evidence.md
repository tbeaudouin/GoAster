# Central migration integration evidence

## Review and outcome

The agent starts the private migration scheduler before registering the ready migration tool and before serving model requests. It stops the scheduler on shutdown and on registration failure. Startup fails loudly if scheduler initialization or startup fails. Existing interrupted claims and terminal jobs fail closed; only enabled, valid jobs can resume. Missing operation material, changed terms or pairing, and uncertain create outcomes require manual review. The exclusive dispatch marker prevents a second create even after ambiguous local state. The local operation and scheduler directories are outside the model-writable artefacts workspace; they do not defend against the same OS identity or loss of all local state.

The source uses the documented v1 immutable request terms for availability and create, with a stable operation ID, capability, login public key and owner digest. Empty locations permit any eligible location; estimates are optional and never gate creation. Approval remains ordinary explicit private conversation, not a software approval mechanism or cryptographic binding. Start is guarded by conversation type and the scheduler captures the locally paired positive Telegram recipient. Pairing is checked again before creation and delivery; a change during an in-flight request cannot be undone and requires manual follow-up.

Review corrected the startup wiring, overly specific assumptions about the length of the server label prefix and maximum expiry, duplicate top-level response fields, and preservation of underlying paired-recipient errors. Successful create responses accept HTTP 200 or 201; the supplied contract does not specify a success status. Handoff material is retained in private local files for owner cleanup. Orphaned operation files and terminal singleton jobs require manual attention; no automatic replay or cleanup is performed.

The offline contract fixtures validate request fields, fixed terms, stable bindings, unavailable/restart/available execution, single creation, changed terms and recipient rejection, and secret exclusion. Coordination and scheduler tests cover claims, interrupted runs, and stop behavior; chat and Telegram tests cover private conversation and recipient delivery. Startup tests cover readiness ordering and failure cleanup; the actual agent socket test exercises startup and graceful shutdown with a private home.

Offline tests cannot prove the live Web implementation, provider semantics, Redis durability/no eviction and restore fencing, proxy log redaction, or owner identity. The Web contract activates central creation with valid configured Redis; it has no separate durability-confirmation gate, and Redis durability remains an accepted, unproven risk. These are deployment assumptions, not claims of live acceptance. No external network, credentials, cloud, Telegram, deployment, or GitHub writes were used; no commit was made.

## Validation

- `bash packaging/test-skill-assets.sh` — passed offline on 2026-11-15, including installed migration skill safety, immutable-term, and local-link checks.
- `GOPROXY=off go test ./... -timeout 120s` — passed offline on 2026-11-15.
- `GOPROXY=off go vet ./...` — passed.
- `GOPROXY=off go test -race ./internal/migration ./internal/scheduler ./internal/telegram ./internal/chat ./cmd/goaster-agent -timeout 180s` — passed.
- `git diff --check` — passed on 2026-11-15.

The initial full and race runs exposed the socket test's use of a home beneath a world-writable temporary directory; the test now uses a private repository-local home and both checks passed on rerun.
