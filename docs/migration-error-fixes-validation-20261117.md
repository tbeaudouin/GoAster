# Migration diagnostic fixes validation — 2026-11-17

## Implemented

- The one-shot Web response accepts additive `error.provider_message` and
  `error.provider_fields` values beside `code`, `stage`, `http_status`, and
  `provider_code`.
- Provider messages, field names, and field messages are bounded and fixed
  allowlists. Unknown text, raw bodies, URLs, setup material, tokens, bearer
  secrets, and private keys are dropped from model output and diagnostics.
- Only an explicit authoritative `creation_state: "not_created"` from the
  operation result is used. HTTP 422 or `invalid_input` metadata alone never
  establishes this state in GoAster; missing or unrecognized state always
  sanitizes to `unknown`, regardless of metadata. Explicit `created` and
  `unknown` survive contradictory 422 metadata, `created` survives timeout
  metadata, and explicit `not_created` plus timeout metadata is normalized
  conservatively to `unknown`.
- The response decoder first enforces unique lower-case top-level fields, then
  keeps strict typed decoding for the complete response contract. If the error
  envelope is malformed, only an unambiguous exact `created` state and a
  positive server ID plus any request-matching server label survive as
  `code: "response_unusable"`; duplicate or ambiguous top-level creation
  evidence remains `unknown`. Unknown/error values are not copied, and malformed
  responses never reach handoff or count as successful responses.
- The accumulated diagnostic notice is bounded to 8 KiB including its
  app-generated prefix. It trims older details/records while retaining the
  newest creation outcome, and repeated outcomes remain in occurrence order.
- The migration tool sanitizes operation seams before model output. The
  existing `SessionStore` persists only the safe migration diagnostic metadata
  in `migration_diagnostics`; generic tool results and synthetic conversation
  claims are not persisted.
- The tool result is checkpointed immediately after the migration handler
  returns, before model continuation. Successful turns also save the metadata
  with normal conversation state. A follow-up request reconstructs the same
  app-generated metadata notice after a restart without re-invoking migration.
- The observer retains the latest sanitized diagnostic in live session and
  `SessionStore` memory before attempting its checkpoint. A checkpoint failure
  is returned to the model loop and stops continuation; same-store reload and
  session construction fail closed while evidence is pending. Ordinary saves
  preserve and persist the pending record, while explicit replacement and disk
  version conflicts fail without clearing it; the pending flag clears only
  after a complete successful save. If the original storage is restored
  without a concurrent change, a later live follow-up safely persists the
  pending `created` outcome.
- Bundled migration and Hetzner-auth guidance uses read-only
  `GET /v1/server_types?name=<url-encoded-exact-type>` with
  `server_types[]` and `locations[].available`; it does not use the retired
  generic datacenters endpoint, invent an availability tool, expose tokens, or
  make capacity guarantees. Public rough pricing remains informational.

## Offline commands and results

All commands ran from the repository root with the installed Go 1.25.9 binary.
No live provider, Web, Redis, cloud, Telegram, deployment, GitHub, or
secret-backed operation was used.

| Command | Result |
| --- | --- |
| `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s` | PASS |
| `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` | PASS |
| `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/chat ./internal/migration ./internal/diagnostics ./internal/artefacts -count=1 -timeout 300s` | PASS |
| `sh packaging/test-skill-assets.sh` | PASS — allowlist, installed package, and local Markdown reference checks |
| `git diff --check` | PASS |

Focused tests cover additive 422/timeout diagnostics and contradictory state
precedence through Web decoding and tool sanitization, missing and invalid
creation state through sanitization, malformed string/unknown nested error
envelopes and duplicate top-level `creation_state` through
CreateOnce-to-tool-to-checkpoint-to-restart context, hostile provider details,
safe logging and tool output, bounded worst-case metadata after restart,
repeated outcomes across failed turns and restarts, successful follow-up/restart
continuity, model failure/cancel/deadline after a tool result,
pre-replacement checkpoint failure and live recovery, same-store Load and
second-session refusal while pending, evidence-preserving ordinary Save,
explicit replacement refusal, disk-version conflict, concurrent reload refusal,
no repeated migration invocation, and current packaged availability guidance.

## Exact remaining gaps

- A session without an existing `SessionStore` has no restart durability; the
  in-memory notice still works for that live session.
- If the process stops before the migration handler returns, no diagnostic
  checkpoint can be made. If the checkpoint or later state save fails, is
  interrupted, or the filesystem loses the atomically replaced file, only
  the existing filesystem durability guarantees apply; live-memory retention
  does not make a failed checkpoint restart-durable. A concurrent disk change
  is intentionally refused rather than merged.
- The pending guard is held by the live `SessionStore`; an independent process
  or separately constructed store can only see what reached disk. It cannot
  reconstruct a pending result that failed before replacement.
- An explicit `created` response marked `response_unusable` retains only safe
  identity for manual reconciliation; it does not authorize handoff or an
  automatic retry.
- If a safe provider message or field detail is not on the fixed allowlist,
  it is intentionally omitted rather than retaining raw provider text.
- A lost response, timeout, or other `unknown` outcome still requires external
  reconciliation before any separately approved retry. This change does not
  inspect provider state or guarantee capacity.

## Follow-up verification

| Command | Result |
| --- | --- |
| `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/migration ./internal/chat -count=1 -timeout 120s` | PASS |
| `git diff --check` | PASS |

## Parent final verification and acceptance

November 17, 2026: parent independently reran the final source using the local Go 1.25.0 toolchain with GOTOOLCHAIN=local, GOPROXY=off and GOSUMDB=off: uncached full `go test ./... -count=1 -timeout=5m`, full `go test -race ./... -count=1 -timeout=5m`, `go vet ./...`, `go build ./...`, packaging skill/reference checks and `git diff --check` all PASS. An initial parent run with global TMPDIR=/root/tmp failed the privilege-drop attachment fixture because the unprivileged child cannot traverse /root; the final rerun used the normal Go-test temporary location and passed without a source workaround. Only packaging used TMPDIR=/root/tmp.

Final independent acceptance: `migration-error-fixes-terra-final2-20261117.md` then `migration-error-fixes-sol-accepted2-20261117.md`, both SATISFIED. Earlier review reports are chronological findings, resolved by these final reports, not outstanding blockers. Sol independently repeated full tests, vet, full race, build and packaging with its local Go 1.25.9 toolchain.

No installed binary or installed-skill update, live migration/provider/managed Redis/Telegram/browser/SSH acceptance, paid creation or actual cron was performed. Failed checkpoint cannot promise restart durability; pending live evidence is guarded rather than silently lost or merged with conflicting disk state.
