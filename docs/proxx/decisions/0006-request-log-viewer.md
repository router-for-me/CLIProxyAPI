# 0006: Request log viewer API and retention

Date: 2026-11-16. Status: accepted.

## Context

With `observability.logs.request-log: true`, the request logger (`internal/logging/request_logger*.go`)
writes one text file per request into the log directory:
`[error-]<sanitized-path>-<YYYY-MM-DDTHHMMSS>[_n]-<short-id>.log`, with the sections
`REQUEST INFO`, `HEADERS`, `REQUEST BODY`, optional `WEBSOCKET TIMELINE` / `API WEBSOCKET TIMELINE`,
`API REQUEST n`, `API ERROR RESPONSE`, `API RESPONSE n` and `RESPONSE`. Upstream lets you download
a single file by id, and that's all. Nothing bounds the total size.

## Decision

- New package `internal/proxx/requestlogs`. It indexes the directory, parses the files, joins them
  with proxx usage records, enforces retention and notifies live-tail subscribers. Endpoints live under
  `/v8/management/proxx/requests` (management auth).
- The files on disk remain the only source of truth. proxx never writes them and never changes their format.
- Retention: `proxx.request-logs.max-total-size-mb`. Unset means 10240 (10 GB) and 0 means unlimited.
  Only request log files count toward the cap and only they get deleted: the name must match the pattern
  above and the first line must be `=== REQUEST INFO ===`. `main.log`, rotated app logs and `*.tmp`
  spool files are never touched.
- No new upstream hooks. The lifecycle runs through the existing facade `internal/proxx/proxx.go`
  (`Start` / `Reload` / `Shutdown`).
- Enabling or disabling request logging uses the existing v8 config endpoint:
  `PUT /v8/management/config/observability/logs/request-log` with a raw JSON body `true` or `false`
  (`GET` reads it). The endpoint is not duplicated here. The list response reports the effective state as
  `request_log_enabled`.
- The cap is changed the same way: `PUT /v8/management/config/proxx/request-logs/max-total-size-mb`
  with a raw integer body. An explicit `0` survives later v8 config writes, and the e2e checks this.

## API (JSON keys snake_case)

| Method | Path | Result |
|---|---|---|
| GET | `/v8/management/proxx/requests` | `{items:[Row], next_cursor, models, providers, request_log_enabled, total_size_bytes, max_total_size_bytes (0 = unlimited), log_dir}` |
| GET | `/v8/management/proxx/requests/:id` | `{row, client, attempts, response, websocket_timeline, api_websocket_timeline, raw_size_bytes}` |
| GET | `/v8/management/proxx/requests/:id/raw` | the file as `text/plain` |
| DELETE | `/v8/management/proxx/requests/:id` | `{removed: 1}` |
| DELETE | `/v8/management/proxx/requests` | `{removed: n}` (request log files only) |
| GET | `/v8/management/proxx/requests/stream` | SSE: `event: request` + Row JSON per newly completed file; `: ping` every 15s |

List query parameters:
- `limit`: default 50, max 500.
- `cursor`: opaque.
- `status`: `2xx`, `4xx`, `5xx` or `error`. Any other value returns 400.
- `model`: case-insensitive match on the requested model, the served model or the model group.
- `provider`: case-insensitive match.
- `q`: case-insensitive substring search over the whole file.
- `since` / `until`: unix milliseconds, inclusive.

`models` / `providers` (first page only, i.e. no `cursor`): the sorted distinct models (group,
requested, served) and providers of every request log in the `since`/`until` window, ignoring the
other filters and paging. The viewer's filter dropdowns use them, so a model that was only logged
further down the list (e.g. one Jev call among thousands of chat requests) can be picked without
scrolling to it first. Later pages return them as null.

Results are newest first.

Row fields: `id, file, time (RFC3339), time_ms, method, path, status, duration_ms|null, stream,
requested_model, served_model, group, provider, input_tokens|null, output_tokens|null,
reasoning_tokens|null, cached_tokens|null, cost_usd|null, cost_source, attempts, error, size_bytes`.

Field semantics (interpretations the panel relies on):
- `id` is the 8-character short request id from the file name. That is the last 8 characters of the
  gin request id; the full id appears in the `X-Cpa-Trace-Id` response header. `/:id` also accepts the
  full request id or the exact file name. If several files share an id, the newest wins.
- `time` and `time_ms` come from the `REQUEST INFO` `Timestamp` (it has a zone offset), not from the
  file name, which is local time with no zone.
