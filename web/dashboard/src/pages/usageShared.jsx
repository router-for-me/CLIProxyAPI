// usageShared.jsx — components and helpers shared between the Usage Stats page
// (events-only) and the Errors page (failed-attempt drill-down). Lifted out of
// UsageStatsPage.jsx when Errors was promoted from a tab into its own sidebar
// menu item so the two pages can render identical token/cost breakdowns, presets,
// filter selects, and detail rows without duplication.

// Preset time windows the dashboard offers at the top of the page. Each
// preset computes from/to in UTC using relative offsets from now.
export const PRESETS = [
  { label: 'Last 15m', duration: 15, unit: 'minute', interval: 'minute' },
  { label: 'Last 1h', duration: 1, unit: 'hour', interval: 'minute' },
  { label: 'Last 6h', duration: 6, unit: 'hour', interval: 'hour' },
  { label: 'Last 24h', duration: 24, unit: 'hour', interval: 'hour' },
  { label: 'Last 7d', duration: 7, unit: 'day', interval: 'day' },
  { label: 'Last 30d', duration: 30, unit: 'day', interval: 'day' },
];

export function presetToRange(preset) {
  const now = new Date();
  const from = new Date(now);
  if (preset.unit === 'minute') from.setMinutes(from.getMinutes() - preset.duration);
  if (preset.unit === 'hour') from.setHours(from.getHours() - preset.duration);
  if (preset.unit === 'day') from.setDate(from.getDate() - preset.duration);
  return { from: from.toISOString(), to: now.toISOString(), interval: preset.interval };
}

// Token breakdown segments rendered left-to-right in the stacked bar. Order
// is deliberate: input-side tokens (input / cached / cache creation) first,
// then output-side tokens (output / reasoning). Provider parsers normalize
// the persisted fields so each segment is non-overlapping:
//   - input_tokens          = billable non-cached prompt tokens (OpenAI/Gemini
//                            subtract cached from prompt_tokens at parse time)
//   - cached_tokens         = cache-read tokens (strictly; never cache-creation)
//   - cache_creation_tokens = cache-write tokens
// For post-fix rows the segment sum matches total_tokens exactly. Pre-fix
// historical rows may still drift (cached_tokens used to fall back to
// cache-creation for Anthropic); the "different by N" hint surfaces that.
//
// Each segment also carries its cost_breakdown field name so the cost bar
// can attribute dollars to the same slices. The mapping mirrors Go's
// SegmentCosts: cached_tokens → cached_read, cache_creation_tokens →
// cache_creation (cache-write surcharge).
export const TOKEN_SEGMENTS = [
  { key: 'input_tokens', costKey: 'input', label: 'Input', short: 'in', color: 'var(--accent)' },
  { key: 'cached_tokens', costKey: 'cached_read', label: 'Cached', short: 'cch', color: 'var(--success)' },
  { key: 'cache_creation_tokens', costKey: 'cache_creation', label: 'Cache write', short: 'ccw', color: '#38bdf8' },
  { key: 'output_tokens', costKey: 'output', label: 'Output', short: 'out', color: 'var(--warning)' },
  { key: 'reasoning_tokens', costKey: 'reasoning', label: 'Reasoning', short: 're', color: '#a78bfa' },
];

export function tokenSegments(e) {
  return TOKEN_SEGMENTS.map((s) => ({ ...s, value: Number(e[s.key] || 0) }));
}

export function costSegments(e) {
  const b = e.cost_breakdown || {};
  return TOKEN_SEGMENTS.map((s) => ({ ...s, value: Number(b[s.costKey] || 0) }));
}

// fmtUSD renders a per-segment dollar amount with enough precision that the
// operator can tell sub-cent slices apart. Zero is shown as "$0" so absent
// (no pricing row) and free both read cleanly.
export function fmtUSD(v) {
  if (!v || v <= 0) return '$0';
  if (v < 0.01) return `$${v.toFixed(6)}`;
  return `$${v.toFixed(4)}`;
}

// fmtUSDCompact renders a per-segment dollar amount with the minimum number
// of digits needed to stay readable inside the dense table cell. Always
// groups thousands-separated for legibility.
export function fmtUSDCompact(v) {
  if (!v || v <= 0) return '0';
  if (v < 0.01) return v.toFixed(6);
  return v.toFixed(4);
}

