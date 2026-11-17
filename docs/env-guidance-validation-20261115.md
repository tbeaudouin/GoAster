# Environment guidance validation

Migration token lookup distinguishes an absent `HCLOUD_TOKEN` from malformed
settings, an invalid saved token, or a non-regular settings path. The active
one-shot create fails closed before key generation or HTTP when protected
settings cannot be read or the token is unavailable. Telegram `/get-env`
sends the saved environment bytes unchanged and keeps values out of the
agent, model, command output, and logs.

Tests use temporary homes and synthetic credentials with local transports. No
provider, Telegram, deployment, runtime, GitHub, or real secret was used.
