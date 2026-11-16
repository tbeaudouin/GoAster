---
name: goaster-migration
description: Create a migration server and deliver its setup privately.
---

Use `goaster_migration` only in a paired private Telegram conversation. Obtain explicit approval for one creation with the exact server type and optional strict locations. An omitted or empty list permits any compatible location. A nonempty list is never widened. There is no target price or price cap.

Before approval, provide best-effort latest public pricing and ordinary read-only type and capacity information, clearly identifying source and currency. Follow `hetzner-auth` and `goaster-env` for authenticated Hetzner reads and protected `HCLOUD_TOKEN`; never expose credentials. Availability is a snapshot, not a reservation. IPv4 may cost extra.

Call `goaster_migration` with `action: create`, `server_type`, and optional `locations` exactly once per approved attempt. The service selects the architecture and image. It may report `not_created`, `unknown`, or `created`. A failed request may have created a server. Before a new attempt, warn that a duplicate could incur extra bills, recommend optional account inspection, and obtain one clear yes. Do not require account inspection, retained history, monitor IDs, or repeated confirmation. Never automatically retry creation.

The private key and setup URL go directly to the revalidated paired Telegram owner. If delivery fails, report that the server was created and delivery failed; do not resend or create again automatically. No migration key or handoff is retained locally.

If requested, ordinary cron may check availability read-only every 30 minutes, notify when capacity returns, and stop on success. Cron must never repeat create. Automatic eventual creation after prior approval is not supported; fresh interactive approval is needed when capacity returns.

Old dedicated migration monitors are retired and will not resume or replay. Do not delete old protected records automatically. The owner completes setup using the private handoff and existing web verification flow.
