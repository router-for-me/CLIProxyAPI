import React, { useState, useMemo } from 'react';
import { getAutoRouterProfile, putAutoRouterProfile, simulateAutoRouterProfile } from '../../api/client.js';
import { useAsync } from '../../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState } from '../Primitives.jsx';
import { useToast } from '../Toast.jsx';

// The 7 weight fields in canonical order (must all be present — validation
// rejects partial weight sets).
const WEIGHT_FIELDS = [
  { key: 'token_count', label: 'Tokens' },
  { key: 'code_presence', label: 'Code' },
  { key: 'reasoning_markers', label: 'Reasoning' },
  { key: 'technical_terms', label: 'Technical' },
  { key: 'simple_indicators', label: 'Simple penalty' },
  { key: 'multi_step_patterns', label: 'Multi-step' },
  { key: 'question_complexity', label: 'Question' },
];

const TIER_OPTIONS = ['simple', 'medium', 'complex', 'reasoning'];

const DEFAULT_THRESHOLDS = { simple_max: 0.15, medium_max: 0.35, complex_max: 0.6 };

// emptyCandidate builds a fresh editable copy of a profile config.
function emptyCandidate(config) {
  const weights = {};
  for (const f of WEIGHT_FIELDS) weights[f.key] = Number(config?.weights?.[f.key] ?? 0);
  return {
    thresholds: {
      simple_max: Number(config?.thresholds?.simple_max ?? DEFAULT_THRESHOLDS.simple_max),
      medium_max: Number(config?.thresholds?.medium_max ?? DEFAULT_THRESHOLDS.medium_max),
      complex_max: Number(config?.thresholds?.complex_max ?? DEFAULT_THRESHOLDS.complex_max),
    },
    weights,
    keyword_tier_rules: (config?.keyword_tier_rules || []).map((r) => ({
      id: r.id || '',
      tier: r.tier || 'complex',
      keywordsText: (r.keywords || []).join(', '),
    })),
  };
}

// candidateToPayload converts the editable form state into a ProfileConfig
// payload (keywords split back into arrays, empty rows dropped).
function candidateToPayload(candidate) {
  return {
    thresholds: candidate.thresholds,
    weights: candidate.weights,
    keyword_tier_rules: candidate.keyword_tier_rules
      .filter((r) => r.id.trim())
      .map((r) => ({
        id: r.id.trim(),
        tier: r.tier,
        keywords: r.keywordsText.split(',').map((k) => k.trim()).filter(Boolean),
      })),
  };
}

