# API-first migration independent review — 2026-11-16

**SATISFIED** — reviewed the tracked and untracked migration edits against `api-first-web-contract-20261116.md` and repository instructions. Validation below is offline; it does not establish live service or Telegram behavior.

## Findings and fixes

- The success path discarded confirmed `created` when a setup URL or other handoff data was malformed. It now retains `created`, reports `handoff_failed`, and returns only independently validated reconciliation fields. Delivery failure and a changed paired owner also retain `created`; no handoff is sent to a changed owner.
- Untrusted response fields could carry reflected credentials into model output. Response codes and stages now use bounded known values, request token and owner secret are scrubbed from returned strings, IP addresses are parsed, and diagnostics omit remote bodies and secrets. The handoff URL must have the configured origin and the exact generated owner secret in its fragment before private delivery.
- Tests now exercise owner changes, unavailable and failed private delivery, wrong origin and secret, lost response, redirect, created HTTP errors, reflected secrets, public/private key correspondence, exact optional locations, and explicit fresh calls without retained retry state. Scheduled tool calls are rejected. The production `serve` wiring is checked for the one-shot tool and absence of the retired scheduler startup.
- One race was found in the new lost-response test counter and fixed using an atomic counter. The rerun passed.

## Flow and limitations

A private paired owner approves one call with the exact type and optional strict locations. A fresh interactive retry needs no prior record or monitor ID; the skill asks for a duplicate-charge warning and one fresh approval. Transport failure after dispatch is `unknown` and no automatic retry occurs. Service-confirmed creation remains `created` when handoff fails. Ephemeral private material is delivered only through the revalidated paired owner and is not retained by this flow.

The production entry point does not start the legacy migration scheduler. Legacy implementation and tests remain in the repository, and old protected records are not automatically deleted. Ordinary cron availability monitoring is intentionally read-only and notification-only. Automatic eventual creation is **not provided**; there is no scheduled approval-dependent create. New creation requires fresh interactive approval.

## Validation

With the locally installed Go 1.25 toolchain, `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

- `go test ./...` — passed.
- `go vet ./...` — passed.
- `go test -race ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent` — passed after fixing the test counter race.
- `git diff --check` — passed.

## Follow-up to Sol's blocked review

The subsequent [Sol review](api-first-migration-final-review-20261116.md) found three blockers in the earlier snapshot. That report remains the record of its findings; the earlier SATISFIED conclusion did not cover these issues. This follow-up addresses them in the current working tree:

- Bundled migration META and AUTH and the README now describe the one-shot private flow, conversational retry warning and fresh approval, ephemeral handoff, and notification-only ordinary cron. Existing user-owned installed skills are not overwritten; their reconciliation and rollout are separate.
- Create transport EOF is classified as unexpected while retaining the original cause. Normal EOF and cancellation remain exempt elsewhere. Service failure diagnostics include allowlisted status, code, stage, creation state, and provider code. Telegram owns its delivery error diagnostic and returns a safe wrapper preserving the cause; the caller avoids a duplicate. Tests cover these boundaries and redaction.
- The offline packaging check now validates source and installed META, AUTH and SKILL guidance for privacy, one-shot approval, conversational retry, no persistence and read-only cron notifications, as well as rejecting retired guidance.

Validation used `/usr/local/go/bin/go` 1.25.9 with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

- `/usr/local/go/bin/go test ./...` — passed.
- `/usr/local/go/bin/go vet ./...` — passed.
- `/usr/local/go/bin/go test -race -count=1 -timeout=180s ./...` — passed.
- `/usr/local/go/bin/go test -race -count=1 ./internal/migration` — passed after the final provider-code assertion.
- `TMPDIR="$PWD" sh packaging/test-skill-assets.sh` — passed.
- `git diff --check` — passed.

These are offline checks only; installed user skills and live services were not changed or verified.

## Follow-up to the final re-review

The Telegram multipart transport boundary now classifies an HTTP EOF without cancellation as unexpected while retaining the original wrapped cause. Its single bounded diagnostic carries the sendDocument method and safe migration handoff, creation-state, and failure-code context; the delivery ownership marker prevents a duplicate from `CreateOnce`. Expected cancellation and normal EOF handling elsewhere remain unchanged. The adjacent Telegram JSON transport was inspected; this targeted change addresses multipart delivery without changing unrelated HTTP behavior.

An offline regression drives a successful one-shot create followed by a real local Telegram HTTP connection closing after the multipart upload. It checks the preserved EOF causes, confirmed creation and failed handoff, one safe ERROR, and no duplicate caller diagnostic. A separate test covers cancellation joined with EOF.

Validation with `/usr/local/go/bin/go` 1.25.9, `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

- `go test ./...` and `go vet ./...` — passed.
- `go test -race -count=1 -timeout=180s ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent` — passed.
- Uncached targeted Telegram EOF and cancellation tests, including a race run — passed.
- `TMPDIR="$PWD" sh packaging/test-skill-assets.sh` — passed.
- `git diff --check` — passed.

All checks were offline; no live provider or Telegram service was contacted.