- `status` is the client-facing status from the `RESPONSE` section. It is `0` when the file has no
  status: websocket transcripts, or a streaming response whose status was never written.
- `error` is true for `error-` files or `status >= 400`. `status=error` filters on this flag.
- `duration_ms` is the file mtime minus the request timestamp. The logger writes the file when the
  request finishes, so this is wall-clock request time, including the time to stream the response.
- `requested_model` is the top-level `model` in the request body. Gemini paths
  (`/models/<model>:...`) are the fallback. It is empty for websocket transcripts, which have no
  request body.
- `served_model`:
  1. `X-Proxx-Served-Model` (the model-group member's client-visible name), else
  2. the usage record's served model (the upstream model ID, e.g. `up-ok` for alias `model-ok`).
- `group` is the requested model when the response carries `X-Proxx-Served-Model` or the requested
  model is a configured group.
- `provider` is the usage record provider. The fallback is `provider=` from the last attempt's
  `Auth:` line. Both use the same key, e.g. `openai-compatible-<name>` for openai-compatibility entries.
- Tokens and cost come from the usage records of all attempts of the request, summed, with the
  usagestats token semantics: input includes cache reads/writes and output includes reasoning.
  - `cost_source` comes from the last successful record, or `unknown` when records exist but none is priced.
  - With no usage record (the request was logged before the last restart, or the record was evicted),
    tokens fall back to the `usage` reported in the client response body: OpenAI chat/responses,
    Claude, Gemini `usageMetadata`. Cost is then null and `cost_source` is empty.
  - A failed request (`error: true`) whose usage records report no tokens at all is treated the same way:
    tokens come only from the response body (usually null), cost is null and `cost_source` is empty, so the
    panel shows "—" instead of "0 / 0" and "$0" (2026-11-16).
- `attempts` is the number of `API REQUEST n` sections, else the number of usage records.

Detail:
- Every body field is capped at 2 MB, and its `*_truncated` flag is set when cut. The raw endpoint
  has everything.
- `attempts` pairs `API REQUEST n` with `API RESPONSE n`.
  - The unnumbered `API RESPONSE` written on the error path is used only when no numbered response exists.
  - `API ERROR RESPONSE` entries fill attempts without a response, in order.
  - Each attempt also has `auth`: the masked `Auth:` line. This is a field added beyond the contract.
- `response.sse_events` and `response.assembled` are set only for `text/event-stream` responses;
  both are null otherwise.
  - Events are capped at 5000, or 2 MB of data.
  - `assembled` reconstructs text, reasoning and tool calls for OpenAI chat
    (`choices[].delta`), OpenAI Responses (`response.*` events), Claude (`content_block_*`) and Gemini
    (`candidates[].content.parts`, including the gemini-cli `response` wrapper).
  - `assembled` is null if no event matches a known format.
- Secrets are returned exactly as masked in the file. No decompression is attempted: the logged
  bodies are already plain text even when a header says `Content-Encoding: gzip`.

## Implementation notes

- Log directory: resolved once at start the same way `defaultRequestLoggerFactory` does it:
  `logging.ResolveLogDirectory(cfg)`, joined with the config file's directory when relative.
  Without `WRITABLE_PATH` and with no writable `./logs`, that is `<auth-dir>/logs`. The management
  handler's own `logDirectory()` skips the join, so it is not reused.
- Effective `request_log_enabled` = `request-log && !commercial-mode && !home.enabled`. In home mode
  the logs are forwarded instead of written.
- Index:
  - The index maps file name to the parsed metadata, cached by size and mtime.
  - A refresh lists the directory by name and stats only files that are new or not yet complete,
    because complete files never change.
  - The running total is summed from the index.
  - Files up to 16 MB are parsed fully once. Larger files are parsed from their first 16 MB plus the
    `RESPONSE` section found in the last 1 MB.
- Parser:
  - It reads lines with `bufio.Reader` and has no line length limit; single-line bodies are capped
    while reading.
  - Section headers are recognized only as exact known names, at file start or after a blank line, in
    the logger's write order, and never inside the final `RESPONSE` section. A response body with
    header-like lines therefore does not split the file.
  - The final `RESPONSE` section has two layouts, and both are parsed (old files stay on disk):
    - legacy (up to upstream v8.0.73): `Status: N`, header lines, a blank line, the body;
    - v2 (upstream v8.0.79+): `Status: N`, optional `Error: <diagnostic>` lines, a blank line,
      `Headers:`, header lines or `<none>`, a blank line, `Body:`, the body. A websocket transcript
      may have a section with only `Error:` lines.
    - v2 is recognized when the first blank line comes before any header line and the next line is
      exactly `Headers:`. v2 `Error:` lines (local read, dial and stream errors that upstream no longer
      writes into `API RESPONSE n`) are returned as `response.errors`, omitted when empty.
- Usage join:
  - The named usage plugin `proxx-request-logs` keeps the last 20000 requests in memory, keyed by
    the full `record.TraceID`.
  - `usage.Manager.Publish` sets `TraceID` from `logging.GetRequestID(ctx)`, the gin request id that
    also names the file. No hook was needed.
  - A file is matched by the full id at the end of its `X-Cpa-Trace-Id` response header. Without that
    header (websocket transcripts), it falls back to the short id from the file name, using the latest
    request with that short id. Keying by the 32-bit short id alone would merge colliding requests.
  - The join happens when a row is built, never in the parse cache, so records that arrive after the
    file are still picked up.
- Live tail:
  - The directory is polled every second, only while at least one stream is open.
  - fsnotify was not used: its kqueue backend (macOS, BSD) opens one descriptor per file in a watched
    directory, which a 10 GB log directory would exhaust.
  - Both writers create the `.log` with `O_EXCL` and then write it, so a file counts as complete only
    once it is non-empty, starts with `REQUEST INFO`, and has not been modified for 1 second. Only then
    is it emitted.
  - Files already present when the index is first built are never announced. The subscriber buffer
    holds 64 rows; rows beyond that are dropped for a slow client.
  - The handler flushes after every event and ping and sets no deadlines (AGENTS.md timeout rule).
- Retention runs:
  - at start and after every config reload (via the facade);
  - every 60s;
  - on every refresh (list, detail, stream poll) whenever the total exceeds the cap.

  It deletes the oldest complete files by mtime first. Files still being written are skipped and
  removed on a later pass.
- Paging: keyset cursor on (`time_ms`, file name), encoded as base64url, so new files arriving between
  page loads never shift later pages. `q` runs the cheap filters first, then scans files newest first
  in 1 MB chunks (with a `len(q)-1` overlap) and stops after `limit + 1` matches.

## Limitations

- Joined usage (provider, served upstream model, tokens summed over attempts, cost) is in memory only.
  After a restart, older rows fall back to file-derived fields, and their cost is null.
- `q` scans file contents, so on a large directory a search with a rare term reads many files.
- Websocket transcripts have no request body and at most a diagnostics-only `RESPONSE`, so `requested_model` is empty and
  `status` is 0. The timelines are returned raw.
- The unnumbered error-path `API RESPONSE` is dropped when numbered responses exist (it duplicates them).
- Upstream `Error:` lines that follow a response body inside an `API RESPONSE n` section stay in
  `response_body`.
- A file whose name matches the pattern and that starts with `=== REQUEST INFO ===` is treated as a
  request log. Never put other files with such names in the log directory.
- The upstream `logs-max-total-size-mb` cleaner (app logs) can also delete request logs in the same
  directory. The two caps are independent.

## Tests

- Unit tests (`go test -race ./internal/proxx/requestlogs/`) use fixtures copied from real logs
  written by the binary: OpenAI non-stream, OpenAI stream, model-group failover (429 → 200) and a 400
  `error-` file. They also use synthesized Claude, Gemini and Responses streams. Coverage:
  - parsing and assembly, header-like lines in bodies, and 2 MB truncation;
  - keyset paging stability, every filter, and the usage join (including short-id collisions);
  - a websocket transcript (timelines, status 0, attempts);
  - retention: oldest first, app logs untouched;
  - stream completion with an injected clock, no sleeps;
  - delete and delete-all, and path traversal.
- E2E: `PROXX_E2E=1 go test ./test/proxx/requestlogs_e2e/ -run E2E -v -count=1`. It builds the real
  binary and runs it with `--local-model` against an in-process mock OpenAI-compatible upstream, with
  no provider keys. It sends non-stream, stream, `auto` failover (429 → member 2) and 400 requests,
  then checks:
  - `log_dir`;
  - the rows, including joined tokens on the failover row;
  - filters, `q` and paging;
  - detail sections and the assembled stream text;
  - raw;
  - that the live SSE stream delivers rows (including one for a request sent while it is open);
  - toggling `request-log` through the v8 config endpoint;
  - retention after a hot reload to `max-total-size-mb: 1`, which keeps the newest files;
  - setting the cap to `0` through the v8 config endpoint, and that the value survives another v8 write;
  - delete and delete-all.
