import React, { useMemo } from 'react';
import { Spinner, ErrorBanner, EmptyState } from '../Primitives.jsx';

// Built-in thresholds used to draw the guide lines when no decision in the
// window carries a profile_snapshot (mirrors the scorer defaults).
const DEFAULT_THRESHOLDS = { simple_max: 0.15, medium_max: 0.35, complex_max: 0.6 };

// The 7 score dimensions in display order with human labels.
const DIMENSIONS = [
  { key: 'token_count', label: 'Tokens' },
  { key: 'code_presence', label: 'Code' },
  { key: 'reasoning_markers', label: 'Reasoning' },
  { key: 'technical_terms', label: 'Technical' },
  { key: 'simple_indicators', label: 'Simple penalty' },
  { key: 'multi_step_patterns', label: 'Multi-step' },
  { key: 'question_complexity', label: 'Question' },
];

// Tier colors keep the histogram/tier language consistent across the page.
const TIER_COLORS = {
  simple: '#7ca6c9',
  medium: '#7cc9a1',
  complex: '#d9b45c',
  reasoning: '#c97c7c',
};

// tierForBucket maps a histogram bucket index (0..19) to the tier whose range
// covers it, given the thresholds — used to tint bars by tier.
function tierForBucket(bucket, t) {
  const upper = (bucket + 1) * 0.05;
  if (upper <= t.simple_max) return 'simple';
  if (upper <= t.medium_max) return 'medium';
  if (upper <= t.complex_max) return 'complex';
  return 'reasoning';
}

// ScoreHistogram renders the 20-bucket score_total distribution as inline SVG.
// Dashed vertical lines mark the profile thresholds; bars are tinted by tier.
function ScoreHistogram({ histogram, thresholds }) {
  const width = 720;
  const height = 180;
  const pad = { top: 12, right: 8, bottom: 22, left: 30 };
  const plotW = width - pad.left - pad.right;
  const plotH = height - pad.top - pad.bottom;
  const maxCount = Math.max(1, ...histogram.map((b) => b.count));
  const barW = plotW / histogram.length;

  const x = (bucket) => pad.left + bucket * barW;
  // score_total 0..1 → x coordinate.
  const xScore = (score) => pad.left + score * plotW;

  return (
    <svg viewBox={`0 0 ${width} ${height}`} style={{ width: '100%', height: 'auto' }} role="img" aria-label="Score total histogram">
      {/* threshold guide lines */}
      {[thresholds.simple_max, thresholds.medium_max, thresholds.complex_max].map((t, i) => (
        <g key={i}>
          <line
            x1={xScore(t)} x2={xScore(t)} y1={pad.top} y2={pad.top + plotH}
            stroke="currentColor" strokeDasharray="3 3" opacity="0.35"
          />
          <text x={xScore(t) + 3} y={pad.top + 10} fontSize="10" opacity="0.6">{t.toFixed(2)}</text>
        </g>
      ))}
      {histogram.map((b) => {
        const h = (b.count / maxCount) * plotH;
        const tier = tierForBucket(b.bucket, thresholds);
        return (
          <rect
            key={b.bucket}
            x={x(b.bucket) + 1}
            y={pad.top + plotH - h}
            width={Math.max(1, barW - 2)}
            height={h}
            fill={TIER_COLORS[tier] || '#999'}
            opacity="0.85"
          >
            <title>{`[${(b.bucket * 0.05).toFixed(2)}–${((b.bucket + 1) * 0.05).toFixed(2)}): ${b.count}`}</title>
          </rect>
        );
      })}
      {/* axis */}
      <line x1={pad.left} x2={width - pad.right} y1={pad.top + plotH} y2={pad.top + plotH} stroke="currentColor" opacity="0.3" />
      {[0, 0.25, 0.5, 0.75, 1].map((v) => (
        <text key={v} x={xScore(v)} y={height - 6} fontSize="10" textAnchor="middle" opacity="0.6">{v}</text>
      ))}
    </svg>
  );
}

