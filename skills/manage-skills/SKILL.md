# Manage skills

Create, install, or update local GoAster skill instructions when requested; the request authorizes the scoped local installation. Do not stop at a draft or ask for another approval. Installation does not authorize executing the skill's sensitive or consequential actions. Preserve unrelated work and honor actual tool access and existing security requirements.

Write the three aligned files under `~/.goaster/artefacts/skills/<name>/`, using a lowercase letters, digits, and hyphens name. Use private directories and files, and avoid symlinks:

- `SKILL.md`: concise, direct workflow and safety instructions.
- `AUTH.md`: authentication guidance; use `No authentication required.` when appropriate.
- `META.md`: only `name: <name>` and a single-line `description: <summary>`.

Validate the files and metadata with the file tools, then use `load_skill` to verify the installed instructions. Discovery reads metadata from disk at the next chat turn; loading reads the current workflow and authentication guidance. No rebuild or restart is required. Report any access or validation failure.

Before creating a skill, suggest that the user may optionally search [goaster-find-contributions](../goaster-find-contributions/SKILL.md) for shared skills. Search only if chosen; otherwise proceed without a separate approval or research round. In a coordinated source task, skill and documentation edits that accompany the source change remain covered by its single approval; skip research by default.

A local skill request does not require source evolution, a fork, GitHub operations, a server snapshot, binary build or deployment, or restart. A genuine Go code or binary change uses the separate source workflow and does not block an unrelated local skill installation.
