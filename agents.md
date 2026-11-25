# Repository conventions

- Avoid pointers at all cost.
- If introducing a pointer is unavoidable, obtain explicit user confirmation before introducing it.

# Strict top-level GoAster runtime-failure rule

This rule applies to every GoAster package and every unexpected runtime failure, including adjacent or similar failure paths. Make each failure easy to debug without weakening privacy or replay safety.

- At the appropriate owner boundary, preserve the complete wrapped/joined underlying cause and emit a clear, bounded `ERROR` diagnostic to stderr or the system journal. Include the safe operation and stage/phase, plus available safe model/backend, HTTP status, backend type/code, request ID, and transport cause. If a detail is absent or present but unrecognized, state that explicitly as `absent` or `unrecognized`; never invent context or pretend that authentication was established.
- The owner boundary owns the diagnostic: do not duplicate the same failure at lower layers. A retry history may record each failed attempt once, and must record the retry decision and attempt/total counts. A success after retry must still retain and log the earlier failure; success does not erase it.
- Retry only an explicitly classified transient failure before response output or side effects. A model request may be retried after an earlier tool succeeded: retrying that request does not execute the earlier tool again. Never replay a tool call, handoff, or other uncertain external action in the same turn. The explicitly approved migration workflow may make its next normal scheduled create with the same exact parameters and existing exact-name guard after an uncertain outcome; never replay its handoff. All other uncertain external actions remain subject to the no-replay rule.
- Friendly messages must remain concise and safe, and may describe authentication only when a typed or otherwise explicit authentication outcome establishes it. Generic transport, server, malformed-response, formatting, storage, or unclassified failures must not be presented as authentication failures.
- Diagnostics are strictly bounded and redacted: never log secrets, credentials, tokens, authorization headers, request bodies, prompts, model responses, tool output, arbitrary provider bodies, private content, secret-bearing files, or credential-bearing URLs. Redact credential-shaped data and control/ANSI/newline injection; a desire for debugging detail never overrides these exclusions.
- When changing a failure path, add or update offline regression and redaction tests for the path and its surrounding/similar cases. Expected user cancellation and normal EOF may remain unlogged; every other runtime failure requires the `ERROR` diagnostic.

## Temporary OpenAI model HTTP error diagnostic exception

- Thomas explicitly authorizes logging the whole raw OpenAI model HTTP error response body temporarily for HTTP 400 debugging, only in the owner-boundary server stderr/journal diagnostic as one safely quoted field. Retain the 8 MiB response read limit and report empty, missing, failed, or oversized reads explicitly. This exception does not cover outgoing requests, authorization headers, configuration tokens, successful/model output bodies, SSE success streams, or unrelated private content; never expose the body in Telegram, API, or agent-facing errors. All other diagnostic restrictions remain in force.

# Scheduled tool parity

- Every new normal agent tool must also be available to genuine scheduled turns through the shared registry. Scheduled turns cannot use cron action `add`; `cron_disable_self` is a scheduled-only convenience. Add or update tests for this invariant. Existing authorization, ownership, and safety guards remain in force.

# Model message role invariant

- GoAster must never create or send system-role messages. Use developer-role messages for instructions. Reject system roles at the shared model request boundary, including summarization, retries, and tool continuations. Add regression tests for instruction construction and these paths.
