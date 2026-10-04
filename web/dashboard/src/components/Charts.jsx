import React, { useMemo } from 'react';

// Lightweight, dependency-free SVG charts for the Usage Stats page.
// Avoids pulling in a chart library (chart.js, recharts) which would bloat
// the bundle. Each component is a pure SVG render of its data — no axis
// ticks, no tooltips; just the shape. The surrounding tables give the
// precise numbers; these charts are for at-a-glance trends.

// Sparkline — minimal line chart for a single-series metric over time.
// `values` are numbers; `labels` are optional x-axis tick labels (e.g. timestamps).
export function Sparkline({ values, color = 'var(--accent)', height = 60, width = 320 }) {
  const path = useMemo(() => {
    if (!values || values.length === 0) return '';
    const max = Math.max(...values, 1);
    const min = Math.min(...values, 0);
    const range = max - min || 1;
    const stepX = values.length > 1 ? width / (values.length - 1) : 0;
    return values
      .map((v, i) => {
        const x = i * stepX;
        const y = height - ((v - min) / range) * height;
        return `${i === 0 ? 'M' : 'L'} ${x.toFixed(1)} ${y.toFixed(1)}`;
      })
      .join(' ');
  }, [values, width, height]);

  const areaPath = useMemo(() => {
    if (!path) return '';
    return `${path} L ${width} ${height} L 0 ${height} Z`;
  }, [path, width, height]);

  if (!values || values.length === 0) {
    return <div className="dim" style={{ fontSize: 12, padding: 20 }}>No data</div>;
  }
  const gradId = `spark-${Math.random().toString(36).slice(2, 9)}`;
  return (
    <svg width={width} height={height} style={{ display: 'block' }}>
      <defs>
        <linearGradient id={gradId} x1="0" y1="0" x2="0" y2="1">
          <stop offset="0%" stopColor={color} stopOpacity="0.3" />
          <stop offset="100%" stopColor={color} stopOpacity="0" />
        </linearGradient>
      </defs>
      <path d={areaPath} fill={`url(#${gradId})`} stroke="none" />
      <path d={path} fill="none" stroke={color} strokeWidth="1.5" />
    </svg>
  );
}

// BarChart — vertical bars for time-series or categorical data.
// `data` is an array of { label, value } objects. label is shown only for
// sparse ticks (last 6 buckets) to avoid axis clutter.
export function BarChart({ data, color = 'var(--accent)', height = 100, width = 480, maxBars = 24 }) {
  const visible = useMemo(() => {
    if (!data || data.length === 0) return [];
    // Down-sample to maxBars points by taking every Nth point so the chart
    // always fits without horizontal scrolling.
    if (data.length <= maxBars) return data;
    const step = Math.ceil(data.length / maxBars);
    return data.filter((_, i) => i % step === 0);
  }, [data, maxBars]);
  const max = useMemo(() => Math.max(...(visible.map((d) => d.value) || [1]), 1), [visible]);
  if (visible.length === 0) {
    return <div className="dim" style={{ fontSize: 12, padding: 20 }}>No data</div>;
  }
  const barWidth = width / visible.length;
  const ticks = pickTickLabels(visible);
  return (
    <svg width={width} height={height + 16} style={{ display: 'block' }}>
      {visible.map((d, i) => {
        const barH = (d.value / max) * height;
        const x = i * barWidth;
        return (
          <g key={i}>
            <rect
              x={x + 1} y={height - barH}
              width={Math.max(barWidth - 2, 1)}
              height={barH} fill={color}
              opacity={0.85}
              rx={1}
            >
              <title>{`${d.label}: ${d.value.toLocaleString()}`}</title>
            </rect>
            {ticks.includes(i) && (
              <text x={x + barWidth / 2} y={height + 12} fill="var(--text-dim)" fontSize="9" textAnchor="middle">
                {shortLabel(d.label)}
              </text>
            )}
          </g>
        );
      })}
    </svg>
  );
}

