# Background command validation (2026-11-19)

## Scope and result

This change is limited to `background_command` compatibility when a model interface sends every declared string property. The registered object schema is unchanged: `action` is required, and `name`, `id`, `data`, and `keys` remain optional strings. The tool description now explains that empty strings in fields unused by the selected action are accepted as absent.

The handler applies the same rule across all actions: only an exactly empty irrelevant string is ignored, while any raw nonempty forbidden value, including whitespace-only `name` or `id`, returns `invalid_request` with `forbidden_fields`. Required command-name and session-ID checks retain their existing trimming behavior, so whitespace-only values are still treated as missing when those fields are required. `input`/`write` still require that `data` be supplied (an explicit empty value retains its previous behavior); `send-keys` requires nonempty supported key tokens, including named keys, modifier forms, or one printable character per token. Parsing still rejects malformed/non-object JSON, duplicate or unknown fields, non-string values including null, trailing data, oversized values, and missing action. Allowlisting, input and secret warnings, execution controls, and no-replay behavior were not changed.

## Regression coverage

- An action matrix covers all 11 actions with all five declared string fields present, checking that irrelevant empty fields pass and both ordinary nonempty and whitespace-only forbidden values fail without echoing their value.
- Validation cases retain missing action/name/ID/data and empty send-keys failures, including whitespace-only required name and ID values.
- A local fake Codex provider checks the registered model-facing schema, sends a `list` call with every declared field populated and empty irrelevant placeholders, then verifies the successful tool output reaches the follow-up request.
- The command-manager fake-helper and tool end-to-end tests use a generic non-GitHub `legacy-keys` PTY with `keys_only: true`; both reject raw `input`/`write`, then send `up,down,left,right,y,N,comma` and assert that the helper received exactly `\x1b[A\x1b[B\x1b[D\x1b[CyN,`. They also require completed `exited` state, `exit_code: 0`, and no `input_failed`; the helper is local test code and never runs GitHub/login or receives a secret.
- The tool-description expectation preserves generic opt-in non-secret `input`/`write` while scoping the prohibition on those actions to the bundled `github-login` profile.
- Existing parser, allowlist, input, privacy, and unexpected-failure regressions continue to run in the full suite.

## Verification and gaps

Passing offline verification used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` with Go 1.25.9 at `/usr/local/go/bin/go`.

- `go test ./... -count=1 -timeout 300s` (including the background-command and fake-provider regressions): PASS.
- `go vet ./...`: PASS.

The provider-flow regression uses only a local fake HTTP server and a read-only `list` action; it does not contact a live model or exercise a side-effecting tool call. Existing input policy is guidance and command/profile validation, not automatic secret detection; callers must continue to follow the non-secret input restrictions.

## Independent final verification

Parent independently reran the full offline suite, `go vet ./...`, focused `go test -race ./internal/chat ./internal/command -count=1 -timeout 300s`, and `git diff --check`: PASS. Terra review found forbidden whitespace name/ID handling; fixed with raw nonempty flags and regression cases. Subsequent Terra review and final Sol review: SATISFIED. This is source-only validation: running-agent rollout and a real provider/GitHub login remain unverified.

## Current universal PTY key-policy scope (2026-11-19)

This delivery changes only the key policy for all allowed, eligible interactive
PTYs. Every existing named terminal key remains available, and each
comma-delimited token may also be one printable character with case preserved.
Use the named `comma` key for a literal comma. `keys_only` only disables raw
`input`/`write` for backward compatibility; it never restricts `send-keys`.
There is no GitHub-specific key exception and no new configuration knob.

The existing allowlist, command-name-only selection, interactive/PTY checks,
direct execution, bounds, and no-secrets protections remain in force. Empty
keys, unknown or pasted multi-character tokens, raw control/ANSI bytes,
invalid modifier forms, and encoded sequences over `MaxSessionInputBytes` are
rejected. The bundled `github-login` profile remains fixed and keeps
`input`/`write` unavailable because of the same `keys_only` policy.

The exact-text authentication and tool-description regressions were updated,
and focused coverage verifies generic legacy `keys_only` profiles reject raw
input/write while the PTY helper receives the exact arrow, lowercase,
uppercase, and named-comma bytes above and exits successfully. No terminal
parser, CPR reply, initial-winsize fallback, writer serialization, or
environment override is part of this delivery.

## Current verification and deferred terminal diagnosis

Offline verification used Go 1.25.9 at `/usr/local/go/bin/go`:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...`: PASS.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/chat ./internal/command -count=1 -timeout 300s`: PASS.

Terminal diagnosis is complete: the baseline lacks CPR replies and an actual
initial PTY window size. A proper library solution needs explicit approval from
Thomas for the required pointer boundary; that approval has been requested, so
the terminal fix is deferred. Running-agent behavior, live GitHub login,
credential use, browser interaction, deployment, and rollout remain
unverified.