export function toUTC(localValue) {
  if (!localValue) return '';
  // datetime-local input produces "2026-07-19T15:30"; assume the user means
  // local time. Convert to ISO UTC. Prefer toUTCFromTZ when a timezone selector
  // is on the page — this helper assumes the browser's local zone and is kept
  // for the presentational UsageToolbar which has no selector of its own.
  const d = new Date(localValue);
  if (isNaN(d.getTime())) return '';
  return d.toISOString();
}

// Timezones offered in the Usage Stats / Errors toolbar. The list favors the
// regions an operator is likely to care about; "UTC" is always present so the
// prior behavior stays one click away. Default is Asia/Jakarta per product.
export const DEFAULT_TIMEZONE = 'Asia/Jakarta';
export const TZ_STORAGE = 'nixllm.dashboard.usageTimezone';
export const TIMEZONES = [
  'Asia/Jakarta',
  'UTC',
  'Asia/Singapore',
  'Asia/Tokyo',
  'Asia/Shanghai',
  'Asia/Kolkata',
  'Asia/Dubai',
  'Australia/Sydney',
  'America/Los_Angeles',
  'America/New_York',
  'America/Sao_Paulo',
  'Europe/London',
  'Europe/Berlin',
];

// supportedTimezones returns the set of IANA tz ids the runtime accepts. Used
// to validate a persisted/localStorage value before applying it, so a stale
// preference from an older runtime never crashes Intl.DateTimeFormat.
function supportedTimezones() {
  try {
    if (Array.isArray(Intl.supportedValuesOf)) {
      const v = Intl.supportedValuesOf('timeZone');
      if (v && v.length) return new Set(v);
    }
  } catch { /* older engines — fall through to the hardcoded list */ }
  return new Set(TIMEZONES);
}

// loadTimezone reads the operator's tz preference from localStorage, defaulting
// to Asia/Jakarta. Falls back to the default when the stored value is no longer
// supported by the current runtime (e.g. an obsolete id) so the UI never
// throws on a bad Intl time zone.
export function loadTimezone() {
  let stored;
  try { stored = localStorage.getItem(TZ_STORAGE) || ''; } catch { stored = ''; }
  const supported = supportedTimezones();
  if (stored && supported.has(stored)) return stored;
  if (supported.has(DEFAULT_TIMEZONE)) return DEFAULT_TIMEZONE;
  return 'UTC';
}

// saveTimezone persists the tz preference so it survives reloads and applies
// across the Usage Stats and Errors pages.
export function saveTimezone(tz) {
  try { localStorage.setItem(TZ_STORAGE, tz); } catch { /* ignore */ }
}

// formatInTZ renders an ISO timestamp as a wall-clock string in the given IANA
// timezone, e.g. "2026-07-28 15:30:00". Mirrors the prior
// toISOString().replace('T',' ') layout so the column width and "mono" cell
// styling are unchanged; only the displayed offset shifts. Returns '' for a
// falsy input so the caller can fall back to '—'.
//
// Implementation: Intl with en-CA yields "YYYY-MM-DD, HH:MM:SS (AM/PM)" under
// hour12:false; we strip the leading day-of-week when present and rejoin as
// "YYYY-MM-DD HH:MM:SS". Doing it via Intl (rather than Date getters) keeps the
// value correct across DST transitions in the target zone.
export function formatInTZ(iso, tz) {
  if (!iso) return '';
  const d = new Date(iso);
  if (isNaN(d.getTime())) return '';
  let parts;
  try {
    parts = new Intl.DateTimeFormat('en-CA', {
      timeZone: tz,
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
      hour12: false,
    }).formatToParts(d);
  } catch {
    // Unsupported tz id — degrade to UTC.
    return new Date(iso).toISOString().replace('T', ' ').replace(/\.\d+Z$/, 'Z');
  }
  const get = (t) => (parts.find((p) => p.type === t) || {}).value || '';
  // Intl can render hour "24" near midnight under hour12:false; normalize to 00.
  let h = get('hour');
  if (h === '24') h = '00';
  return `${get('year')}-${get('month')}-${get('day')} ${h}:${get('minute')}:${get('second')}`;
}

// tzAbbreviation returns the short zone name (e.g. "WIB", "UTC", "PDT") for a
// tz at the given instant, used in the detail modal to make the wall-clock
// unambiguous. Falls back to the tz id itself.
export function tzAbbreviation(iso, tz) {
  if (!iso) return tz;
  const d = new Date(iso);
  if (isNaN(d.getTime())) return tz;
  try {
    const s = new Intl.DateTimeFormat('en', { timeZone: tz, timeZoneName: 'short' })
      .formatToParts(d)
      .find((p) => p.type === 'timeZoneName');
    return (s && s.value) || tz;
  } catch {
    return tz;
  }
}