// MultiBarChart — stacked bars for two metrics over time (e.g. input + output tokens).
export function MultiBarChart({ data, colorA = 'var(--accent)', colorB = 'var(--warning)', height = 100, width = 480, maxBars = 24 }) {
  const visible = useMemo(() => {
    if (!data || data.length === 0) return [];
    if (data.length <= maxBars) return data;
    const step = Math.ceil(data.length / (maxBars));
    return data.filter((_, i) => i % step === 0);
  }, [data, maxBars]);
  const max = useMemo(() => {
    if (visible.length === 0) return 1;
    return Math.max(...visible.map((d) => (d.a || 0) + (d.b || 0)), 1);
  }, [visible]);
  if (visible.length === 0) {
    return <div className="dim" style={{ fontSize: 12, padding: 20 }}>No data</div>;
  }
  const barWidth = width / visible.length;
  const ticks = pickTickLabels(visible);
  return (
    <svg width={width} height={height + 16} style={{ display: 'block' }}>
      {visible.map((d, i) => {
        const a = d.a || 0, b = d.b || 0;
        const hA = (a / max) * height;
        const hB = (b / max) * height;
        const x = i * barWidth;
        return (
          <g key={i}>
            <rect x={x + 1} y={height - hA - hB}
              width={Math.max(barWidth - 2, 1)} height={hB}
              fill={colorB} opacity={0.85} rx={1}>
              <title>{`${d.label}: A=${a.toLocaleString()}  B=${b.toLocaleString()}`}</title>
            </rect>
            <rect x={x + 1} y={height - hA}
              width={Math.max(barWidth - 2, 1)} height={hA}
              fill={colorA} opacity={0.85} rx={1}>
            </rect>
            {ticks.includes(i) && (
              <text x={x + barWidth / 2} y={height + 12} fill="var(--text-dim)" fontSize="9" textAnchor="middle">
                {shortLabel(d.label)}
              </text>
            )}
          </g>
        );
      })}
    </svg>
  );
}

// pickTickLabels returns indices of labels to actually show, so we don't
// render overlapping text on dense charts. Returns the first, last, and
// ~4 evenly-spaced indices.
function pickTickLabels(data) {
  const n = data.length;
  if (n <= 1) return [0];
  if (n <= 6) return data.map((_, i) => i);
  const out = new Set([0, n - 1]);
  const spacing = Math.floor(n / 5);
  for (let i = spacing; i < n - 1; i += spacing) out.add(i);
  return [...out];
}

// shortLabel trims a bucket label to a short tick for axis display:
//   "2026-07-19T12:00:00Z" → "12:00"
//   "2026-07-19" → "07-19"
function shortLabel(label) {
  if (!label) return '';
  const isoMatch = label.match(/T(\d{2}:\d{2})/);
  if (isoMatch) return isoMatch[1];
  const dateMatch = label.match(/(\d{2}-\d{2})$/);
  if (dateMatch) return dateMatch[1];
  if (label.length > 8) return label.slice(-8);
  return label;
}

