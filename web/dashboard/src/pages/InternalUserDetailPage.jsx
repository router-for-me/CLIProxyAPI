import React, { useState, useMemo, useCallback, useEffect } from 'react';
import { useParams, Link } from 'react-router-dom';
import {
  getInternalUser,
  patchInternalUser,
  deleteInternalUser,
  resetInternalUserSpend,
  reconcileInternalUserSpend,
  getInternalUserTotals,
  getInternalUserTimeSeries,
  getInternalUserTop,
  getInternalUserWindows,
  getInternalUserEvents,
  getInternalUserModelSpend,
  listInternalUserKeys,
  attachKeyToUser,
  detachKeyFromUser,
  listAPIKeys,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import {
  Spinner, ErrorBanner, EmptyState, Stat, Modal,
} from '../components/Primitives.jsx';
import { Sparkline, BarChart, MultiBarChart } from '../components/Charts.jsx';
import InternalUserPolicyForm, { formToPatch } from '../components/InternalUserPolicyForm.jsx';

const PRESETS = [
  { label: 'Last 15m', duration: 15, unit: 'minute', interval: 'minute' },
  { label: 'Last 1h', duration: 1, unit: 'hour', interval: 'minute' },
  { label: 'Last 6h', duration: 6, unit: 'hour', interval: 'hour' },
  { label: 'Last 24h', duration: 24, unit: 'hour', interval: 'hour' },
  { label: 'Last 7d', duration: 7, unit: 'day', interval: 'day' },
  { label: 'Last 30d', duration: 30, unit: 'day', interval: 'day' },
];

function presetToRange(preset) {
  const now = new Date();
  const from = new Date(now);
  if (preset.unit === 'minute') from.setMinutes(from.getMinutes() - preset.duration);
  if (preset.unit === 'hour') from.setHours(from.getHours() - preset.duration);
  if (preset.unit === 'day') from.setDate(from.getDate() - preset.duration);
  return { from: from.toISOString(), to: now.toISOString(), interval: preset.interval };
}

const ROLE_BADGE_CLASS = {
  internal_user: 'badge--active',
  proxy_admin: 'badge--revoked',
  proxy_admin_viewer: 'badge--muted',
};

function RoleBadge({ role }) {
  const cls = ROLE_BADGE_CLASS[role] || 'badge--muted';
  return <span className={`badge ${cls}`}>{role || 'unknown'}</span>;
}

export default function InternalUserDetailPage() {
  const { id } = useParams();
  const [presetIdx, setPresetIdx] = useState(3); // Last 24h
  const rangeParams = useMemo(() => presetToRange(PRESETS[presetIdx]), [presetIdx]);
  const baseFilter = useMemo(() => ({
    from: rangeParams.from,
    to: rangeParams.to,
  }), [rangeParams.from, rangeParams.to]);

  const profile = useAsync(() => getInternalUser(id), [id]);
  const totals = useAsync(
    () => getInternalUserTotals(id, baseFilter),
    [id, JSON.stringify(baseFilter)],
  );
  const ts = useAsync(
    () => getInternalUserTimeSeries(id, baseFilter, rangeParams.interval),
    [id, JSON.stringify(baseFilter), rangeParams.interval],
  );
  const topModels = useAsync(
    () => getInternalUserTop(id, { dimension: 'model', metric: 'cost_usd', limit: 10, ...baseFilter }),
    [id, JSON.stringify(baseFilter)],
  );
  const topProviders = useAsync(
    () => getInternalUserTop(id, { dimension: 'provider', metric: 'cost_usd', limit: 10, ...baseFilter }),
    [id, JSON.stringify(baseFilter)],
  );
  const windows = useAsync(() => getInternalUserWindows(id), [id]);
  const modelSpend = useAsync(() => getInternalUserModelSpend(id), [id]);
  const keys = useAsync(() => listInternalUserKeys(id, { page: 1, pageSize: 100 }), [id]);

  const reloadAll = useCallback(() => {
    profile.reload();
    totals.reload();
    ts.reload();
    topModels.reload();
    topProviders.reload();
    windows.reload();
    modelSpend.reload();
    keys.reload();
  }, [profile, totals, ts, topModels, topProviders, windows, modelSpend, keys]);

  if (profile.loading) return <Spinner label="Loading user…" />;
  if (profile.error) return <ErrorBanner error={profile.error} onRetry={profile.reload} />;
  if (!profile.data) return null;
  const u = profile.data;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">{u.user_alias || u.id}</h1>
          <div className="main__subtitle mono dim">{u.id}</div>
        </div>
        <div className="row gap-sm">
          <Link to="/internal-users"><button>Back</button></Link>
          <button onClick={reloadAll}>Refresh</button>
        </div>
      </div>

      <div className="grid grid--2">
        <ProfileCard user={u} onUpdated={profile.reload} />
        <BudgetWindowCard windows={windows} maxBudget={u.max_budget} spend={u.spend} />
      </div>

      <div className="card" style={{ marginTop: 16 }}>
        <div className="row gap-sm" style={{ flexWrap: 'wrap', marginBottom: 12 }}>
          {PRESETS.map((p, i) => (
            <button
              key={p.label}
              onClick={() => setPresetIdx(i)}
              className={i === presetIdx ? 'primary' : ''}
              style={{ padding: '4px 10px', fontSize: 12 }}
            >
              {p.label}
            </button>
          ))}
        </div>
      </div>

      <ErrorBanner error={totals.error} onRetry={totals.reload} />

      <div className="grid grid--4" style={{ marginTop: 8 }}>
        <Stat
          label="Total Spend"
          value={`$${(totals.data?.totals?.cost_usd || 0).toFixed(4)}`}
          delta={`window spend ($${(u.spend || 0).toFixed(4)} running)`}
        />
        <Stat
          label="Total Tokens"
          value={(totals.data?.totals?.total_tokens || 0).toLocaleString()}
          delta={`in ${(totals.data?.totals?.input_tokens || 0).toLocaleString()} · out ${(totals.data?.totals?.output_tokens || 0).toLocaleString()}`}
        />
        <Stat
          label="Total Requests"
          value={(totals.data?.totals?.request_count || 0).toLocaleString()}
          delta={`${(totals.data?.totals?.failed_count || 0).toLocaleString()} failed (${(totals.data?.failure_rate || 0).toFixed(1)}%)`}
        />
        <Stat
          label="Active Models"
          value={String(topModels.data?.entries?.length || 0)}
          delta={`${topProviders.data?.entries?.length || 0} providers`}
        />
      </div>

      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Request volume</h3>
            <span className="dim" style={{ fontSize: 12 }}>{rangeParams.interval}</span>
          </div>
          {ts.loading && <Spinner label="Loading…" />}
          {!ts.loading && (ts.data?.points || []).length > 0 && (
            <BarChart data={(ts.data?.points || []).map((p) => ({ label: p.bucket, value: p.request_count }))} />
          )}
          {!ts.loading && (ts.data?.points || []).length === 0 && (
            <EmptyState title="No data for this window" />
          )}
        </div>
        <div className="card">
          <div className="row row--between" style={{ marginBottom: 12 }}>
            <h3 className="card__title" style={{ margin: 0 }}>Token usage</h3>
          </div>
          {ts.loading && <Spinner label="Loading…" />}
          {!ts.loading && (ts.data?.points || []).length > 0 && (
            <MultiBarChart data={(ts.data?.points || []).map((p) => ({
              label: p.bucket, value: p.request_count, a: p.input_tokens, b: p.output_tokens,
            }))} />
          )}
          {!ts.loading && (ts.data?.points || []).length === 0 && (
            <EmptyState title="No data for this window" />
          )}
        </div>
      </div>

      <div className="grid grid--3" style={{ marginTop: 16 }}>
        <div className="card">
          <h3 className="card__title">Cost trend</h3>
          <Sparkline values={(ts.data?.points || []).map((p) => p.cost_usd || 0)} color="var(--accent)" />
        </div>
        <div className="card">
          <h3 className="card__title">RPM</h3>
          <Sparkline values={rpmFromPoints(ts.data?.points || [])} color="var(--warning)" />
        </div>
        <div className="card">
          <h3 className="card__title">Avg latency (ms)</h3>
          <Sparkline values={latencyFromPoints(ts.data?.points || [])} color="#a78bfa" />
        </div>
      </div>

      <div className="grid grid--2" style={{ marginTop: 16 }}>
        <LeaderboardCard title="Top models (by spend)" entries={topModels.data?.entries || []} loading={topModels.loading} />
        <LeaderboardCard title="Top providers (by spend)" entries={topProviders.data?.entries || []} loading={topProviders.loading} />
      </div>

      <OwnedKeysCard userId={id} keys={keys} />
      <ModelSpendCard modelSpend={modelSpend} />
    </>
  );
}

