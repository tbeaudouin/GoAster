# Read/write temporary files

Load this skill before choosing a temporary path, managing temporary files, or
setting temporary-directory variables for a subprocess. Load any other skill
required by the task. Discovery metadata is only a hint; it does not enforce
these rules on `shell_execute` or other tools. If this skill cannot be loaded,
stop the temp-dependent workflow.

## Resolve the private root

Use `/tmp/goaster` for GoAster-managed temporary work. Before using `/tmp`,
verify with `lstat` that it is a real, trusted system directory with the sticky
bit (normally owned by root). Do not follow a symlink or change `/tmp`.

Create `/tmp/goaster` exclusively as the current identity under umask `077`.
Whether new or existing, it must be a real directory owned by the current
identity with mode `0700`. If it is occupied by a symlink, another file type,
another owner, or different permissions, stop and report the conflict. Do not
take it over, change its owner or mode, use `sudo`, or modify a parent
directory. Separate identities cannot share this root; a collision must fail
clearly.

The temporary root does not depend on `HOME`. Before changing `HOME`,
`USERPROFILE`, or another profile variable, save the verified real home and
exact credential paths. Never override `HOME` for an authenticated GoAster
operation. If an isolated child needs a profile override, apply it only to that
child and never copy credentials into the temporary area.

## Create and use one task directory

Create one fresh, unique child of `/tmp/goaster` per task, using an exclusive
`mktemp -d`-style operation under umask `077`. Verify its absolute path, real
directory type, current owner, and `0700` mode before use. Never use `/tmp`,
`/tmp/goaster`, the repository, or a shared directory as the task directory.

Point all inherited subprocess temporary-directory variables at that task
directory:

```sh
export TMPDIR="$TASK_DIR"
export TMP="$TASK_DIR"
export TEMP="$TASK_DIR"
```

Keep temporary contents non-secret and limited to the task. Never put
credentials, tokens, private keys, pairing material, control secrets, or auth
headers in files, filenames, arguments, logs, or child output. Keep paths
inside the task directory, reject symlinks and unexpected file types, and
create private files exclusively (normally mode `0600`). For a durable file,
use a private same-directory temporary file and atomic replacement only when
the owning workflow requires it; never replace an unrelated file. Use bounded
commands and do not start, stop, restart, or replace services as a side effect
of temporary-file handling.

A different unprivileged identity cannot traverse a `0700` task directory,
even though it is under `/tmp`. For a test that must run as another identity,
put only non-secret fixtures in a separate fresh directory directly under
trusted system `/tmp`, owned by or access-controlled for the approved test
identity; create it as that identity when possible. Give the child only the
fixture paths it needs. Setting that child's `TMPDIR`, `TMP`, and `TEMP` to the
fixture directory is the explicit identity-change exception. Do not loosen
permissions on `/tmp`, `/root`, `/tmp/goaster`, or authentication files.

GoAster per-turn attachment directories are runtime-managed and have their own
validation and cleanup contract. Follow that contract for runtime-supplied
attachment paths; do not remove a process temporary root or rewrite durable
GoAster state to fit this workflow.

## Clean up narrowly

Arrange cleanup when the task starts; run it on success, failure, or
interruption after child processes exit. Before removal, verify the exact
generated task path, current ownership, real directory type, private mode,
and that it remains below `/tmp/goaster`. Remove only that task directory,
never a wildcard or parent. Clean up a separate fixture only at its exact
generated path, after the child exits, using its owning or approved test
identity. Never remove `/tmp/goaster` or `/tmp`.

Preserve authentication and authorization files, including
`~/.goaster/agent-auth.json`, `~/.goaster/auth.json`, configured control
secrets, Telegram tokens, and pairing state. Do not inspect, copy, move,
overwrite, or delete them. If one is found in a task directory, stop and
preserve it for explicit recovery; report incomplete cleanup without printing
temporary contents or secret-bearing paths.
