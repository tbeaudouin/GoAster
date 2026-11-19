# Background command validation (2026-11-19)

## Scope and result

This change is limited to `background_command` compatibility when a model interface sends every declared string property. The registered object schema is unchanged: `action` is required, and `name`, `id`, `data`, and `keys` remain optional strings. The tool description now explains that empty strings in fields unused by the selected action are accepted as absent.

The handler applies the same rule across all actions: only an exactly empty irrelevant string is ignored, while any raw nonempty forbidden value, including whitespace-only `name` or `id`, returns `invalid_request` with `forbidden_fields`. Required command-name and session-ID checks retain their existing trimming behavior, so whitespace-only values are still treated as missing when those fields are required. `input`/`write` still require that `data` be supplied (an explicit empty value retains its previous behavior); `send-keys` still requires nonempty named keys. Parsing still rejects malformed/non-object JSON, duplicate or unknown fields, non-string values including null, trailing data, oversized values, and missing action. Allowlisting, input and secret warnings, execution controls, and no-replay behavior were not changed.

## Regression coverage

- An action matrix covers all 11 actions with all five declared string fields present, checking that irrelevant empty fields pass and both ordinary nonempty and whitespace-only forbidden values fail without echoing their value.
- Validation cases retain missing action/name/ID/data and empty send-keys failures, including whitespace-only required name and ID values.
- A local fake Codex provider checks the registered model-facing schema, sends a `list` call with every declared field populated and empty irrelevant placeholders, then verifies the successful tool output reaches the follow-up request.
- Existing parser, allowlist, input, privacy, and unexpected-failure regressions continue to run in the full suite.

## Verification and gaps

Passing offline verification used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` with Go 1.25.9 at `/usr/local/go/bin/go`.

- `go test ./... -count=1 -timeout 300s` (including the background-command and fake-provider regressions): PASS.
- `go vet ./...`: PASS.

The provider-flow regression uses only a local fake HTTP server and a read-only `list` action; it does not contact a live model or exercise a side-effecting tool call. Existing input policy is guidance and command/profile validation, not automatic secret detection; callers must continue to follow the non-secret input restrictions.

## Independent final verification

Parent independently reran the full offline suite, `go vet ./...`, focused `go test -race ./internal/chat ./internal/command -count=1 -timeout 300s`, and `git diff --check`: PASS. Terra review found forbidden whitespace name/ID handling; fixed with raw nonempty flags and regression cases. Subsequent Terra review and final Sol review: SATISFIED. This is source-only validation: running-agent rollout and a real provider/GitHub login remain unverified.
