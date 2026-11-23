# Authentication and authorization

Use this skill only in a paired private Telegram conversation. Before claiming
that `HCLOUD_TOKEN` is missing, actually check the protected
`<real user home>/.goaster/env` first and process `HCLOUD_TOKEN` second, without
exposing the value. If the protected file cannot be read, fail closed. The
workflow must never request a pasted provider token. For a missing token, use the private
`/getenv`, local edit of `goaster.env`, same-message `/env` attachment,
names-only review, and exact `ACCEPT` flow.

Obtain one private spending approval for the cryptorandom exact
`server_name` matching `goaster-migration-[0-9a-f]{16}`, exact `server_type`,
optional `locations`, and rough public price. Before requesting approval, you
MUST check the latest official public rough pricing; make a best-effort check
and explicitly report unknown if unavailable. That approval covers the
immediate first create and unlimited retries every 30 minutes after any migration error, including uncertainty and incomplete handoff; do not ask
the owner to choose now versus waiting. Follow the four-step `SKILL.md` journey
for creation and retry. The rough public price is informational only, not a
target, ceiling, ranking, or billing discussion.

Before creation, verify private delivery is available. The runtime generates
keys internally and, only after validated confirmed success, sends the setup URL
as a separate clickable private message and the SSH private key as a key-only
`id_ed25519` attachment to the revalidated paired owner. Keep the token, URL,
key, request body, and provider response out of model input/output, tool output,
cron messages, and logs. Do not persist them or make extra copies; the
protected environment is the intended token storage. A validated initial `target_exists` for the exact approved name finishes without creating a retry job. If delivery is uncertain
or partial, report the state and keep the same job enabled. The next normal create
uses the existing exact-name guard; never resend or replay the handoff, recover keys,
or adopt an existing target. The two
handoff parts are separate private messages.

For scheduled attempts the runtime queues one fixed, safe status to the
originating conversation after the result is handled. No status contains private
handoff material; a local failure requires review.
