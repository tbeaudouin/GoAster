# Authentication

A local browser does not require Browser Use account authentication. Websites may separately require an authorized session or supported secret-safe login input.

Browser Use Cloud requires authentication: the CLI supports a Browser Use sign-in or `BROWSER_USE_API_KEY`. Consult https://docs.browser-use.com/open-source/browser-use-cli.md for current options and verify the installed CLI's help. Use the local /env flow for API keys; never request or expose keys, passwords, OTPs, or session credentials in chat, command arguments, logs, or recordings. Do not initiate cloud login or use without the separate approval required by the skill. Authentication does not authorize consequential actions.
