import React, { useMemo, useState } from 'react';
import { getUsageEvent, getUsageErrors, getUsageEventBodies } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import {
  Spinner, ErrorBanner, EmptyState, Modal,
} from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';

// usageShared.jsx — components and helpers shared between the Usage Stats page,
// the Recent Events page (split out of Usage Stats), and the Errors page
// (failed-attempt drill-down). Lifted out of UsageStatsPage.jsx when Errors was
// promoted from a tab into its own sidebar menu item, and again when Recent
// Events was promoted to its own page so the two surfaces could render identical
// token/cost breakdowns, presets, filter selects, and detail rows without
// duplication.

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
  { key: 'input_tokens', costKey: 'input', rateKey: 'input_per_1m_usd', label: 'Input', short: 'in', color: 'var(--accent)' },
  { key: 'cached_tokens', costKey: 'cached_read', rateKey: 'cached_read_per_1m_usd', label: 'Cached', short: 'cch', color: 'var(--success)' },
  { key: 'cache_creation_tokens', costKey: 'cache_creation', rateKey: 'cached_input_per_1m_usd', label: 'Cache write', short: 'ccw', color: '#38bdf8' },
  { key: 'output_tokens', costKey: 'output', rateKey: 'output_per_1m_usd', label: 'Output', short: 'out', color: 'var(--warning)' },
  { key: 'reasoning_tokens', costKey: 'reasoning', rateKey: 'reasoning_per_1m_usd', label: 'Reasoning', short: 're', color: '#a78bfa' },
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

// fmtRate renders a unit price (USD per 1,000,000 tokens) for the cost
// derivation table. Zero is shown as "$0" so "free" and "rate not set" both
// read cleanly; the no-pricing banner distinguishes the latter.
export function fmtRate(v) {
  if (!v || v <= 0) return '$0';
  return `$${v.toFixed(4)}`;
}

