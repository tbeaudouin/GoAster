# Automatic skill refresh final review - 2026-11-20

Verdict: pass for the reviewed offline scope after fixing one completed-output preservation blocker. Routine changes to selected instructions load automatically and preserve the conversation without a clear or manual accept step. This verdict does not establish exactly-once execution or durable tool-output recovery across crashes.

Scope: read root agents.md and Terra's docs/automatic-skill-refresh-review-20261120.md, searched the repository for case variants of CLAUDE.md (none found), and reviewed the complete uncommitted diff in internal/chat/openai.go, provider.go, skill_tool.go, skill_regression_test.go, and tools_test.go, together with surrounding session, context-budget, selection, and failure handling. Also reviewed the validation document. No live provider, network service, GoAster runtime, secrets, GitHub, issues, commit, or push was used. HTTP tests use local fake servers and module fetching was disabled.

The blocker was in replaceRestoredSkillItems: it treated every call ID beginning with call_goaster_restored_skill_ as app-supplied guidance. A completed action with a provider-generated ID using that prefix lost both its call and result during refresh. A regression reproduced the loss before the fix. Restored guidance now carries an unexported boolean provenance marker in codexInputItem, set only when constructing the app's snapshot. The marker is absent from JSON. Refresh removes only marked items, replaces old model-requested load outputs with a reference to current guidance, and retains completed action pairs even when their IDs use that prefix. The integration regression also repeats a discarded pending call ID and verifies rejection with no second tool execution. The helper regression checks removal of an old snapshot and replacement of a model load output sharing the prefix.

The inspected invariants hold within this scope:

- Current validated AUTH.md, META.md, and SKILL.md content replaces the selected snapshot and its digest. Old load outputs do not compete with current AUTH guidance after refresh. Current authentication, legacy safety, and approval requirements remain applicable; neither a digest checkpoint nor retained conversation approval grants new authority. Whether earlier approval satisfies changed requirements remains a model decision under the current guidance.
- Completed call/output pairs survive mid-batch refresh in the current provider request. Pending calls from the interrupted batch are omitted from the continuation, and their IDs are rejected if repeated within that request. Refresh itself does not rerun completed actions. This is not a general duplicate-action prevention mechanism for fresh model calls or later turns.
- Refresh checks occur before actions, before subsequent model requests, and before accepting a tool-free answer. Refresh churn is bounded at three accepted changes per provider request. The latest digest is checkpointed before the limit response; no further action is executed after the limit is detected. Missing, malformed, over-budget, or unsaveable guidance stops continuation through existing recovery paths with a safe action-review notice.
- Conversation and summary checkpoint behavior is preserved. Refresh structures and the new marker add no pointer types or pointer fields; existing Session receivers and test/HTTP/JSON pointer APIs are reused. No new storage subsystem, approval mechanism, or retry path was added.

The existing durability limit remains: completed tool outputs live in the current provider request and are not individually checkpointed across crashes or a turn stopped by churn. A successfully saved churn reply retains the warning that actions may have completed and their results must be checked before repetition. This warning is retained unchanged. A crash before that save cannot guarantee its durability. Existing history bounds and compaction still apply. File changes between validation and action remain possible; these checks are not an atomic filesystem/action transaction.

Final-review changes are limited to the provenance marker and refresh filter, the two related regressions, this verdict, and documentation corrections. The earlier validation document now limits nonreplay claims to the actual request scope and records the durability warning. Existing review and validation titles use a plain hyphen.

Validation after the code fix:

- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./... -count=1 -timeout 300s` passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test -race ./internal/chat -run 'Skill|CodexProviderRegistersAndExecutesSkillTool' -count=1 -timeout 300s` passed.
- `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go vet ./...` passed.
- `git diff --check` passed.

The supplied parent-run history reports an initial full-suite attempt exiting 1 with truncated failure output, then a successful full-suite rerun. That initial failure remains unexplained; this review does not claim every run passed. The separate targeted failure during this review was intentional regression reproduction of the completed-output bug described above.
