---
name: model-administration
description: Owner/operator workflow for changing configured OpenAI models and testing them through GoAster.
---

# GoAster model administration

Use this skill only when the owner or operator asks to add or change a configured OpenAI model. Do not expose model administration as a public chat command.

1. Read the actual instance's `~/.goaster/artefacts/config/openai-models.json` and save the complete previous runtime JSON for restoration. Do not use a compiled template or fallback.
2. Add the requested exact model ID and alias if requested. Keep all chat selections unchanged. Use the existing protected atomic workflow: validate the complete JSON, write a restricted same-directory temporary file, sync it, and atomically replace the runtime file.
3. Run `goaster-agent test-model EXACT_MODEL_ID` as the same service user with the same instance HOME and environment and its own agent-auth.json. Do not pass shell secrets or inherit unrelated credentials. This uses the normal provider and runtime configuration in a separate local process; it does not prove a live Telegram turn. The command must succeed with an exact sentinel match before retaining the change. The old prompt and test-prompt commands cannot verify a candidate model.
4. On failure, timeout, mismatch, unavailable model, or uncertainty, restore the complete previous runtime JSON using the same protected atomic workflow and report that the candidate was not retained. No restart is needed; runtime configuration is re-read.
