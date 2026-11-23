# Tool error diagnostics review and validation

Handler errors retain their underlying causes while diagnostics and model-facing recovery use fixed reason categories. Known tool names are allowlisted; arbitrary names and error text are not copied into those fields. Read-only recovery stops the rest of the batch and supplies a reference and debugging advice. Action errors, cancellation, storage and checkpoint failures stop without replay. Local configuration classifications retain their specific messages.

The file handlers normally turn argument and file failures into tool output rather than returning errors; this change categorizes typed errors that actually reach the handler-error boundary and does not refactor those handlers. An empty finalizer response is rejected by the existing parser and takes the safe model-error path. Model-generated explanations cannot be guaranteed never to repeat the deterministic notice. Live providers and Telegram were not exercised.

Offline validation using `/usr/local/go/bin/go`:

- `go test ./internal/chat ./internal/telegram ./internal/diagnostics`: passed.
- Focused handler, reason and sanitizer tests, including `-race`: passed. They cover typed reasons and redaction, registered and injected names, paired batch records and no later execution, finalizer outcomes, storage/checkpoint stops and recovery advice.
- `go vet ./...` and `go build ./...`: passed.
- `go test ./...`: failed on `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestTaskPlanningDefaultSkill`, `TestFeatureInspirationPolicy`, `TestInstallBundledStartersOnce`, and `TestBundledGitHubAuthDocumentsExact`. A full test run of an unchanged `git archive HEAD` reproduced the same five failures. The disposable baseline directory was removed.

Recovered handler diagnostic follow-up (offline, 2026-11-23):

- `GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/chat ./internal/telegram ./internal/diagnostics` — passed.
- `GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/chat -run 'TestHandler|TestToolCauseReason|TestToolDiagnosticNames' -count=1` — passed.
- `GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` — passed.
- `GOPROXY=off GOSUMDB=off /usr/local/go/bin/go build ./...` — passed.

The recovery regression covers both read-only handlers, complete diagnostic context, one handler failure record after successful finalization, and private-text exclusion.

Final acceptance: the read-only Sol review found the recovered-handler context gap, which was fixed and regression-tested. The final Sol re-review returned SATISFIED, including affected package tests, focused races and whitespace checks. Parent independently reran affected package tests, full vet/build and whitespace checks successfully on the final code. Source validation only; no running agent, migration, scheduled job or installed service was changed.
