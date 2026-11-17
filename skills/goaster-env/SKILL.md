---
name: goaster-env
description: Safely check and use locally protected environment variables.
---

Use this skill when another skill needs an environment variable or secret. Environment values can be supplied locally through `/env` or stored in Bitwarden Secrets Manager; use the user's configured source. Check whether the required value is available without printing or exposing it. For `HCLOUD_TOKEN`, read protected `<real user home>/.goaster/env` first and use process `HCLOUD_TOKEN` only as fallback. If protected settings cannot be read, fail closed; do not bypass them. Presence does not establish that a token authenticates successfully.

If `HCLOUD_TOKEN` is missing, explain the setup in the paired private Telegram conversation. Tell the user to send `/get-env` to download `goaster.env`, edit it locally to add `HCLOUD_TOKEN`, attach the edited file with `/env` in the same message, review the preview, and reply exactly `ACCEPT`. Never ask them to paste a secret in chat. After acceptance, recheck availability without displaying the value. For other missing variables, explain what is needed and stop. For Bitwarden, follow the `bitwarden-secrets-manager` skill.
