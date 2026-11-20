# Skill selection recovery validation — 2026-11-20

Full loaded skill guidance is stored in ordered conversation history, passed to later turns, and included in normal summarization. Explicit loads append the current content; no selected-skill registry or automatic refresh is used. Legacy `selected_skills` values are ignored while the saved summary and messages remain available. Invalid or unavailable skills produce a normal tool error. Per-file, actual model-context, conversation, and storage size limits still apply; there is no runtime skill-count cap. Failed loads end the current turn through the normal diagnostic path but do not poison later turns.

Subsequent independent reviews and additional race/baseline evidence are recorded in `skill-context-review-20261120.md` and `skill-context-final-review-20261120.md`. Both reviews found the final implementation ready; the three full-suite failures were reproduced on unchanged main `a53599a`.

Offline validation used `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off` and `/root/go/pkg/mod/golang.org/toolchain@v0.0.1-go1.25.13.linux-amd64/bin/go`:

- `go test ./internal/chat -count=1 -timeout 300s` — passed. Coverage includes eleven distinct skill loads, restart with full guidance, explicit reload after a change, bad loads followed by another turn, and compaction with legacy pending markers.
- `go test ./... -count=1 -timeout 300s` — failed: `cmd/goaster-agent` `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, and `internal/artefacts` `TestFeatureInspirationPolicy` and `TestInstallBundledStartersOnce` fail on existing bundled starter content expectations. `internal/chat` and all other tested packages passed.
- `go vet ./...` — passed.

These are offline source and fixture checks; no runtime or deployed service was changed.
