# Import memory validation — 2026-11-19

Added `goaster-import-memory` as a bundled starter skill. Its original user-supplied workflow and eight steps are preserved verbatim; short safety clarifications follow them. Runtime skill compatibility guidance reflects the current artefact loader: valid matching metadata, at most 100 skills, regular nonempty UTF-8 files, and the existing file size limits. No new artefact schema was introduced.

Updated the starter asset allowlist and its exact expected list. The export skill's supplied workflow remains unchanged. The earlier export validation record now notes this separate import-skill delivery.

Scope is limited to the new skill, starter packaging inventory, and validation reports. No runtime code was changed. No deployment or import execution occurred.

Parent validation passed: `bash packaging/test-skill-assets.sh`, `/usr/local/go/bin/go test ./internal/artefacts -run 'Starter|Discover' -count=1`, original attachment byte-for-byte prefix check, and `git diff --cached --check`. These are offline packaging/discovery checks, not a destructive import test.

The real replacement workflow was not exercised. In particular, this validation does not establish successful ZIP compatibility across real installations or successful full artefacts replacement; those remain untested.
