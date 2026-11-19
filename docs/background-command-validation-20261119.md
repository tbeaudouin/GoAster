# Background command error details

Invalid `background_command` requests return the existing `invalid_request` status with a fixed reason and actionable message. Parsing rejects malformed or non-object JSON, duplicate or unknown fields, non-string values including null, trailing data, oversized input, and missing action. Action validation retains the original accepted shapes: list permits empty name and id but rejects nonempty values and any data or keys; start permits an empty id but rejects a nonempty id and any data or keys; session actions permit an empty name but reject a nonempty name. Data and keys requirements remain action-specific.

Manager failures retain their existing statuses and return fixed guidance without raw error text. Unexpected failures are sent once to the existing bounded diagnostics with the action in the operation. The guidance for uncertain start, input, and session outcomes calls for inspecting state before further action. Typed manager errors identify disallowed names and invalid keys.

The regression tests cover invalid results and privacy, action validation and empty-field compatibility, manager diagnostics, and delivery of the actual tool result in the next request to a local fake Codex provider. The tests do not establish how a deployed model will respond to the guidance.

## Final independent verification

Parent reran the complete offline suite (`go test ./... -count=1 -timeout 300s`), `go vet ./...`, focused race tests (`go test -race ./internal/chat ./internal/command ./internal/diagnostics -count=1 -timeout 300s`) and `git diff --check`: all PASS. Environment: `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`, `/usr/local/go/bin/go`. Independent final Sol review after Terra corrections returned SATISFIED. No live agent upgrade, actual login, provider action or deployment was performed.
