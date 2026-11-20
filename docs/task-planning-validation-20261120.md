# Task-planning default skill validation

## Scope

Add a concise `task-planning` default skill for user-requested task, project, or implementation plans. Load `project-memory`, save or update `PLAN.md` alongside related notes, avoid duplicate plans, report the saved path, and distinguish planning from implementation approval. No runtime code changes.

## Results

- Offline `go test ./internal/artefacts -run '^(TestTaskPlanningDefaultSkill|TestDefaultFilesystemSkillContracts)$' -count=1`: PASS. Covers starter installation, discovery, loading, guidance, and existing filesystem contracts.
- Offline `go vet ./internal/artefacts`: PASS.
- Shell syntax and staged whitespace checks: PASS.
- Independent `assets.sh` tree validation and `install_starters_to_prefix` copy with byte comparisons: PASS for all 30 default skills.
- `packaging/test-skill-assets.sh`: blocked by unchanged existing `hetzner-auto-backup` wording assertion for `GET /servers/{id}/actions/{action_id}`. Not a full packaging-harness pass.

Go commands used `/usr/local/go/bin/go` with `GOTOOLCHAIN=local GOPROXY=off GOSUMDB=off`. The initial system Go invocation could not verify its selected toolchain offline; the installed local toolchain resolved this.

## Remaining gaps

No live model planning scenario, deployment, runtime upgrade, or installed-skill reconciliation performed. Full Go suite was not rerun for this skill-only addition. Existing installed skills remain unchanged.
