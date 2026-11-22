# GitHub authentication

Check authentication on the user's host:

```sh
gh auth status --hostname github.com
gh api --hostname github.com user --jq '.login'
```

Use the authenticated API login as the account; if the user named an account, verify it matches. Do not infer identity from a Git remote, environment variable, or repository name. Missing configured credentials are a blocker; do not ask for secrets in chat.

## Log in

If authentication is needed, start the private, allowlisted `github-login`
profile using `background_command`:

```json
{"action":"start","name":"github-login"}
```

The profile runs this fixed command in an interactive terminal (PTY), without
model-supplied arguments:

```sh
gh auth login --hostname github.com --git-protocol https --web
```

Use the session ID returned by `start` for subsequent `background_command`
calls. The profile's backward-compatible `keys_only` setting disables `input`
and `write` but does not restrict `send-keys`. Every eligible PTY accepts every
named terminal key already supported by `send-keys`, including arrows, tab,
space, backspace, enter/return, escape, function keys, and the existing
control/modifier forms. It also accepts one ordinary printable character per
comma-delimited token, so answer a Git credential prompt with a single `y` or
`n` when needed:

```json
{"action":"send-keys","id":"<session-id>","keys":"y"}
```

Use named `enter` to submit a prompt or open the browser:

```json
{"action":"send-keys","id":"<session-id>","keys":"enter"}
```

To cancel, send `ctrl-c` to the same session:

```json
{"action":"send-keys","id":"<session-id>","keys":"ctrl-c"}
```

Do not use `input` or `write`, paste text, or enter passwords, tokens, codes,
credentials, or any other secrets. A comma separates key tokens; use named
`comma` for a literal comma. Read the terminal output and share GitHub's
official verification URL and one-time code with the user, who completes
verification in their browser. Leave the session running while they do so.

Afterward, check the session with `status` and read `output` only as needed.
Verify the host and account using the commands above. Stop any still-running
or canceled session and verify its terminal state.
