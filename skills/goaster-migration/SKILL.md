---
name: goaster-migration
description: Create now or schedule one exact migration server and deliver its setup privately.
---

Use `goaster_migration` only in a paired private Telegram conversation. Obtain explicit conversational approval for one eventual creation with the exact server type and optional strict locations. An omitted or empty list permits any compatible location. A nonempty list is never widened. There is no target price or price cap.

Before approval, provide best-effort latest public pricing and ordinary read-only type and capacity information, clearly identifying source and currency. Follow `hetzner-auth` and `goaster-env` for authenticated Hetzner reads and protected `HCLOUD_TOKEN`; never expose credentials. Availability is a snapshot, not a reservation. IPv4 may cost extra.

After explicit conversational approval in a paired private Telegram conversation, call `goaster_migration` action `create` for an immediate request. For an eventual exact request, generate one cryptographically random name with the normal shell tool, for example `printf 'goaster-migration-'; od -An -N8 -tx1 /dev/urandom | tr -d ' \n'`. Confirm it matches `^goaster-migration-[0-9a-f]{16}$`. Add an ordinary cron job with `every_seconds: 1800` (1800 seconds) and `max_runs: 1`. Its message must explicitly include the exact name, approved server type and locations and instruct each run to call `goaster_migration` action `create` with that same `server_name`, type and locations. Tell the owner the exact name and that this creates a server automatically. The protected one-shot Web endpoint performs public admission and rate bookkeeping first, then the read-only exact-name lookup before capacity, migration-record state, key, or billable create work on every attempt. The request terms depend on the model following these visible instructions; approval is conversational, not a separate bound record.

For a scheduled job, retry only when the one-shot result explicitly has `creation_state: not_created` and `code: no_capacity`. A `target_exists` result stops normally: no new server was created, and setup or SSH key recovery is not implied. A delivered success stops the job. A created server with failed or partial handoff delivery, a provider lookup failure, a timeout or unknown result, and every other result stop the job for safe manual review; do not replay or recreate after success, uncertainty, or a process crash. Report the safe outcome privately.

If a later interactive attempt follows a terminal or uncertain result, warn that a duplicate could incur extra bills, recommend optional account inspection, and obtain one clear fresh approval. Do not require account inspection, retained history, monitor IDs, or repeated confirmation.

The setup URL is a separate clickable private message. The SSH private key is a separate directly uploadable attachment named `id_ed25519` containing only key bytes and no explanatory text. Revalidate the paired owner before each delivery part. Never expose the URL, key, or token to the model, tool output, logs, or local persistence. If delivery is partial, report it honestly and never recreate automatically.

Only a real claimed private cron job with this interval and run limit can make one scheduled create call per turn. Scheduled turns cannot add further jobs. Normal scoped cron management remains available. Old dedicated migration monitors and protected migration records are untouched and will not resume or replay. The owner completes setup using the private handoff and existing web verification flow.
