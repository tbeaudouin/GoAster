# GoAster self-debug

To debug yourself, read your recent systemd logs:

```sh
journalctl -u goaster-agent.service -n 100 --no-pager
```

After checking your systemd logs, inspect your runtime source code in the user’s GoAster fork (see the [goaster-fork skill](../goaster-fork/SKILL.md)) to understand where the bug may come from.

Keep secrets and private content out of reports.