// toUTCFromTZ interprets a datetime-local input value ("YYYY-MM-DDTHH:MM") as a
// wall-clock in the given tz and returns its ISO-UTC equivalent. Replaces toUTC
// on pages that carry a timezone selector; the server only ever receives UTC
// from/to. Returns '' for an empty/invalid input.
//
// Approach: build a Date whose UTC fields equal the typed wall-clock, format it
// in the target tz and in UTC, and shift by the observed offset. This avoids
// any date-fns/dayjs dependency and stays correct across DST boundaries for the
// typed instant.
export function toUTCFromTZ(localValue, tz) {
  if (!localValue) return '';
  // Parse the "YYYY-MM-DDTHH:MM" (optionally with seconds) components without
  // leaning on the browser's local-zone Date parsing.
  const m = /^(\d{4})-(\d{2})-(\d{2})[T ](\d{2}):(\d{2})(?::(\d{2}))?$/.exec(localValue);
  if (!m) return '';
  const [, y, mo, d2, h, mi, s] = m;
  // Construct the wall-clock as UTC so Date.getUTC* returns exactly what was
  // typed; then reverse the target-tz offset to land on the real UTC instant.
  const wallUtc = new Date(Date.UTC(+y, +mo - 1, +d2, +h, +mi, +(s || 0)));
  if (isNaN(wallUtc.getTime())) return '';
  // Offset (minutes) of `tz` at this instant, e.g. +420 for Asia/Jakarta (WIB).
  let offsetMin;
  try {
    const asTz = new Intl.DateTimeFormat('en-US', {
      timeZone: tz,
      year: 'numeric', month: '2-digit', day: '2-digit',
      hour: '2-digit', minute: '2-digit', second: '2-digit',
      hour12: false,
    }).formatToParts(wallUtc);
    const get = (t) => (asTz.find((p) => p.type === t) || {}).value || '0';
    let hh = get('hour'); if (hh === '24') hh = '00';
    const tzInstant = Date.UTC(+get('year'), +get('month') - 1, +get('day'), +hh, +get('minute'), +get('second'));
    offsetMin = Math.round((tzInstant - wallUtc.getTime()) / 60000);
  } catch {
    // Unsupported tz — treat as UTC.
    return wallUtc.toISOString();
  }
  return new Date(wallUtc.getTime() - offsetMin * 60000).toISOString();
}

export const EVENTS_PAGE_SIZE = 10;

// LegendDot renders the colored square that precedes each segment label in
// the compact and modal token-breakdown legends. The color comes from the
// segment data (not a fixed class) so the palette stays centralized in
// TOKEN_SEGMENTS.
export function LegendDot({ color, className = '' }) {
  return (
    <span
      className={`tokbar__legend-dot ${className}`}
      style={{ background: color }}
    />
  );
}

// TokBar renders a stacked horizontal bar of the given segments. Each
// segment width is its share of `sum`; segments with a non-zero value get a
// minimum width so they remain visible. The `size` variant controls the bar
// height only.
export function TokBar({ segs, sum, size = 'sm', dim = false }) {
  const pct = (v, base) => (base === 0 ? 0 : (v / base) * 100);
  return (
    <div className={`tokbar tokbar--${size}`}>
      {segs.map((s) => s.value > 0 && (
        <div
          key={s.key}
          className="tokbar__seg tokbar__seg--min"
          style={{
            width: `${pct(s.value, sum)}%`,
            background: s.color,
            opacity: dim ? 0.7 : 1,
          }}
        />
      ))}
    </div>
  );
}

