# Chat stream lifecycle review — 2026-11-17

Verdict: acceptable with the fixes below. Offline tests pass; live delivery and network behavior were outside this review.

## Resolved findings

- Stream write failures previously allowed the session to continue. A child context is now canceled on the first encode or flush failure, the initial notice is checked before starting a turn, and later stream events are suppressed. The provider's existing observer-before-action cancellation check prevents the next tool invocation. The existing real-provider fixture exercises cancellation before first and subsequent actions.
- The proposed diagnostic collapsed encode and flush failures under one operation. The original `agent.chat.stream_encode` and `agent.chat.stream_flush` names are retained, along with bounded event and phase metadata and the wrapped transport cause. A returned cancellation error is joined to the stream failure, preserving already-available safe provider status and phase in the diagnostic. The session's normal cancellation logging suppression avoids a duplicate cancellation record.
- The proposed test exposed `ObserveToolCall` only for a fake session and introduced pointer signatures for a migration transport. The export and transport were removed; existing provider and lost-response fixtures verify these paths. No new pointer declarations were introduced.
- A shadowed named `result` would make the deferred lifecycle record report the zero value after successful decoding. Assignment now updates the named return. Early explicit returns also assign the named return before the defer runs. Handoff failures carry the handoff stage.
- A dispatch record cannot establish remote receipt. The journal records entry, the point immediately before the HTTP client invocation, and return; a transport error still leaves creation unknown. Handoff delivery is reported separately and a created result without confirmed delivery remains review-required. Records use fixed, sanitized metadata and do not include secrets or request/response content.

The change adds neither automatic replay nor persistence machinery. Validation details and limitations are in [the validation report](chat-stream-lifecycle-validation-20261117.md).
