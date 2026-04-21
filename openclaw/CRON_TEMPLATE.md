# OpenClaw Cron Template

Use this pattern to attach a lightweight heartbeat cron for CLIProxyAPI.

## Example

```bash
openclaw cron add   --name cliproxyapi.heartbeat   --agent main   --every 10m   --session isolated   --thinking off   --timeout-seconds 180   --tools exec   --no-deliver   --message 'Run: CLI_PROXY_NOTIFY=0 bash /path/to/CLIProxyAPI/scripts/heartbeat_example.sh . If healthy, return exactly HEARTBEAT_OK. Otherwise return stdout exactly.'
```

Then add failure alerts:

```bash
openclaw cron edit <JOB_ID>   --failure-alert   --failure-alert-after 1   --failure-alert-channel telegram   --failure-alert-to <YOUR_OBS_CHAT_ID>
```

## Notes

- Keep healthy output quiet: `HEARTBEAT_OK`
- Only notify on failure / recovery / needs-attention
- Prefer isolated session target
- Keep host-specific paths outside the template when possible