// CompactTokenBreakdown renders the stacked token bar plus a single-line
// numeric summary. Designed for the dense Recent Events table cell — hover
// the bar or the row for a title tooltip with every segment's value. When
// the event carries a cost_breakdown (include=cost_breakdown on the events
// request), a second stacked bar attributes dollars to the same slices.
export function CompactTokenBreakdown({ e }) {
  const segs = tokenSegments(e);
  const sum = segs.reduce((a, s) => a + s.value, 0);
  const title = segs.map((s) => `${s.label}: ${s.value.toLocaleString()}`).join(' · ');

  const cSegs = costSegments(e);
  const cSum = cSegs.reduce((a, s) => a + s.value, 0);
  const hasCost = Boolean(e.cost_breakdown);

  return (
    <div title={`${title} · Total: ${Number(e.total_tokens || 0).toLocaleString()}${hasCost ? ` · Cost: ${fmtUSD(e.cost_usd || 0)}` : ''}`}>
      <TokBar segs={segs} sum={sum} size="sm" />
      <div className="mono tokbar__legend tokbar__legend--sm">
        {segs.map((s) => (
          <span key={s.key} className="tokbar__legend-item tokbar__legend-item--sm">
            <LegendDot color={s.color} />
            <span className="dim">{s.short}</span>{' '}{s.value.toLocaleString()}
          </span>
        ))}
        <span className="dim tokbar__legend-sum">
          · Σ {(e.total_tokens || 0).toLocaleString()}
        </span>
      </div>
      {hasCost && (
        <>
          <div style={{ marginTop: 4 }}>
            <TokBar segs={cSegs} sum={cSum} size="md" dim />
          </div>
          <div className="mono tokbar__legend tokbar__legend--sm">
            {cSegs.map((s) => (
              <span key={s.key} className="tokbar__legend-item tokbar__legend-item--sm">
                <LegendDot color={s.color} />
                <span className="dim">{s.short}</span>{' '}{fmtUSDCompact(s.value)}
              </span>
            ))}
            <span className="dim tokbar__legend-sum">
              · Σ {fmtUSDCompact(e.cost_usd || 0)}
            </span>
          </div>
        </>
      )}
    </div>
  );
}

// TokenBreakdownCard is the richer, full-width breakdown shown in the event
// detail modal: stacked bar + legend with values, percentages, and the
// persisted total for reconciliation. When the event carries a
// cost_breakdown (always present on the single-event endpoint), a second bar
// attributes dollar cost to the same slices with a parallel legend.
export function TokenBreakdownCard({ e }) {
  const segs = tokenSegments(e);
  const sum = segs.reduce((a, s) => a + s.value, 0);
  const pct = (v, base) => (base === 0 ? 0 : (v / base) * 100);
  const total = Number(e.total_tokens || 0);

  const cSegs = costSegments(e);
  const cSum = cSegs.reduce((a, s) => a + s.value, 0);
  const hasCost = Boolean(e.cost_breakdown);

  return (
    <div className="detail-row__block">
      <div className="detail-row__block-label">Token breakdown</div>
      <TokBar segs={segs} sum={sum} size="lg" />
      <div className="tokbar__legend" style={{ marginTop: 10 }}>
        {segs.map((s) => (
          <span key={s.key} className="tokbar__legend-item">
            <LegendDot color={s.color} />
            <span className="dim">{s.label}</span>
            <span className="mono">{s.value.toLocaleString()}</span>
            <span className="dim" style={{ fontSize: 11 }}>{pct(s.value, sum).toFixed(1)}%</span>
          </span>
        ))}
        <span className="dim mono tokbar__legend-sum">
          sum {sum.toLocaleString()} · total {total.toLocaleString()}
          {sum !== total ? ` (different by ${(sum - total).toLocaleString()})` : ''}
        </span>
      </div>

      {hasCost && (
        <>
          <div className="detail-row__block-label" style={{ marginTop: 14 }}>
            Cost breakdown <span style={{ fontSize: 10 }}>(at-rest pricing)</span>
          </div>
          <TokBar segs={cSegs} sum={cSum} size="lg" dim />
          <div className="tokbar__legend" style={{ marginTop: 10 }}>
            {cSegs.map((s) => (
              <span key={s.key} className="tokbar__legend-item">
                <LegendDot color={s.color} />
                <span className="dim">{s.label}</span>
                <span className="mono">{fmtUSD(s.value)}</span>
                <span className="dim" style={{ fontSize: 11 }}>{pct(s.value, cSum).toFixed(1)}%</span>
              </span>
            ))}
            <span className="dim mono tokbar__legend-sum">
              sum {fmtUSD(cSum)} · total {fmtUSD(e.cost_usd || 0)}
              {Math.abs(cSum - (e.cost_usd || 0)) > 1e-9 ? ` (different by ${fmtUSD(cSum - (e.cost_usd || 0))})` : ''}
            </span>
          </div>
        </>
      )}
    </div>
  );
}

// ChartSkeleton fills the chart area with shimmer bars instead of a centered
// spinner, matching the loading pattern used across the overhauled pages and
// avoiding layout shift when the data arrives.
export function ChartSkeleton() {
  return (
    <div className="chart-skeleton">
      <div className="skeleton-line chart-skeleton__bar" />
    </div>
  );
}