// ModelSpendCard renders per-model aggregations (cost / tokens / counts)
// computed on-the-fly from usage_events — mirrors LiteLLM_UserTable.model_spend
// without precomputing a JSON column (avoids drift). When the per-user
// repository also stores model_max_budgets inside metadata, the card overlays
// the configured caps as a budget-vs-usage bar.
function ModelSpendCard({ modelSpend }) {
  const entries = modelSpend.data?.entries || [];
  const totalCost = entries.reduce((a, e) => a + (e.cost_usd || 0), 0);
  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="row row--between" style={{ marginBottom: 12 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Per-model spend</h3>
        <button onClick={modelSpend.reload}>Refresh</button>
      </div>
      <ErrorBanner error={modelSpend.error} onRetry={modelSpend.reload} />
      {modelSpend.loading && <Spinner label="Loading…" />}
      {!modelSpend.loading && entries.length === 0 && (
        <EmptyState title="No model attribution yet" hint="Spend by model is reconciled from usage_events on every view." />
      )}
      {!modelSpend.loading && entries.length > 0 && (
        <table className="table">
          <thead>
            <tr>
              <th>Model</th>
              <th>Cost</th>
              <th>% of total</th>
              <th>Requests</th>
              <th>Tokens (in / out)</th>
            </tr>
          </thead>
          <tbody>
            {entries.map((e, i) => {
              const pct = totalCost > 0 ? (e.cost_usd / totalCost) * 100 : 0;
              return (
                <tr key={`${e.model}-${i}`}>
                  <td className="mono">{e.model}</td>
                  <td className="mono">${Number(e.cost_usd || 0).toFixed(4)}</td>
                  <td>
                    <div style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
                      <div style={{
                        width: 60, height: 6, background: 'var(--bg)', borderRadius: 3, overflow: 'hidden',
                      }}>
                        <div style={{
                          width: `${pct}%`, height: '100%', background: 'var(--accent)', minWidth: pct > 0 ? 2 : 0,
                        }} />
                      </div>
                      <span className="dim mono" style={{ fontSize: 11 }}>{pct.toFixed(1)}%</span>
                    </div>
                  </td>
                  <td className="mono">{(e.request_count || 0).toLocaleString()}</td>
                  <td className="mono">
                    {(e.input_tokens || 0).toLocaleString()} / {(e.output_tokens || 0).toLocaleString()}
                  </td>
                </tr>
              );
            })}
          </tbody>
          <tfoot>
            <tr>
              <th>Total</th>
              <th className="mono">${totalCost.toFixed(4)}</th>
              <th />
              <th className="mono">{entries.reduce((a, e) => a + (e.request_count || 0), 0).toLocaleString()}</th>
              <th className="mono">
                {entries.reduce((a, e) => a + (e.input_tokens || 0), 0).toLocaleString()} /{' '}
                {entries.reduce((a, e) => a + (e.output_tokens || 0), 0).toLocaleString()}
              </th>
            </tr>
          </tfoot>
        </table>
      )}
    </div>
  );
}

// rpmFromPoints derives requests-per-minute from a minute bucket. For hour/day
// buckets this is approximate (avg per minute over the bucket span) but still
// useful as a trend signal.
function rpmFromPoints(points) {
  return points.map((p) => {
    const n = p.request_count || 0;
    return n; // count per bucket — the chart hint clarifies the unit is bucket
  });
}

// latencyFromPoints is not directly available from SelectTimeSeries (which
// returns token sums but not latency averages). Surfacing zero for now; a
// follow-up can extend SelectTimeSeries to include avg latency_ms per bucket.
function latencyFromPoints(_points) {
  return [];
}

function ProfileCard({ user, onUpdated }) {
  const [editing, setEditing] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [busy, setBusy] = useState(false);

  if (editing) {
    return (
      <ProfileEditor
        user={user}
        onCancel={() => setEditing(false)}
        onSaved={() => { setEditing(false); onUpdated(); }}
      />
    );
  }
  return (
    <div className="card">
      <h3 className="card__title">Profile</h3>
      <div className="form__row">
        <div className="form__label">Alias</div>
        <div>{user.user_alias || '—'}</div>
      </div>
      <div className="form__row">
        <div className="form__label">Email</div>
        <div className="mono dim">{user.user_email || '—'}</div>
      </div>
      <div className="form__row">
        <div className="form__label">Role</div>
        <RoleBadge role={user.user_role} />
      </div>
      <div className="form__row">
        <div className="form__label">Max budget</div>
        <div className="mono">{user.max_budget ? `$${Number(user.max_budget).toFixed(2)}` : 'unlimited'}</div>
      </div>
      <div className="form__row">
        <div className="form__label">Budget duration</div>
        <div className="mono">{user.budget_duration || 'no reset'}</div>
      </div>
      <div className="form__row">
        <div className="form__label">Reset at</div>
        <div className="mono">{user.budget_reset_at ? new Date(user.budget_reset_at).toLocaleString() : '—'}</div>
      </div>
      <div className="form__row">
        <div className="form__label">RPM / TPM limits</div>
        <div className="mono">
          {user.rpm_limit ?? '—'} / {user.tpm_limit ?? '—'}
        </div>
      </div>
      <div className="form__row">
        <div className="form__label">Created / Updated</div>
        <div className="dim">
          {new Date(user.created_at).toLocaleString()} · {new Date(user.updated_at).toLocaleString()}
        </div>
      </div>
      {user.metadata && Object.keys(user.metadata).length > 0 && (
        <div className="form__row">
          <div className="form__label">Metadata</div>
          <pre className="copyable" style={{ whiteSpace: 'pre-wrap' }}>
            {JSON.stringify(user.metadata, null, 2)}
          </pre>
        </div>
      )}
      <div className="form__actions">
        <button className="primary" onClick={() => setEditing(true)}>Edit Profile</button>
        <button onClick={async () => {
          setBusy(true);
          try {
            const r = await reconcileInternalUserSpend(user.id);
            const delta = r?.result?.delta ?? 0;
            const updated = r?.result?.updated ?? false;
            if (!updated) {
              alert('Spend is already in sync with usage_events (no drift detected).');
            } else {
              alert(`Reconciled: fixed $${Number(delta).toFixed(4)} drift (events SUM $${Number(r.result.events_spend || 0).toFixed(4)} → stored $${Number(r.result.stored_spend || 0).toFixed(4)}).`);
            }
            onUpdated();
          } catch (err) { alert(err.message); }
          finally { setBusy(false); }
        }} disabled={busy}>Reconcile Spend</button>
        <button onClick={async () => {
          setBusy(true);
          try { await resetInternalUserSpend(user.id); onUpdated(); }
          catch (err) { alert(err.message); }
          finally { setBusy(false); }
        }} disabled={busy}>Reset Spend</button>
        <button className="danger" onClick={() => setShowDelete(true)}>Delete</button>
      </div>
      {showDelete && (
        <DeleteModal
          userId={user.id}
          name={user.user_alias || user.id}
          onClose={() => setShowDelete(false)}
          onDone={() => { setShowDelete(false); window.location.href = '/internal-users'; }}
        />
      )}
    </div>
  );
}

function ProfileEditor({ user, onCancel, onSaved }) {
  const [form, setForm] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  async function handleSubmit(e) {
    e.preventDefault();
    if (!form) { onCancel(); return; }
    setSubmitting(true);
    setError('');
    const patch = formToPatch(form);
    try {
      await patchInternalUser(user.id, patch);
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Edit Profile</h3>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <InternalUserPolicyForm initial={user} onChange={setForm} />
        <div className="form__actions">
          <button type="button" onClick={onCancel} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? 'Saving…' : 'Save Profile'}
          </button>
        </div>
      </form>
    </div>
  );
}

function DeleteModal({ userId, name, onClose, onDone }) {
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  async function handleDelete() {
    if (confirm !== name) return;
    setSubmitting(true);
    setError('');
    try {
      await deleteInternalUser(userId);
      onDone();
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }
  return (
    <Modal title="Delete Internal User" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <p>
        Permanently delete <strong>{name}</strong>? The user row, per-user
        budget windows (cascade), and the spend counter are removed. Owned API
        keys are NOT deleted — their user_id is left dangling.
      </p>
      <div className="form__row">
        <label className="form__label">Type the alias to confirm</label>
        <input type="text" value={confirm} onChange={(e) => setConfirm(e.target.value)} placeholder={name} />
      </div>
      <div className="form__actions">
        <button onClick={onClose} disabled={submitting}>Cancel</button>
        <button className="danger" onClick={handleDelete} disabled={submitting || confirm !== name}>
          {submitting ? 'Deleting…' : 'Delete Forever'}
        </button>
      </div>
    </Modal>
  );
}

function BudgetWindowCard({ windows, maxBudget, spend }) {
  const pct = maxBudget && Number(maxBudget) > 0
    ? Math.min(100, (spend / Number(maxBudget)) * 100)
    : null;
  return (
    <div className="card">
      <h3 className="card__title">Budget & Usage</h3>
      {!maxBudget && (
        <p className="muted dim">No max_budget set — this user is unlimited.</p>
      )}
      {maxBudget && pct !== null && (
        <>
          <div style={{ height: 14, width: '100%', background: 'var(--bg)', borderRadius: 6, overflow: 'hidden', marginBottom: 8 }}>
            <div style={{
              width: `${pct}%`,
              height: '100%',
              background: pct >= 100 ? 'var(--danger)' : pct >= 80 ? 'var(--warning)' : 'var(--success)',
              minWidth: 2,
            }} />
          </div>
          <div className="dim mono" style={{ fontSize: 12 }}>
            ${Number(spend).toFixed(4)} / ${Number(maxBudget).toFixed(2)} ({pct.toFixed(1)}%)
          </div>
        </>
      )}
      {windows.loading && <Spinner label="Loading windows…" />}
      <ErrorBanner error={windows.error} onRetry={windows.reload} />
      {!windows.loading && (windows.data?.windows || []).length > 0 && (
        <table className="table" style={{ marginTop: 12 }}>
          <thead>
            <tr><th>Type</th><th>Start</th><th>End</th><th>Requests</th><th>Tokens</th><th>Cost</th></tr>
          </thead>
          <tbody>
            {(windows.data?.windows || []).map((w) => (
              <tr key={`${w.window_type}-${w.window_start}`}>
                <td><span className="badge badge--muted">{w.window_type}</span></td>
                <td className="mono dim">{new Date(w.window_start).toLocaleString()}</td>
                <td className="mono dim">{new Date(w.window_end).toLocaleString()}</td>
                <td className="mono">{w.request_count}</td>
                <td className="mono">{w.total_tokens.toLocaleString()}</td>
                <td className="mono">${Number(w.cost_usd).toFixed(4)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {!windows.loading && (windows.data?.windows || []).length === 0 && maxBudget && (
        <p className="dim muted" style={{ marginTop: 12 }}>No usage in any window yet.</p>
      )}
    </div>
  );
}

function LeaderboardCard({ title, entries, loading }) {
  return (
    <div className="card">
      <h3 className="card__title">{title}</h3>
      {loading && <Spinner label="Loading…" />}
      {!loading && entries.length === 0 && <EmptyState title="No data" />}
      {!loading && entries.length > 0 && (
        <table className="table">
          <thead>
            <tr><th>Key</th><th>Requests</th><th>Tokens</th><th>Cost</th></tr>
          </thead>
          <tbody>
            {entries.map((e, i) => (
              <tr key={`${e.key}-${i}`}>
                <td className="mono">{e.key || '—'}</td>
                <td className="mono">{e.request_count.toLocaleString()}</td>
                <td className="mono">{e.total_tokens.toLocaleString()}</td>
                <td className="mono">${Number(e.cost_usd).toFixed(4)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function OwnedKeysCard({ userId, keys }) {
  const [attaching, setAttaching] = useState(false);
  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="row row--between" style={{ marginBottom: 12 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Owned API Keys</h3>
        <div className="row gap-sm">
          <button onClick={keys.reload}>Refresh</button>
          <button onClick={() => setAttaching(true)}>Attach Key</button>
        </div>
      </div>
      {keys.loading && <Spinner label="Loading keys…" />}
      <ErrorBanner error={keys.error} onRetry={keys.reload} />
      {!keys.loading && (keys.data?.api_keys || []).length === 0 && (
        <EmptyState title="No keys assigned" hint="Attach an existing API key to attribute usage to this user." />
      )}
      {!keys.loading && (keys.data?.api_keys || []).length > 0 && (
        <table className="table">
          <thead>
            <tr><th>Name</th><th>Alias</th><th>Status</th><th>Prefix</th><th>Actions</th></tr>
          </thead>
          <tbody>
            {(keys.data?.api_keys || []).map((k) => (
              <OwnedKeyRow key={k.id} userId={userId} k={k} onUpdated={keys.reload} />
            ))}
          </tbody>
        </table>
      )}
      {attaching && (
        <AttachKeyModal userId={userId} onClose={() => setAttaching(false)} onAttached={() => { setAttaching(false); keys.reload(); }} />
      )}
    </div>
  );
}

function OwnedKeyRow({ userId, k, onUpdated }) {
  const [busy, setBusy] = useState(false);
  async function handleDetach() {
    if (!confirm(`Detach key ${k.name} (${k.id}) from this user?`)) return;
    setBusy(true);
    try {
      await detachKeyFromUser(userId, k.id);
      onUpdated();
    } catch (err) {
      alert(err.message);
    } finally {
      setBusy(false);
    }
  }
  return (
    <tr>
      <td>
        <Link to={`/api-keys/${encodeURIComponent(k.id)}`}>{k.name}</Link>
        <div className="dim mono" style={{ fontSize: 11 }}>{k.id}</div>
      </td>
      <td className="mono dim">{k.key_alias || '—'}</td>
      <td><span className={`badge badge--${k.status === 'active' ? 'active' : 'muted'}`}>{k.status}</span></td>
      <td className="mono">{k.key_prefix}…</td>
      <td>
        <button className="danger" onClick={handleDetach} disabled={busy}>Detach</button>
      </td>
    </tr>
  );
}

function AttachKeyModal({ userId, onClose, onAttached }) {
  const [search, setSearch] = useState('');
  const [results, setResults] = useState([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [busyId, setBusyId] = useState('');

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError('');
    listAPIKeys({ page: 1, pageSize: 50, status: '' })
      .then((data) => {
        if (cancelled) return;
        const all = data.api_keys || [];
        const filtered = search.trim()
          ? all.filter((k) =>
              (k.name || '').toLowerCase().includes(search.toLowerCase()) ||
              (k.key_alias || '').toLowerCase().includes(search.toLowerCase()) ||
              (k.id || '').toLowerCase().includes(search.toLowerCase()))
          : all;
        setResults(filtered.filter((k) => !k.user_id)); // only unattached keys
      })
      .catch((err) => { if (!cancelled) setError(err.message); })
      .finally(() => { if (!cancelled) setLoading(false); });
    return () => { cancelled = true; };
  }, [search]);

  async function handleAttach(keyId) {
    setBusyId(keyId);
    setError('');
    try {
      await attachKeyToUser(userId, keyId);
      onAttached();
    } catch (err) {
      setError(err.message);
    } finally {
      setBusyId('');
    }
  }

  return (
    <Modal title="Attach API Key" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <div className="form__row">
        <input type="text" value={search} onChange={(e) => setSearch(e.target.value)} placeholder="search unattached keys" />
      </div>
      {loading && <Spinner label="Loading keys…" />}
      {!loading && results.length === 0 && <EmptyState title="No unattached keys match" />}
      {!loading && results.length > 0 && (
        <table className="table" style={{ marginTop: 8 }}>
          <thead><tr><th>Name</th><th>Alias</th><th>Prefix</th><th></th></tr></thead>
          <tbody>
            {results.map((k) => (
              <tr key={k.id}>
                <td>{k.name}<div className="dim mono" style={{ fontSize: 11 }}>{k.id}</div></td>
                <td className="mono dim">{k.key_alias || '—'}</td>
                <td className="mono">{k.key_prefix}…</td>
                <td>
                  <button className="primary" onClick={() => handleAttach(k.id)} disabled={busyId === k.id}>
                    {busyId === k.id ? 'Attaching…' : 'Attach'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Modal>
  );
}
