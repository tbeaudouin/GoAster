# Authentication and authorization

Use this skill only in a paired private Telegram conversation. Obtain explicit conversational approval for one creation with the exact server type and optional strict locations. No separate approval record is required. This skill grants no access to Hetzner, GoAsterWeb, Telegram, SSH, or secrets; follow `goaster-env` and `hetzner-auth` for the configured credential source.

The provider token is request-scoped. The private key and setup URL go directly to the revalidated paired Telegram owner, never to the model, ordinary chat, logs, or local persistence. Stop before creation if private delivery is unavailable. If delivery fails after creation, report the created state and do not resend automatically.

A lost response can mean a server was created. Before another attempt, warn about duplicate charges, suggest optional account inspection, and obtain fresh conversational approval. Never automatically retry creation. Ordinary cron may check availability read-only and notify; it must never create a server. Retired migration monitors do not resume or replay, and old protected records are not automatically deleted.
