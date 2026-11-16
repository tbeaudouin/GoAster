# API-first migration integration

The active `goaster_migration` tool accepts only `action=create`, the exact server type, and optional strict locations. In a paired private Telegram conversation, it loads the current protected Hetzner token, generates an ephemeral Ed25519 key and bearer secret, and sends one request to the web contract in `api-first-web-contract-20261116.md`. The service chooses architecture and image. No price cap or operation history is used. The handoff goes directly to the revalidated paired owner. Secrets are not returned to the model or persisted by this flow.

Failures before dispatch report `not_created`; lost or unusable responses report `unknown`; confirmed server creation with failed Telegram delivery reports `created`. No automatic retry or resend occurs. A new attempt requires the skill's duplicate billing warning and one owner approval; account inspection is optional.

Dedicated migration monitors are retired: they will not resume or replay, and old protected records are not automatically removed. Optional ordinary cron monitoring is read-only, notifies when capacity returns, and stops on success. Automatic eventual creation after prior approval is not supported; another interactive approval is required.

Offline validation: `go test ./...`, `go vet ./...`, and `go test -race ./internal/migration ./internal/chat ./internal/telegram ./cmd/goaster-agent`. Tests use fake HTTP and delivery functions and must not contact live services.
