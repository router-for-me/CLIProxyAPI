# Live account and quota watcher

This optional terminal companion prints one line for each completed CLIProxyAPI API request. It shows the provider, model, selected account, token count, and the most recently observed quota percentages. It does not alter Claude Code or Codex responses.

Enable `observability.usage.usage-statistics-enabled: true` in the v8 server configuration. Then run:

```sh
python3 examples/usage-watch/watch.py --key-file /path/to/management-key
```

Set `--base-url` if the server is not at `http://127.0.0.1:8317`. Use `--provider claude` or `--provider codex` to filter output. `--once` prints currently queued records and exits.

The watcher **pops** usage records from the management queue, including records hidden by `--provider`. Do not run it alongside another consumer that needs the same queue. Start it in a separate terminal before opening Claude Code or Codex. A coding-agent turn can make multiple API requests, so the output describes requests, not whole prompts. An account-change notice compares consecutive observed requests for the same provider. Quota numbers are observations from provider response headers; they may be absent or stale and are not a guaranteed number of prompts remaining. Keep the management key private.

If a credential has no email, the watcher displays its opaque credential index (or `unknown account`). It never uses the queue's `source` field as a label because that field can contain an API key. Credential metadata is fetched before the destructive queue read; when that lookup fails, the queue is left untouched.
