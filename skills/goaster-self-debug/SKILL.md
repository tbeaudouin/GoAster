# GoAster self-debug

To debug yourself, read your recent systemd logs:

```sh
journalctl -u goaster-agent.service -n 100 --no-pager
```

Keep secrets and private content out of reports.
