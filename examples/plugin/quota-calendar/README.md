# Quota Calendar plugin

This plugin exposes a live iCalendar feed containing the current per-model quota reset times.

## Build

From the repository root:

```bash
cd examples/plugin/quota-calendar/go
go build -buildmode=c-shared -o quota-calendar.so .
rm -f quota-calendar.h
```

The resulting `quota-calendar.so` belongs in the configured plugin directory. Enable it with:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    quota-calendar:
      enabled: true
      priority: 1
```

## Feed URL

```text
http://127.0.0.1:8080/v0/resource/plugins/quota-calendar/calendar.ics
```

The resource is intentionally unauthenticated by the plugin host's resource contract. Protect it with a reverse proxy or a private bind address if the feed contains sensitive model/account information. For a calendar client that cannot send custom auth headers, use an unguessable private subscription URL at the reverse proxy layer rather than placing credentials in the ICS content.

The feed is generated on every request. It uses a stable `UID` per provider/model pair, chooses the latest reset seen across matching auth entries, omits expired or missing reset times, and emits at most one `VEVENT` per provider/model pair. Calendar subscribers therefore update the existing event when the reset time changes instead of accumulating duplicates.

The response is `text/calendar`, uses deterministic CRLF/folding and source-derived `DTSTAMP`/`LAST-MODIFIED` values, sets `Cache-Control: no-store`, and advertises a 15-minute refresh interval. Calendar clients may still refresh less frequently according to their own policies.

## Core SDK requirement

Current CLIProxyAPI plugins can read auth-level runtime state, but per-model quota state is not exposed through the plugin callback contract. This plugin therefore requires the accompanying host callback change that adds `model_states` to `HostAuthFileEntry`.

Only non-secret runtime fields are exposed: model status, availability, reset/retry times, quota flag/reason, and update time. Access tokens and credential JSON are never read.