// pricingHasRates mirrors Go's Pricing.HasRates so the card can tell a
// genuinely-zero (free) breakdown from a missing-pricing row (all rates 0,
// cost 0). The applied_pricing payload is present-but-all-zero in the
// missing case, so the dashboard shows a "no pricing configured" banner
// instead of conflating it with free.
export function pricingHasRates(p) {
  if (!p) return false;
  return Number(p.input_per_1m_usd || 0) > 0
    || Number(p.output_per_1m_usd || 0) > 0
    || Number(p.reasoning_per_1m_usd || 0) > 0
    || Number(p.cached_input_per_1m_usd || 0) > 0
    || Number(p.cached_read_per_1m_usd || 0) > 0;
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
// attributes dollar cost to the same slices with a parallel legend, and a
// per-segment derivation table (tokens × rate → cost) surfaces the unit rates
// that produced it via applied_pricing. When applied_pricing is present but
// all-zero (no pricing row matched the model/alias), a banner makes that
// explicit so "no pricing configured" is distinguishable from "free".
export function TokenBreakdownCard({ e }) {
  const segs = tokenSegments(e);
  const sum = segs.reduce((a, s) => a + s.value, 0);
  const pct = (v, base) => (base === 0 ? 0 : (v / base) * 100);
  const total = Number(e.total_tokens || 0);

  const cSegs = costSegments(e);
  const cSum = cSegs.reduce((a, s) => a + s.value, 0);
  const hasCost = Boolean(e.cost_breakdown);
  // applied_pricing carries the unit rates that produced the breakdown, so the
  // card can render a per-segment tokens × rate → cost derivation. Present-but-
  // all-zero when no pricing row matched the model/alias — the banner below
  // surfaces that as "no pricing configured" rather than reading as "free".
  const pricing = e.applied_pricing;
  const hasRates = pricingHasRates(pricing);
  // Discount: discount_pct is the resolved model-group discount applied to
  // cost_usd at flush time. original_cost_usd is the pre-discount cost
  // (tokens × rate); cost_usd is the post-discount value actually billed.
  // When no discount was applied both stay 0 and the derivation table renders
  // its plain Total row (unchanged behavior).
  const discountPct = Number(e.discount_pct || 0);
  const hasDiscount = discountPct > 0 && Number(e.original_cost_usd || 0) > 0;
  const discountAmount = hasDiscount
    ? Number(e.original_cost_usd || 0) - Number(e.cost_usd || 0)
    : 0;
  // Derivation rows zip the token counts, resolved rates, and per-segment
  // dollar costs. Built from TOKEN_SEGMENTS so the order/coloring matches the
  // stacked bars above exactly. Only rendered when applied_pricing carries at
  // least one rate — an all-zero pricing row (deleted/changed since flush)
  // would show misleading all-zero segment rows, so the discount summary below
  // (which derives from the persisted cost_usd, not the re-resolved rates)
  // takes over and the per-segment table is skipped.
  const derRows = hasCost && pricing && hasRates
    ? cSegs.map((s) => ({
        ...s,
        tokens: Number(e[s.key] || 0),
        rate: Number(pricing[s.rateKey] || 0),
      }))
    : [];
  // hasSummaryRows is true when we can render either the per-segment
  // derivation table (derRows > 0) or just the discount summary (when a
  // discount applies to a row whose pricing has drifted but cost_usd is
  // still present).
  const hasSummaryRows = derRows.length > 0 || hasDiscount;

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
              {hasDiscount
                ? ` (after ${discountPct}% discount of ${fmtUSD(e.original_cost_usd || 0)})`
                : (Math.abs(cSum - (e.cost_usd || 0)) > 1e-9 ? ` (different by ${fmtUSD(cSum - (e.cost_usd || 0))})` : '')}
            </span>
          </div>

          {hasSummaryRows && (
            <table className="costder">
              <thead>
                <tr>
                  <th>Segment</th>
                  <th style={{ textAlign: 'right' }}>Tokens</th>
                  <th style={{ textAlign: 'right' }}>Rate ($/1M)</th>
                  <th style={{ textAlign: 'right' }}>Cost</th>
                </tr>
              </thead>
              <tbody>
                {derRows.map((r) => (
                  <tr key={r.key}>
                    <td>
                      <LegendDot color={r.color} />
                      <span>{r.label}</span>
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>{r.tokens.toLocaleString()}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtRate(r.rate)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtUSD(r.value)}</td>
                  </tr>
                ))}
                {hasDiscount ? (
                  <>
                    <tr className="costder__sum">
                      <td>Before discount</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{total.toLocaleString()}</td>
                      <td />
                      <td className="mono" style={{ textAlign: 'right' }}>{fmtUSD(e.original_cost_usd || 0)}</td>
                    </tr>
                    <tr className="costder__discount">
                      <td>Discount</td>
                      <td />
                      <td className="mono" style={{ textAlign: 'right' }}>{discountPct}%</td>
                      <td className="mono" style={{ textAlign: 'right' }}>−{fmtUSD(discountAmount)}</td>
                    </tr>
                    <tr className="costder__sum">
                      <td>Total (after discount)</td>
                      <td className="mono" style={{ textAlign: 'right' }}>{total.toLocaleString()}</td>
                      <td />
                      <td className="mono" style={{ textAlign: 'right' }}>{fmtUSD(e.cost_usd || 0)}</td>
                    </tr>
                  </>
                ) : (
                  <tr className="costder__sum">
                    <td>Total</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{total.toLocaleString()}</td>
                    <td />
                    <td className="mono" style={{ textAlign: 'right' }}>{fmtUSD(e.cost_usd || 0)}</td>
                  </tr>
                )}
              </tbody>
            </table>
          )}

          {pricing && !hasRates && (
            <div className="costder__nopricing">
              No pricing configured for <span className="mono">{e.alias || e.model}</span> — cost is $0.
              Set rates in Model Catalog → Pricing to attribute dollars.
            </div>
          )}
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

// CopyButton copies `value` to the clipboard and shows a transient "copied"
// confirmation. Falls back to the legacy execCommand path when the async
// Clipboard API is unavailable (http origins / older browsers). Renders
// nothing when value is empty so it can be dropped inline without guarding
// at every call site.
export function CopyButton({ value, label = 'Copy' }) {
  const [copied, setCopied] = useState(false);
  if (!value) return null;
  function copy() {
    const done = () => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    };
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(String(value)).then(done).catch(() => { /* ignore */ });
        return;
      }
    } catch { /* fall through to execCommand */ }
    try {
      const ta = document.createElement('textarea');
      ta.value = String(value);
      ta.style.position = 'fixed';
      ta.style.opacity = '0';
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      document.body.removeChild(ta);
      done();
    } catch { /* ignore */ }
  }
  return (
    <button
      type="button"
      className="detail-row__copy"
      onClick={copy}
      title={`Copy ${label}`}
      aria-label={`Copy ${label}`}
    >
      {copied ? '✓ copied' : '⧉'}
    </button>
  );
}

