# Skill selection recovery validation — 2026-11-20

Named pending selections are reloaded with complete current guidance and their digests checkpointed before the model or tools run. Existing history and summary remain intact. A legacy anonymous marker cannot identify the missing guidance, so the fixed recovery message asks the user to check completed actions, use `/clear`, and request the skills again. Invalid and excessive new requests do not create anonymous markers. Selection count no longer has a lifetime cap; per-request tool limits, skill bytes, context and serialized state limits remain. Guidance and its digest do not confer authorization; checkpoint failures still stop the session. Diagnostics use fixed skill stage and reason fields without names, paths, content or arguments.

Offline commands used Go 1.25.13 from the locally installed toolchain with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`:

- `go test ./internal/chat ./internal/telegram ./internal/diagnostics` — passed.
- `go test ./internal/chat -run 'TestCodexProviderRegistersAndExecutesSkillTool|TestLegacyAnonymousSelectionFailsWithSafeRecovery|TestSkillFailureDiagnosticsRedactPrivateDetails' -count=1` — passed.
- `go test ./...` — failed in unrelated starter content expectations: `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestFeatureInspirationPolicy`, and `TestInstallBundledStartersOnce`; the chat, Telegram and diagnostics packages passed.
- `go vet ./...` — passed.
- The system Go 1.22.2 could not run the module, which requires Go 1.25.

These are offline tests against source and local fixtures; no installed binary, deployed service, production state, or live provider was changed or validated. Legacy anonymous selections require `/clear` because their requested guidance is unknowable. The full suite is not green due to the starter-content failures above.