export default function SimulationTab({ router, range, apiKeyId, onOpenReplay }) {
  const toast = useToast();
  const [candidate, setCandidate] = useState(null);
  const [simulating, setSimulating] = useState(false);
  const [applying, setApplying] = useState(false);
  const [result, setResult] = useState(null);
  const [simError, setSimError] = useState(null);

  // Load the active profile (built-in defaults when the router has none).
  const profile = useAsync(() => getAutoRouterProfile(router.id), [router.id], {
    // A missing profile row surfaces as the default profile from the server;
    // any other error surfaces through the banner below.
  });

  const activeConfig = profile.data?.config;
  const candidateReady = candidate !== null;
  const seededFor = useMemo(() => `${router.id}:${profile.data?.hash || 'default'}`, [router.id, profile.data?.hash]);
  const [seededKey, setSeededKey] = useState('');

  // Seed the editable candidate once the active profile has loaded (and reseed
  // when the router or the profile hash changes).
  if (activeConfig && seededKey !== seededFor && !candidateReady) {
    setCandidate(emptyCandidate(activeConfig));
    setSeededKey(seededFor);
  }

  async function handleSimulate() {
    if (!candidateReady) return;
    setSimulating(true);
    setSimError(null);
    try {
      const res = await simulateAutoRouterProfile(router.id, {
        config: candidateToPayload(candidate),
        from: range.from,
        to: range.to,
        api_key_id: apiKeyId || undefined,
      });
      setResult(res);
    } catch (err) {
      setSimError(err?.payload?.message || err?.message || 'Simulation failed');
    }
    setSimulating(false);
  }

  async function handleApply() {
    if (!candidateReady) return;
    if (!window.confirm('Apply this scoring profile? New requests will be classified with it immediately.')) return;
    setApplying(true);
    try {
      await putAutoRouterProfile(router.id, candidateToPayload(candidate));
      toast.success('Scoring profile applied');
      setResult(null); // stale: profile changed, simulation must be re-run
      profile.reload();
      setCandidate(null);
      setSeededKey('');
    } catch (err) {
      toast.error(err?.payload?.message || err?.message || 'Failed to apply profile');
    }
    setApplying(false);
  }

  function updateCandidate(patch) {
    setCandidate((prev) => ({ ...prev, ...patch }));
  }

  function updateRule(index, patch) {
    setCandidate((prev) => ({
      ...prev,
      keyword_tier_rules: prev.keyword_tier_rules.map((r, i) => (i === index ? { ...r, ...patch } : r)),
    }));
  }

  if (profile.loading) return <div className="card" style={{ marginTop: 16 }}><Spinner label="Loading scoring profile…" /></div>;
  if (profile.error) return <div className="card" style={{ marginTop: 16 }}><ErrorBanner error={profile.error} onRetry={profile.reload} /></div>;

  const movedPct = result && result.events > 0 ? Math.round((result.moved_count / result.events) * 100) : 0;

  return (
    <>
      <div className="card" style={{ marginTop: 16 }}>
        <div className="row row--between" style={{ marginBottom: 8 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Candidate scoring profile</h3>
          <span className="dim" style={{ fontSize: 12 }}>
            active: {profile.data?.is_default ? 'built-in defaults' : `v${profile.data?.version}`}
          </span>
        </div>
        <div className="main__subtitle" style={{ marginBottom: 12 }}>
          Edit thresholds, weights, and keyword rules, then simulate against the
          stored decisions of the selected range. Nothing is written until you
          press Apply.
        </div>

        {candidateReady && (
          <>
            <div className="grid grid--3" style={{ marginBottom: 12 }}>
              {['simple_max', 'medium_max', 'complex_max'].map((k) => (
                <div className="form__row" key={k}>
                  <label className="form__label">{k.replace('_', ' ')}</label>
                  <input
                    type="number" min="0" max="1" step="0.01"
                    value={candidate.thresholds[k]}
                    onChange={(e) => updateCandidate({ thresholds: { ...candidate.thresholds, [k]: Number(e.target.value) } })}
                  />
                </div>
              ))}
            </div>

            <div className="grid grid--4" style={{ marginBottom: 12 }}>
              {WEIGHT_FIELDS.map((f) => (
                <div className="form__row" key={f.key}>
                  <label className="form__label">{f.label}</label>
                  <input
                    type="number" min="0" step="0.01"
                    value={candidate.weights[f.key]}
                    onChange={(e) => updateCandidate({ weights: { ...candidate.weights, [f.key]: Number(e.target.value) } })}
                  />
                </div>
              ))}
            </div>

            <h4 style={{ margin: '8px 0' }}>Keyword tier rules</h4>
            {candidate.keyword_tier_rules.length === 0 && (
              <div className="dim" style={{ marginBottom: 8 }}>No rules.</div>
            )}
            {candidate.keyword_tier_rules.map((r, i) => (
              <div className="row gap-sm" key={i} style={{ marginBottom: 6, alignItems: 'center' }}>
                <input
                  placeholder="rule id" style={{ maxWidth: 160 }}
                  value={r.id} onChange={(e) => updateRule(i, { id: e.target.value })}
                />
                <select value={r.tier} onChange={(e) => updateRule(i, { tier: e.target.value })}>
                  {TIER_OPTIONS.map((t) => <option key={t} value={t}>{t}</option>)}
                </select>
                <input
                  placeholder="keywords, comma separated" style={{ flex: 1 }}
                  value={r.keywordsText} onChange={(e) => updateRule(i, { keywordsText: e.target.value })}
                />
                <button
                  onClick={() => updateCandidate({ keyword_tier_rules: candidate.keyword_tier_rules.filter((_, j) => j !== i) })}
                >
                  ✕
                </button>
              </div>
            ))}
            <button
              onClick={() => updateCandidate({ keyword_tier_rules: [...candidate.keyword_tier_rules, { id: '', tier: 'complex', keywordsText: '' }] })}
              style={{ marginBottom: 12 }}
            >
              + Add rule
            </button>

            <div className="row gap-sm">
              <button className="primary" onClick={handleSimulate} disabled={simulating}>
                {simulating ? 'Simulating…' : 'Simulate over range'}
              </button>
              <button onClick={handleApply} disabled={applying || !result}>
                {applying ? 'Applying…' : 'Apply'}
              </button>
              {!result && <span className="dim" style={{ fontSize: 12 }}>run a simulation before applying</span>}
            </div>
          </>
        )}
      </div>

      {simError && <ErrorBanner error={{ message: simError }} />}

      {result && (
        <div className="card" style={{ marginTop: 16 }}>
          <div className="row row--between" style={{ marginBottom: 8 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Simulation result</h3>
            <span className="dim" style={{ fontSize: 12 }}>
              {Number(result.events).toLocaleString()} events · {movedPct}% would move
              {result.truncated ? ` · truncated (${Number(result.total_snapshot_events).toLocaleString()} total)` : ''}
            </span>
          </div>

          {result.unsimulable_count > 0 && (
            <div className="card" style={{ background: 'var(--warning-bg, #fdf3d8)', marginBottom: 12, padding: '8px 12px' }}>
              <strong>{result.unsimulable_count} rule(s) unsimulable</strong> — evaluated on stored
              signals only; their matches on request text could not be replayed:
              {' '}{result.unsimulable_rules.join(', ')}
            </div>
          )}

          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>From tier</th>
                  <th>To tier</th>
                  <th style={{ textAlign: 'right' }}>Events</th>
                  <th>Sample requests</th>
                </tr>
              </thead>
              <tbody>
                {(result.moves || []).length === 0 && (
                  <tr><td colSpan="4" className="dim">No tier would change.</td></tr>
                )}
                {(result.moves || []).map((m, i) => (
                  <tr key={i}>
                    <td className="mono">{m.from}</td>
                    <td className="mono">{m.to}</td>
                    <td className="mono" style={{ textAlign: 'right' }}>{Number(m.count).toLocaleString()}</td>
                    <td>
                      {(m.sample_request_ids || []).map((id, j) => (
                        <span key={j}>
                          {j > 0 && ', '}
                          <a
                            href="#"
                            className="mono"
                            onClick={(e) => { e.preventDefault(); onOpenReplay?.(id); }}
                            title="Open in Replay"
                          >
                            {id.slice(0, 8)}…
                          </a>
                        </span>
                      ))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          {/* Confusion matrix */}
          <h4 style={{ margin: '16px 0 8px' }}>Confusion matrix (from × to)</h4>
          <ConfusionMatrix cells={result.confusion || []} />
        </div>
      )}
    </>
  );
}

// ConfusionMatrix renders the from × to grid (tiers in canonical order).
function ConfusionMatrix({ cells }) {
  const lookup = useMemo(() => {
    const map = new Map();
    for (const c of cells) map.set(`${c.from}>${c.to}`, c.count);
    return map;
  }, [cells]);
  return (
    <div style={{ overflowX: 'auto' }}>
      <table className="table">
        <thead>
          <tr>
            <th>from \ to</th>
            {TIER_OPTIONS.map((t) => <th key={t} style={{ textAlign: 'right' }}>{t}</th>)}
          </tr>
        </thead>
        <tbody>
          {TIER_OPTIONS.map((from) => (
            <tr key={from}>
              <td className="mono">{from}</td>
              {TIER_OPTIONS.map((to) => {
                const n = lookup.get(`${from}>${to}`);
                const bold = from === to;
                return (
                  <td key={to} className="mono" style={{ textAlign: 'right', fontWeight: bold ? 700 : 400 }}>
                    {n === undefined ? '·' : Number(n).toLocaleString()}
                  </td>
                );
              })}
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
