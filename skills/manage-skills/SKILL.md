# Manage skills

Each skill requires three files:

- `SKILL.md` contains the main instructions.
- `AUTH.md` contains authentication instructions. If no third-party service is needed, write `No authentication required.`
- `META.md` contains a short summary loaded at the start of every conversation to help the agent decide whether to load the full skill for the user's task.

When creating or modifying a skill, keep all three files aligned. Use simple, direct, concise instructions like the default skills.

Before creating a new skill, suggest to the user that they may optionally
search [goaster-find-contributions](../goaster-find-contributions/SKILL.md) for
shared GoAster skills that may help. Search only if the user chooses it; the
user may skip it. In the coordinated source workflow, include this optional choice in the single approval and skip research by default; adoption for the approved task needs no further prompt.

Do not list every edge case, fallback, or constraint. Trust the model to apply its own safeguards when using the skill.