// FilterSelect renders a dropdown populated with distinct values from the
// usage_events table. Includes a leading "any" option that clears the filter.
// Shows a small "loading…" placeholder while the options request is in flight.
export function FilterSelect({ options, value, onChange, anyLabel, loading }) {
  return (
    <select value={value || ''} onChange={(e) => onChange(e.target.value)} disabled={loading}>
      <option value="">{anyLabel}</option>
      {options.map((opt) => (
        <option key={opt.value} value={opt.value}>{opt.label}</option>
      ))}
    </select>
  );
}

export function DetailRow({ label, value, mono }) {
  return (
    <div className="detail-row">
      <div className="detail-row__label">{label}</div>
      <div className={`detail-row__value ${mono ? 'mono' : ''}`}>{value}</div>
    </div>
  );
}

// UsageToolbar renders the shared preset + custom-range + filter select bar.
// Lifted so the Events and Errors pages stay visually consistent. The caller
// owns the filter state and the change handlers; this component is presentational.
export function UsageToolbar({
  presetIdx,
  onPreset,
  filter,
  updateFilter,
  rangeParams,
  filterOptions,
}) {
  return (
    <div className="card">
      <div className="usage-toolbar">
        <div className="seg-group" role="tablist" aria-label="Time window">
          {PRESETS.map((p, i) => (
            <button
              key={p.label}
              type="button"
              className={`seg-btn ${i === presetIdx && !filter.useCustomRange ? 'seg-btn--active' : ''}`}
              onClick={() => { onPreset(i); updateFilter({ useCustomRange: false }); }}
            >
              {p.label}
            </button>
          ))}
        </div>
        <div className="catalog-toolbar__spacer" />
        <label className="row gap-sm" style={{ cursor: 'pointer' }}>
          <input
            type="checkbox"
            checked={filter.useCustomRange}
            onChange={(e) => updateFilter({ useCustomRange: e.target.checked })}
            style={{ width: 'auto' }}
          />
          <span className="form__label" style={{ margin: 0 }}>Custom range</span>
        </label>
      </div>
      <div className="grid grid--4">
        <div className="form__row" style={{ marginBottom: 0 }}>
          <label className="form__label">API Key</label>
          <FilterSelect
            loading={filterOptions.loading}
            options={(filterOptions.data?.api_keys || []).map((k) => ({ value: k.id, label: k.alias || k.id }))}
            value={filter.api_key_id}
            onChange={(v) => updateFilter({ api_key_id: v })}
            anyLabel="any key"
          />
        </div>
        <div className="form__row" style={{ marginBottom: 0 }}>
          <label className="form__label">Provider</label>
          <FilterSelect
            loading={filterOptions.loading}
            options={(filterOptions.data?.providers || []).map((p) => ({ value: p, label: p }))}
            value={filter.provider}
            onChange={(v) => updateFilter({ provider: v })}
            anyLabel="any provider"
          />
        </div>
        <div className="form__row" style={{ marginBottom: 0 }}>
          <label className="form__label">Model</label>
          <FilterSelect
            loading={filterOptions.loading}
            options={(filterOptions.data?.models || []).map((m) => ({ value: m, label: m }))}
            value={filter.model}
            onChange={(v) => updateFilter({ model: v })}
            anyLabel="any model"
          />
        </div>
        <div className="form__row" style={{ marginBottom: 0 }}>
          <label className="form__label">Interval</label>
          <select
            value={rangeParams.interval}
            onChange={(e) => updateFilter({ interval: e.target.value })}
            disabled={!filter.useCustomRange}
          >
            <option value="minute">minute</option>
            <option value="hour">hour</option>
            <option value="day">day</option>
          </select>
        </div>
      </div>
      {filter.useCustomRange && (
        <div className="usage-toolbar__range">
          <div className="row gap-sm">
            <span className="usage-toolbar__range-label">From (UTC)</span>
            <input type="datetime-local" value={filter.customFrom} onChange={(e) => updateFilter({ customFrom: e.target.value })} />
          </div>
          <div className="row gap-sm">
            <span className="usage-toolbar__range-label">To (UTC)</span>
            <input type="datetime-local" value={filter.customTo} onChange={(e) => updateFilter({ customTo: e.target.value })} />
          </div>
        </div>
      )}
    </div>
  );
}
