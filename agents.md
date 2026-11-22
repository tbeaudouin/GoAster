# Repository conventions

- Avoid pointers at all cost.
- If introducing a pointer is unavoidable, obtain explicit user confirmation before introducing it.

# Strict top-level GoAster runtime-failure rule

This rule applies to every GoAster package and every unexpected runtime failure, including adjacent or similar failure paths. Make each failure easy to debug without weakening privacy or replay safety.

- At the appropriate owner boundary, preserve the complete wrapped/joined underlying cause and emit a clear, bounded `ERROR` diagnostic to stderr or the system journal. Include the safe operation and stage/phase, plus available safe model/backend, HTTP status, backend type/code, request ID, and transport cause. If a detail is absent or present but unrecognized, state that explicitly as `absent` or `unrecognized`; never invent context or pretend that authentication was established.
- The owner boundary owns the diagnostic: do not duplicate the same failure at lower layers. A retry history may record each failed attempt once, and must record the retry decision and attempt/total counts. A success after retry must still retain and log the earlier failure; success does not erase it.
- Retry only an explicitly classified transient failure before response output or side effects. A model request may be retried after an earlier tool succeeded: retrying that request does not execute the earlier tool again. Never replay a tool call, handoff, or other uncertain external action.
- Friendly messages must remain concise and safe, and may describe authentication only when a typed or otherwise explicit authentication outcome establishes it. Generic transport, server, malformed-response, formatting, storage, or unclassified failures must not be presented as authentication failures.
- Diagnostics are strictly bounded and redacted: never log secrets, credentials, tokens, authorization headers, request bodies, prompts, model responses, tool output, arbitrary provider bodies, private content, secret-bearing files, or credential-bearing URLs. Redact credential-shaped data and control/ANSI/newline injection; a desire for debugging detail never overrides these exclusions.
- When changing a failure path, add or update offline regression and redaction tests for the path and its surrounding/similar cases. Expected user cancellation and normal EOF may remain unlogged; every other runtime failure requires the `ERROR` diagnostic.
