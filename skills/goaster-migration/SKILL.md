---
name: goaster-migration
description: Create now or schedule one exact new Hetzner Cloud server and deliver its setup privately.
---

## 1. Check the token

Create one new Hetzner server only, not an existing server or another provider. Use only in paired private Telegram, and check for a token before proceeding: read the protected `<real user home>/.goaster/env` first, then check process `HCLOUD_TOKEN` as fallback. Never claim the token is missing or unreadable without actually checking. Never show or request a pasted token in chat. If it is missing, tell the paired owner to send `/get-env`, add the token locally to `goaster.env`, attach it with `/env` in the same message, review the preview, and reply exactly `ACCEPT`. Fail safely if the protected settings cannot be read. Recheck the protected environment after acceptance. A present token does not prove authentication. Keep secrets out of model input/output, tool output, logs, and extra local copies; the protected environment upload is the intended credential storage.

## 2. Choose the server

Choose an exact size and type that fits the requested workload, preferring a fitting CX or CAX without favoring either. CX is x86 and CAX is ARM; choose CAX only when the user's software supports ARM. CPX requires the user's explicit choice; never substitute it automatically. Before approval, check the latest official public Hetzner catalogue with normal internet search/fetch and give a rough price with its source, currency, and date; include the VAT basis when shown. If pricing is unavailable, say so and continue. Price is context, not a target, cap, ranking, or required quote. Public availability is not a live capacity guarantee. By default, omit `locations` or pass an empty list to allow any compatible available location. If the user specifies locations, treat them as a strict list and never widen it.

If a current authenticated availability clarification is needed, follow
`hetzner-auth` and use only read-only `GET
/v1/server_types?name=<url-encoded-exact-type>`. Match the requested type and
match it in `server_types[]`; inspect its `locations[].available` values,
applying an approved location list strictly. This is a snapshot, not a
capacity guarantee or reservation. Never
use the retired generic `GET /v1/datacenters` endpoint (removed 2026-10-01),
shell around the API, or invent a migration availability tool or endpoint. Keep
the token and Authorization header out of model context, chat, logs, and
command output. Public rough price remains informational and is not an API
pricing prerequisite.

## 3. Get approval for one exact choice

Generate one cryptographically random name using the shell and verify it matches `^goaster-migration-[0-9a-f]{16}$`. Ask for one private spending approval that includes the requested timing—`immediate` or `eventual`—and summarizes the exact `server_name`, exact `server_type`, location choice, and rough price if available. That one approval covers the timing and exact values; do not change the approved name, type, size, or locations.

## 4. Create or arrange a retry

For `immediate`, call `goaster_migration` with `action: "create"` and the approved `server_name`, `server_type`, and `locations`. For `eventual`, make no immediate create call and schedule directly, without another approval. The ordinary generic cron job `message` must instruct each run to call `goaster_migration` with `action: "create"` and the exact approved `server_name`, `server_type`, and `locations`, preserving an approved omission or empty list. Set `every_seconds: 1800` and `max_runs: 1`. Each claimed run makes one create call. Waiting may take days; never promise a time.

After the definite pair `creation_state: "not_created"` and `code: "no_capacity"`, an immediate request may offer eventual waiting; obtain approval for that timing once before scheduling. Rearm eventual waiting only for that pair, and never add a scheduled job from a scheduled turn. Stop on every other result, including success, `target_exists`, uncertainty, partial handoff, or crash. Do not automatically repeat a create or resend a handoff. Only when considering another creation after an uncertain or known-created result, warn of possible duplicate charges and obtain one fresh approval. Do not give that warning in the first reply or after a definite no-create / no-capacity result. Account inspection is optional advice, not an approval gate.

## 5. Deliver the setup privately

Send the setup link and SSH key in separate private messages, revalidating the paired recipient before each delivery. Send the key as a directly uploadable `id_ed25519` attachment containing only the key bytes. Never expose the link or key in model input/output, tool output, logs, or persistence. Report any delivery failure or partial handoff honestly, and do not create another server to recover delivery.
