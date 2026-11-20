# Task-planning default skill validation

## Scope

Add a concise `task-planning` default skill for user-requested plans. Load `project-memory`, save or update `PLAN-<task-name>.md` alongside project notes using a short lowercase hyphenated task name, reuse a plan only for the same task, report the saved path, and distinguish planning from implementation approval. No runtime code changes.

## Historical results (before named-plan correction)

- Offline `go test ./internal/artefacts -run '^(TestTaskPlanningDefaultSkill|TestDefaultFilesystemSkillContracts)$' -count=1`: PASS. Covers starter installation, discovery, loading, guidance, and existing filesystem contracts.
- Offline `go vet ./internal/artefacts`: PASS.
- Shell syntax and staged whitespace checks: PASS.
- Independent `assets.sh` tree validation and `install_starters_to_prefix` copy with byte comparisons: PASS for all 30 default skills.
- `packaging/test-skill-assets.sh`: blocked by unchanged existing `hetzner-auto-backup` wording assertion for `GET /servers/{id}/actions/{action_id}`. Not a full packaging-harness pass.

Go commands used `/usr/local/go/bin/go` with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`. The initial system Go invocation could not verify its selected toolchain offline; the installed local toolchain resolved this.

## Follow-up checks for named-plan correction

- PASS: `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off /usr/local/go/bin/go test ./internal/artefacts -run ^TestTaskPlanningDefaultSkill$ -count=1` checks the updated skill assertions.
- PASS: `git diff --check` for whitespace errors.
- Final read-only review: SATISFIED.
- The focused test checks the skill text through starter installation, discovery, and loading; no live model planning scenario is covered.

No deployment, runtime upgrade, installed-skill reconciliation, or full-suite rerun performed for this correction. Existing installed skills remain unchanged.