// DonutChart — proportional segments for categorical cost/volume breakdown.
// `segments` is an array of { label, value, color }. The center shows the
// total. Segments under ~3% get merged into "other" to keep the ring readable.
export function DonutChart({ segments, size = 160, innerRadius = 0.6 }) {
  const total = useMemo(() => segments.reduce((s, seg) => s + seg.value, 0), [segments]);
  const merged = useMemo(() => {
    if (total === 0) return [];
    const threshold = total * 0.03;
    const main = segments.filter((s) => s.value >= threshold);
    const other = segments.filter((s) => s.value < threshold);
    const otherSum = other.reduce((s, o) => s + o.value, 0);
    const out = [...main];
    if (otherSum > 0) out.push({ label: 'other', value: otherSum, color: 'var(--text-dim)' });
    return out;
  }, [segments, total]);

  const r = size / 2;
  const ir = r * innerRadius;
  const cx = r, cy = r;

  const arcs = useMemo(() => {
    if (merged.length === 0) return [];
    let startAngle = -Math.PI / 2;
    return merged.map((seg) => {
      const frac = seg.value / total;
      const angle = frac * 2 * Math.PI;
      const endAngle = startAngle + angle;
      const x1 = cx + r * Math.cos(startAngle);
      const y1 = cy + r * Math.sin(startAngle);
      const x2 = cx + r * Math.cos(endAngle);
      const y2 = cy + r * Math.sin(endAngle);
      const large = angle > Math.PI ? 1 : 0;
      const path = [
        `M ${cx + ir * Math.cos(startAngle)} ${cy + ir * Math.sin(startAngle)}`,
        `L ${x1} ${y1}`,
        `A ${r} ${r} 0 ${large} 1 ${x2} ${y2}`,
        `L ${cx + ir * Math.cos(endAngle)} ${cy + ir * Math.sin(endAngle)}`,
        `A ${ir} ${ir} 0 ${large} 0 ${cx + ir * Math.cos(startAngle)} ${cy + ir * Math.sin(startAngle)}`,
        'Z',
      ].join(' ');
      const arc = { path, label: seg.label, value: seg.value, color: seg.color, frac };
      startAngle = endAngle;
      return arc;
    });
  }, [merged, r, ir, cx, cy, total]);

  if (merged.length === 0) {
    return <div className="dim" style={{ fontSize: 12, padding: 20, textAlign: 'center' }}>No data</div>;
  }

  const fmt = (v) => {
    if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(1)}M`;
    if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`;
    return v.toFixed(2);
  };

  return (
    <div style={{ display: 'flex', alignItems: 'center', gap: 16, flexWrap: 'wrap' }}>
      <svg width={size} height={size} style={{ flexShrink: 0 }}>
        {arcs.map((a, i) => (
          <path key={i} d={a.path} fill={a.color} opacity={0.85}>
            <title>{`${a.label}: ${fmt(a.value)}`}</title>
          </path>
        ))}
        <text x={cx} y={cy - 4} textAnchor="middle" fill="var(--text)" fontSize="14" fontWeight="bold">
          {fmt(total)}
        </text>
        <text x={cx} y={cy + 12} textAnchor="middle" fill="var(--text-dim)" fontSize="10">
          total
        </text>
      </svg>
      <div>
        {arcs.map((a, i) => (
          <div key={i} className="row gap-sm" style={{ fontSize: 12, marginBottom: 3 }}>
            <span style={{ width: 10, height: 10, borderRadius: '50%', background: a.color, flexShrink: 0 }} />
            <span className="mono" style={{ color: 'var(--text-dim)' }}>{a.label}</span>
            <span className="mono">{fmt(a.value)}</span>
            <span className="dim" style={{ fontSize: 11 }}>({(a.frac * 100).toFixed(1)}%)</span>
          </div>
        ))}
      </div>
    </div>
  );
}

// HorizontalBarChart — ranked horizontal bars for categorical data.
// `data` is an array of { label, value, color? } objects, sorted by value descending.
export function HorizontalBarChart({ data, color = 'var(--accent)', height = 12, maxBars = 10 }) {
  const visible = useMemo(() => {
    if (!data || data.length === 0) return [];
    const sorted = [...data].sort((a, b) => b.value - a.value);
    return sorted.slice(0, maxBars);
  }, [data, maxBars]);
  const max = useMemo(() => Math.max(...(visible.map((d) => d.value) || [1]), 1), [visible]);
  if (visible.length === 0) {
    return <div className="dim" style={{ fontSize: 12, padding: 20 }}>No data</div>;
  }
  const fmt = (v) => {
    if (v >= 1_000_000) return `${(v / 1_000_000).toFixed(2)}M`;
    if (v >= 1_000) return `${(v / 1_000).toFixed(1)}k`;
    return v.toFixed(2);
  };
  return (
    <div>
      {visible.map((d, i) => (
        <div key={i} className="row gap-sm" style={{ marginBottom: 4, alignItems: 'center' }}>
          <span className="mono dim" style={{ width: 120, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap', fontSize: 12, textAlign: 'right' }} title={d.label}>
            {d.label}
          </span>
          <div style={{ flex: 1, background: 'var(--surface-2)', borderRadius: 4, height }}>
            <div
              style={{
                width: `${(d.value / max) * 100}%`,
                height,
                background: d.color || color,
                borderRadius: 4,
                opacity: 0.85,
                minWidth: d.value > 0 ? 2 : 0,
              }}
            />
          </div>
          <span className="mono" style={{ width: 80, textAlign: 'right', fontSize: 12 }}>{fmt(d.value)}</span>
        </div>
      ))}
    </div>
  );
}