// FailoverHistory renders the failed attempts that preceded the currently-viewed
// usage row, correlated by request_id. A request that fails over across N
// credentials produces N usage_errors rows sharing one request_id plus (on
// eventual success) one usage_events row — this timeline surfaces those hidden
// retries so an operator can see the full routing path. Renders nothing when
// no request_id is available or no matching failed attempts exist. `currentKind`
// is 'event' (the current row is the successful attempt) or 'error' (the current
// row is one of the failures).
export function FailoverHistory({ requestId, currentId, currentKind, timezone }) {
  const attempts = useAsync(
    () => (requestId ? getUsageErrors({ request_id: requestId, page_size: 50 }) : Promise.resolve(null)),
    [requestId],
  );
  const rows = attempts.data?.errors || [];
  // Nothing to show: no request id, or this request never failed over.
  if (!requestId || attempts.error || (!attempts.loading && rows.length === 0)) {
    return null;
  }
  // Newest-first from the API; the attempts happened oldest→newest, so flip.
  const ordered = [...rows].sort((a, b) => new Date(a.requested_at) - new Date(b.requested_at));
  const total = ordered.length + (currentKind === 'event' ? 1 : 0);
  const succeeded = currentKind === 'event';

  return (
    <div className="detail-row__block">
      <div className="detail-row__block-label">
        Failover history
        <span className="dim" style={{ fontSize: 11, marginLeft: 8 }}>
          {ordered.length} failed attempt{ordered.length === 1 ? '' : 's'}
          {succeeded ? ' → succeeded' : ` · attempt ${ordered.length} of ${total}`}
        </span>
      </div>
      {attempts.loading && <Spinner label="Loading attempts…" />}
      {!attempts.loading && (
        <ol className="failover">
          {ordered.map((a, i) => {
            const isCurrent = currentKind === 'error' && Number(a.id) === Number(currentId);
            return (
              <li key={String(a.id)} className={`failover__item${isCurrent ? ' failover__item--current' : ''}`}>
                <span className="failover__index">{i + 1}</span>
                <div className="failover__body">
                  <div className="failover__line">
                    <span className="mono">{a.official_provider || a.provider || '—'}</span>
                    <span className="mono dim" style={{ marginLeft: 8 }}>{a.alias || a.model || '—'}</span>
                    <span className="failover__status">{a.fail_status_code || '—'}</span>
                    {a.latency_ms ? (
                      <span className="dim mono" style={{ marginLeft: 8 }}>{a.latency_ms} ms</span>
                    ) : null}
                    <span className="dim mono" style={{ marginLeft: 8 }}>
                      {a.requested_at ? formatInTZ(a.requested_at, timezone) : ''}
                    </span>
                  </div>
                  {a.error_message && (
                    <div className="failover__error mono" title={a.error_message}>{a.error_message}</div>
                  )}
                </div>
              </li>
            );
          })}
          {succeeded && (
            <li className="failover__item failover__item--success">
              <span className="failover__index">✓</span>
              <div className="failover__body">
                <div className="failover__line dim">routed to a healthy credential</div>
              </div>
            </li>
          )}
        </ol>
      )}
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

// EventsTableBody renders the table + pager only (no card). Used by the
// Recent Events page, which wraps it in a single card. Failed-attempt rows
// have their own ErrorsTableBody on the Errors page.
export function EventsTableBody({ events, page, pageSize, onPage, onRowClick, timezone }) {
  const total = events.data?.total || 0;
  const rows = events.data?.events || [];
  const totalPages = total === 0 ? 1 : Math.ceil(total / pageSize);
  return (
    <>
      {events.loading && (
        <div style={{ overflowX: 'auto' }}>
          <table className="table" style={{ tableLayout: 'fixed' }}>
            <thead>
              <tr>
                <th>Time ({timezone})</th>
                <th>Request ID</th>
                <th>Key / Alias</th>
                <th>Provider Official</th>
                <th>Model Alias</th>
                <th style={{ minWidth: 260 }}>Token breakdown</th>
                <th style={{ textAlign: 'right' }}>Cost</th>
                <th style={{ textAlign: 'right' }}>Latency</th>
                <th style={{ textAlign: 'right' }}>TTFT</th>
              </tr>
            </thead>
            <tbody>
              {Array.from({ length: 4 }).map((_, i) => (
                <tr key={i} className="skeleton-row">
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {events.error && <ErrorBanner error={events.error} />}
      {!events.loading && !events.error && rows.length === 0 && (
        <EmptyState title="No events for this window" />
      )}
      {!events.loading && !events.error && rows.length > 0 && (
        <>
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>Time ({timezone})</th>
                  <th>Request ID</th>
                  <th>Key / Alias</th>
                  <th>Provider Official</th>
                  <th>Model Alias</th>
                  <th style={{ minWidth: 260 }}>Token breakdown</th>
                  <th style={{ textAlign: 'right' }}>Cost</th>
                  <th style={{ textAlign: 'right' }}>Latency</th>
                  <th style={{ textAlign: 'right' }}>TTFT</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((e) => (
                  <tr
                    key={String(e.id)}
                    className="row-link"
                    onClick={() => onRowClick(e.id)}
                  >
                    <td className="mono" style={{ whiteSpace: 'nowrap' }} title={e.requested_at ? new Date(e.requested_at).toISOString() : ''}>
                      {e.requested_at ? formatInTZ(e.requested_at, timezone) : '—'}
                    </td>
                    <td className="mono" style={{ whiteSpace: 'nowrap' }}>{e.request_id || '—'}</td>
                    <td className="mono">{e.key_alias || e.api_key_id || '—'}</td>
                    <td>{e.official_provider || e.provider || '—'}</td>
                    <td className="mono">{e.alias || e.model || '—'}</td>
                    <td>
                      <CompactTokenBreakdown e={e} />
                    </td>
                    <td className="mono" style={{ textAlign: 'right' }}>${(e.cost_usd || 0).toFixed(4)}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{e.latency_ms ? `${e.latency_ms} ms` : '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{e.ttft_ms ? `${e.ttft_ms} ms` : '—'}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
          <Pager
            page={page}
            totalPages={totalPages}
            total={total}
            pageSize={pageSize}
            onPageChange={onPage}
          />
        </>
      )}
    </>
  );
}

// prettyJSON returns a 2-space-indented body when it parses as JSON, else the
// raw text. Keeps the modal readable for both JSON and streamed payloads.
function prettyJSON(body) {
  if (!body) return '';
  try {
    return JSON.stringify(JSON.parse(body), null, 2);
  } catch {
    return body;
  }
}

function HeadersList({ headers }) {
  const entries = Object.entries(headers || {});
  if (entries.length === 0) return <div className="dim">No headers captured.</div>;
  return (
    <table className="table" style={{ tableLayout: 'fixed' }}>
      <tbody>
        {entries.map(([k, v]) => (
          <tr key={k}>
            <td className="mono" style={{ width: 220 }}>{k}</td>
            <td className="mono" style={{ wordBreak: 'break-all' }}>{Array.isArray(v) ? v.join(', ') : String(v)}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function BodyBlock({ title, section }) {
  if (!section) return null;
  return (
    <details className="card" style={{ marginTop: 8 }}>
      <summary style={{ cursor: 'pointer' }}>{title}</summary>
      <div className="detail-row__block-label" style={{ marginTop: 8 }}>Headers</div>
      <HeadersList headers={section.headers} />
      <div className="detail-row__block-label" style={{ marginTop: 8 }}>
        Body
        <CopyButton value={section.body || ''} label={`${title} body`} />
      </div>
      <pre className="mono" style={{ whiteSpace: 'pre-wrap', wordBreak: 'break-all', maxHeight: 420, overflow: 'auto' }}>
        {prettyJSON(section.body) || '—'}
      </pre>
    </details>
  );
}

// EventBodiesContent renders one captured payload response. It is split from
// EventBodiesSection so the data-driven markup can be exercised by the
// react-dom/server test harness (effects do not run there).
export function EventBodiesContent({ data, loading, error }) {
  return (
    <>
      <div className="detail-row__block-label" style={{ marginTop: 14 }}>
        Request &amp; Response
        {data?.truncated && <span className="badge badge--warn" title="Capture hit a size cap">truncated</span>}
      </div>
      {loading && <Spinner label="Loading captured payloads…" />}
      {error && <ErrorBanner error={error} />}
      {!loading && !error && data && !data.available && (
        <div className="dim">
          No request/response captured for this request (capture is off for the provider or the data was not retained).
        </div>
      )}
      {!loading && !error && data?.available && (
        <>
          <BodyBlock title="Client request" section={data.client_request} />
          <BodyBlock title="Upstream request" section={{ body: data.upstream_request }} />
          <BodyBlock title="Client response" section={data.client_response} />
          <BodyBlock title="Upstream response" section={{ body: data.upstream_response }} />
        </>
      )}
    </>
  );
}

// EventBodiesSection lazily loads captured request/response payloads for one
// event. Capture is opt-in per upstream provider, so available:false is a
// normal state, not an error.
export function EventBodiesSection({ id }) {
  const detail = useAsync(() => getUsageEventBodies(id), [id]);
  return <EventBodiesContent data={detail.data} loading={detail.loading} error={detail.error} />;
}

// EventDetailModal fetches and renders a single usage event. The sealed
// api_key_principal is intentionally never shown; the operator sees the
// non-secret key_alias and the other fields needed for triage.
export function EventDetailModal({ id, timezone, onClose }) {
  const detail = useAsync(() => getUsageEvent(id), [id]);
  const e = detail.data?.event;
  // A silent upstream substitution is when the served model differs from the
  // resolved model. Surface it explicitly rather than burying it in a field.
  const substituted = Boolean(e?.served_model && e?.model && e.served_model !== e.model);
  return (
    <Modal title={`Event #${id}`} onClose={onClose} size="lg">
      {detail.loading && <Spinner label="Loading…" />}
      {detail.error && <ErrorBanner error={detail.error} />}
      {!detail.loading && !detail.error && e && (
        <>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <DetailRow label={`Time (${tzAbbreviation(e.requested_at, timezone)})`} value={e.requested_at ? formatInTZ(e.requested_at, timezone) : '—'} mono />
            <DetailRow label="Time (UTC)" value={e.requested_at ? new Date(e.requested_at).toISOString() : '—'} mono />
          </div>

          <div className="detail-row__block-label" style={{ marginTop: 14 }}>Routing</div>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <div className="detail-row">
              <div className="detail-row__label">Request ID</div>
              <div className="detail-row__value mono">
                {e.request_id || '—'}
                <CopyButton value={e.request_id} label="request id" />
              </div>
            </div>
            <DetailRow label="API Key ID" value={e.api_key_id || '—'} mono />
            <DetailRow label="Key Alias" value={e.key_alias || '—'} mono />
            <DetailRow label="Provider Official" value={e.official_provider || e.provider || '—'} />
            <DetailRow label="Provider (internal)" value={e.provider || '—'} mono />
            <DetailRow label="Route Model" value={e.route_model || '—'} mono />
            <DetailRow label="Model Alias" value={e.alias || e.model || '—'} mono />
            <DetailRow label="Model (resolved)" value={e.model || '—'} mono />
            <div className="detail-row">
              <div className="detail-row__label">Model (served)</div>
              <div className="detail-row__value mono">
                {e.served_model || '—'}
                {substituted && <span className="badge badge--warn" title="Upstream served a different model than requested">substituted</span>}
              </div>
            </div>
            <DetailRow label="Executor" value={e.executor_type || '—'} />
            <DetailRow label="Auth Type" value={e.auth_type || '—'} />
            <DetailRow label="Source" value={e.source || '—'} />
            <DetailRow label="Reasoning Effort" value={e.reasoning_effort || '—'} />
            <DetailRow label="Service Tier" value={e.service_tier || '—'} />
            <DetailRow label="Response Service Tier" value={e.response_service_tier || '—'} />
            <DetailRow label="Endpoint" value={e.endpoint || '—'} mono />
          </div>

          <div style={{ marginTop: 14 }}>
            <TokenBreakdownCard e={e} />
          </div>

          <div className="detail-row__block-label" style={{ marginTop: 14 }}>Timing & cost</div>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <DetailRow label="Cost (USD)" value={`$${(e.cost_usd || 0).toFixed(4)}`} mono />
            {Number(e.discount_pct || 0) > 0 && (
              <DetailRow
                label="Discount"
                value={`${e.discount_pct}% (was $${(e.original_cost_usd || 0).toFixed(4)})`}
                mono
              />
            )}
            <DetailRow label="Latency" value={e.latency_ms ? `${e.latency_ms} ms` : '—'} mono />
            <DetailRow label="TTFT" value={e.ttft_ms ? `${e.ttft_ms} ms` : '—'} mono />
          </div>

          <div className="detail-row__block-label" style={{ marginTop: 14 }}>Client</div>
          <div className="grid grid--2" style={{ gap: '6px 24px' }}>
            <DetailRow label="Client IP" value={e.client_ip || '—'} mono />
            <DetailRow label="Forwarded For" value={e.forwarded_for || '—'} mono />
            <DetailRow label="Failed" value={e.failed ? 'yes' : 'no'} mono />
            <DetailRow label="Fail Status" value={e.fail_status_code ? String(e.fail_status_code) : '—'} mono />
            <DetailRow label="Generate" value={e.generate ? 'true' : 'false'} mono />
          </div>

          <EventBodiesSection id={id} />

          <FailoverHistory
            requestId={e.request_id}
            currentId={e.id}
            currentKind="event"
            timezone={timezone}
          />
        </>
      )}
      {!detail.loading && !detail.error && !e && <EmptyState title="Event not found" />}
    </Modal>
  );
}
