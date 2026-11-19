# Source sync starter validation — 2026-11-19

Added `goaster-sync-source-changes-after-migration` as a bundled starter skill. The supplied `SKILL.md` is preserved byte-for-byte. `META.md` describes the post-migration use case, and `AUTH.md` contains only paired-conversation and GitHub authentication context; workflow approvals remain in the existing skills.

Offline validation covers the starter allowlist, required files and metadata, source workflow guardrail phrases, installed package contents, and local Markdown references. The workflow itself was not executed. No existing-agent installation or rollout was performed, so rollout to existing agents remains unverified.

Passed: bash packaging/test-skill-assets.sh; /usr/local/go/bin/go test ./internal/artefacts -run 'Starter|Discover' -count=1; supplied-file byte comparison; staged diff check.

Review caveat: full artefact import replaces installed skills. An older export may not contain this new skill, so default bundling does not guarantee availability after such an import. Do not silently merge starter skills or claim end-to-end restoration acceptance; separately confirm the installed workflow is available before using it.
