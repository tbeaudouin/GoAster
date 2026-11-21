# Interrupted conversation progress evidence

Interactive turns checkpoint the exact current user history text before provider work, then record tool starts and completions and a neutral interruption on failure. Attachment-only turns use the existing attachment history placeholder. Invalid UTF-8 or oversized current text is rejected before persistence or provider work; later budget bounds preserve that text or stop the turn. Current-turn guidance is reserved with the user request and latest event; if it cannot fit, checkpointing fails before the next action. Prior-turn guidance and older history can be trimmed with a notice. Scheduled turns retain their prior persistence and retry behavior; migration diagnostics and skill guidance retain their existing checkpoint safeguards.

Restart evidence is bounded status, not a full transcript. Tool arguments and arbitrary outputs are omitted. Recognized shell and file results contribute fixed status labels and an allowlisted exit code; paths, branch names, compiler identifiers, and other variable output are not retained. Completion records handler return and does not by itself establish success. An unmatched start denotes uncertainty. Later history trimming can remove older evidence.

Offline regressions cover handler deadline and cancellation errors containing private URL queries and joined causes across terminal messages, agent messages, diagnostics, and unmatched on-disk starts, as well as real Codex loops with guidance retained across token and message-slot pressure before later actions and after restart, oversized current and prior guidance, adversarial Git filenames and forged exit-status lines, Codex action ordering, save failure gates and restart, realistic shell and file result shapes and redaction, interactive size and budget boundaries, summary fallback, and a scheduled canceled-summary retry with unchanged history and disk before retry. A failed checkpoint stops subsequent actions; a directory-sync failure after replacement may leave the attempted checkpoint on disk while memory retains the last acknowledged state. No live backend or external action was exercised. The parent independently reproduced the same three unrelated starter/memory expectation failures on unchanged main `6a7bac66e395244f28573665bc899f37d6b53036`: `TestAgentPromptUsesRuntimeSkillsAndMemorySetup`, `TestFeatureInspirationPolicy`, and `TestInstallBundledStartersOnce`. The final full suite fails only those same three tests.

Validation: offline `go test ./internal/chat/... ./internal/diagnostics/... -count=1`, `go vet ./internal/chat/... ./internal/diagnostics/...`, and `git diff --check`.

## Final validation and remaining acceptance

All Go commands used `/usr/local/go/bin/go` with `GOPROXY=off GOSUMDB=off`.

- PASS: `go test ./internal/chat/... ./internal/diagnostics/... -count=1`.
- PASS: final `go test -race ./internal/chat ./internal/diagnostics ./internal/scheduler -count=1 -timeout=180s`.
- PASS: final `go vet ./...`, `go build ./...`, and `git diff --check`.
- BASELINE FAILURES ONLY: final `go test ./... -count=1 -timeout=180s`, with the three tests named above independently reproduced on unchanged main using a targeted `-run` expression.
- REVIEW: Terra final review and Sol final confirmation both SATISFIED after the timeout/cancellation privacy regression was fixed. Reviews were static; the parent ran the validation commands.
- NOT RUN: installed-agent upgrade, restart, real provider failure/restart acceptance, or historical incident reproduction. The exact historical screenshot failure remains unproven without the matching running-version journal.
- No GitHub issues, pull requests, server changes, or scheduled-job mutations were made for this work. Source delivery does not update an already-running agent.
