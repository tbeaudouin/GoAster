# Migration capacity and retry review — 2026-11-16

**SATISFIED** — No actionable local blocking findings found in the revised diff.

The previous HTTP-classification finding is fixed: retries require a decoded v1 envelope with matching HTTP status and an explicitly allowed code, stage, and provider-code combination. Unknown or malformed bodies, status-only gateway errors, terminal provider causes, and storage/create stages do not qualify. Local cancellation is terminal; only timeouts and temporary network errors in the Web availability transport qualify without an envelope.

The monitor claims execution before checking, persists the bounded retry count before rearming, resets it on a valid empty response, and stops for review on exhaustion or persistence failure. Retained created/unknown evidence and the dispatch marker prevent another automatic create; creation itself is not retried. Status reconciles retained diagnostics with scheduler state. Diagnostics retain allowlisted fields rather than response bodies or credentials. These conclusions depend on the existing protected storage and scheduler durability guarantees.

Compatibility assumptions for the separately coordinated Web implementation:

- A valid capacity miss returns HTTP 200, v1, the requested `server_type`, and a structurally valid empty `locations` array; available locations follow the source client's validation and requested-location rules.
- Invalid type, image, architecture, provider authentication and other permanent failures return errors rather than successful empty capacity. Malformed location data is an error, not a capacity miss.
- Retryable HTTP errors use the exact v1 envelope accepted by `decodeWebError`, with matching actual and envelope status and one of the combinations documented in the validation record. Other responses stop for manual review.
- Availability is read-only and does not create resources. Web's pre-create guards and operation handling uphold the one-create contract when the source dispatches its single create request; uncertain creation must not be reported as definitely absent.

The paired Web selector and guards were not inspected in this local review; their coordinated review must establish those assumptions. Their absence from this checkout is not a local blocker.

Offline verification: `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -timeout=120s -count=1 ./internal/migration ./internal/scheduler` passed. The validation record reports the broader offline suite and race checks. No services or network were contacted during this review.