// DimensionBars renders the per-dimension averages as horizontal bars.
function DimensionBars({ averages }) {
  return (
    <div className="stats-grid">
      {DIMENSIONS.map((d) => {
        const v = Math.max(0, Math.min(1, Number(averages?.[d.key] ?? 0)));
        return (
          <div key={d.key} className="stat-card">
            <div className="stat-card__label">{d.label}</div>
            <div className="stat-card__value">{v.toFixed(3)}</div>
            <div style={{ background: 'var(--border, #ddd)', height: 6, borderRadius: 3, overflow: 'hidden' }}>
              <div style={{ background: '#7ca6c9', width: `${v * 100}%`, height: '100%' }} />
            </div>
          </div>
        );
      })}
    </div>
  );
}

// parseChainText turns the serialized fallback_chain jsonb text
// ('["reasoning","complex"]') into a readable arrow form.
function parseChainText(text) {
  try {
    const arr = JSON.parse(text);
    return Array.isArray(arr) ? arr.join(' → ') : text;
  } catch {
    return text;
  }
}

export default function DecisionDistributionTab({ router, decisionStats, onOpenReplay }) {
  const stats = decisionStats.data;

  // Thresholds come from the newest decision's profile_snapshot when present;
  // fall back to the scorer defaults.
  const thresholds = useMemo(() => DEFAULT_THRESHOLDS, []);

  if (decisionStats.loading) return <div className="card" style={{ marginTop: 16 }}><Spinner label="Loading decision distribution…" /></div>;
  if (decisionStats.error) return <div className="card" style={{ marginTop: 16 }}><ErrorBanner error={decisionStats.error} onRetry={decisionStats.reload} /></div>;
  if (!stats || !stats.event_count) {
    return <div className="card" style={{ marginTop: 16 }}><EmptyState title="No routed decisions in this range" /></div>;
  }

  const cause = stats.cause_counts || {};
  const keywordCount = Number(cause.literal_keyword_match || 0);
  const scorerCount = Number(cause.complexity_scorer || 0);
  const causeTotal = keywordCount + scorerCount;
  const mismatch = Number(stats.mismatch_count || 0);

  return (
    <>
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 8 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Score distribution</h3>
          <span className="dim" style={{ fontSize: 12 }}>
            {Number(stats.event_count).toLocaleString()} routed events · thresholds from profile
          </span>
        </div>
        <ScoreHistogram histogram={stats.score_histogram || []} thresholds={thresholds} />
      </div>

      <div className="card" style={{ marginTop: 16 }}>
        <h3 className="card__title">Dimension averages</h3>
        <DimensionBars averages={stats.dimension_averages} />
      </div>

      <div className="card" style={{ marginTop: 16 }}>
        <h3 className="card__title">Decision causes & fallbacks</h3>
        <div className="stats-grid" style={{ marginBottom: 12 }}>
          <div className="stat-card">
            <div className="stat-card__label">Keyword rules</div>
            <div className="stat-card__value">{keywordCount.toLocaleString()}</div>
            <div className="stat-card__hint">{causeTotal ? `${Math.round((keywordCount / causeTotal) * 100)}% of decisions` : '—'}</div>
          </div>
          <div className="stat-card">
            <div className="stat-card__label">Complexity scorer</div>
            <div className="stat-card__value">{scorerCount.toLocaleString()}</div>
            <div className="stat-card__hint">{causeTotal ? `${Math.round((scorerCount / causeTotal) * 100)}% of decisions` : '—'}</div>
          </div>
          <div className="stat-card">
            <div className="stat-card__label">Tier mismatches</div>
            <div className="stat-card__value">{mismatch.toLocaleString()}</div>
            <div className="stat-card__hint">
              {mismatch > 0 ? (
                <a
                  href="#"
                  onClick={(e) => { e.preventDefault(); onOpenReplay?.(); }}
                >
                  inspect in Replay →
                </a>
              ) : 'effective ≠ mapping'}
            </div>
          </div>
        </div>
        {(stats.fallback_chains || []).length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr><th>Fallback chain</th><th style={{ textAlign: 'right' }}>Count</th></tr>
              </thead>
              <tbody>
                {stats.fallback_chains.map((c, i) => (
                  <tr key={i}>
                    <td className="mono">{parseChainText(c.chain) || '—'}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{Number(c.count).toLocaleString()}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>
    </>
  );
}
