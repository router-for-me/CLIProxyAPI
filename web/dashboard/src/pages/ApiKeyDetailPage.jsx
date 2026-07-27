import React, { useState, useEffect } from 'react';
import { useParams, useNavigate, Link } from 'react-router-dom';
import {
  getAPIKey, patchAPIKey, putAPIKeyPolicy, regenerateAPIKey, deleteAPIKey, getUsageWindows,
  getInternalUser, getModelGroup,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, StatusBadge, Modal, RoleBadge } from '../components/Primitives.jsx';
import PolicyForm, { formToPolicy } from '../components/PolicyForm.jsx';
import CopyButton from '../components/CopyButton.jsx';
import { useToast } from '../components/Toast.jsx';

export default function ApiKeyDetailPage() {
  const { id } = useParams();
  const { data, error, loading, reload } = useAsync(() => getAPIKey(id), [id]);

  if (loading) return <Spinner label="Loading key…" />;
  if (error) return <ErrorBanner error={error} onRetry={reload} />;
  if (!data) return null;

  return (
    <>
      <KeyHeader apiKey={data} onUpdated={reload} />
      <div className="grid grid--2">
        <KeyDetailsCard apiKey={data} onUpdated={reload} />
        <OwnerCard apiKey={data} />
      </div>
      <div className="grid grid--2">
        <PolicyCard apiKeyId={data.id} policy={data.policy} onUpdated={reload} />
        <UsageWindowsCard apiKeyId={data.id} />
      </div>
    </>
  );
}

// KeyHeader — top summary band: name, id (copy), prefix (copy), status pill,
// and a grouped actions cluster (Refresh / Regenerate / Delete). Keeps the
// high-signal identity + danger actions above the fold instead of buried in
// a details card.
function KeyHeader({ apiKey, onUpdated }) {
  const toast = useToast();
  const [showRegen, setShowRegen] = useState(false);
  const [showDelete, setShowDelete] = useState(false);

  return (
    <div className="card key-header-strip">
      <div className="key-header-strip__main">
        <div className="row" style={{ gap: 12, alignItems: 'baseline', flexWrap: 'wrap' }}>
          <h1 className="main__title" style={{ margin: 0 }}>{apiKey.name}</h1>
          <StatusBadge status={apiKey.status} />
        </div>
        <div className="key-header-strip__meta">
          <span className="key-header-strip__meta-item">
            <span className="dim">id:</span>
            <span className="mono">{apiKey.id}</span>
            <CopyButton value={apiKey.id} label="Copy" small />
          </span>
          <span className="key-header-strip__meta-item">
            <span className="dim">prefix:</span>
            <code className="mono">{apiKey.key_prefix}…</code>
            <CopyButton value={`${apiKey.key_prefix}…`} label="Copy" small />
          </span>
          <span className="key-header-strip__meta-item">
            <span className="dim">owner:</span>
            {apiKey.user_id ? (
              <Link to={`/internal-users/${encodeURIComponent(apiKey.user_id)}`} className="mono">
                {apiKey.user_alias
                  ? `${apiKey.user_alias}${apiKey.user_email ? ` · ${apiKey.user_email}` : ''}`
                  : apiKey.user_id.slice(0, 8)}
              </Link>
            ) : <span className="dim">unassigned</span>}
          </span>
        </div>
      </div>
      <div className="key-header-strip__actions">
        <button onClick={() => { onUpdated(); toast.info('Key refreshed'); }}>Refresh</button>
        <button onClick={() => setShowRegen(true)}>Regenerate</button>
        <button className="danger" onClick={() => setShowDelete(true)}>Delete</button>
      </div>

      {showRegen && (
        <RegenerateModal
          apiKeyId={apiKey.id}
          keyName={apiKey.name}
          onClose={() => setShowRegen(false)}
          onDone={() => { setShowRegen(false); onUpdated(); }}
        />
      )}
      {showDelete && (
        <DeleteModal
          apiKeyId={apiKey.id}
          name={apiKey.name}
          onClose={() => setShowDelete(false)}
          onDone={() => { setShowDelete(false); }}
        />
      )}
    </div>
  );
}

// OwnerCard shows the Internal User that owns this key and surfaces the
// fallback-budget indicator: when the per-key Policy leaves a budget cap
// unset, enforcement falls back to the owner's max_budget (LiteLLM-style
// workflow). Spend attribution always flows to the user via the
// usage_events.user_id / user_windows rows.
//
// The key payload already joins user_alias/user_email, so the basic identity
// row is rendered immediately from apiKey; we only fetch the full internal
// user record for the spend/percentage + RPM fallback display.
function OwnerCard({ apiKey }) {
  const userId = apiKey.user_id || '';
  const owner = useAsync(
    () => (userId ? getInternalUser(userId) : Promise.resolve(null)),
    [userId],
  );

  if (!userId) {
    return (
      <div className="card">
        <h3 className="card__title">Owner</h3>
        <p className="muted" style={{ marginBottom: 12 }}>
          This key is not assigned to any Internal User. Spend attribution
          and per-user fallback budgets are inactive for it.
        </p>
        <div className="form__hint">
          New keys must be created with an owner; use the Internal Users page
          to manage owners.
        </div>
      </div>
    );
  }

  // Render identity from the joined key payload immediately; only block on the
  // full user fetch for the spend/role/fallback figures.
  const u = owner.data;
  const loadingOwner = owner.loading;
  const ownerError = owner.error;

  // Detect which per-key budget caps are unset; for each unset cap, the
  // owner's max_budget is the effective cap (running spend in this window).
  const policy = apiKey.policy || {};
  const hourlyUnset = policy.budget_hourly_usd === null || policy.budget_hourly_usd === undefined;
  const weeklyUnset = policy.budget_weekly_usd === null || policy.budget_weekly_usd === undefined;
  const monthlyUnset = policy.budget_monthly_usd === null || policy.budget_monthly_usd === undefined;
  const rpmUnset = policy.rpm_limit === null || policy.rpm_limit === undefined;
  const maxParallelUnset = policy.max_parallel_requests === null || policy.max_parallel_requests === undefined;
  const anyUnset = hourlyUnset || weeklyUnset || monthlyUnset || rpmUnset || maxParallelUnset;
  const userHasCap = u && u.max_budget && Number(u.max_budget) > 0;

  return (
    <div className="card">
      <div className="row row--between" style={{ marginBottom: 12 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Owner</h3>
        <Link to={`/internal-users/${encodeURIComponent(userId)}`}>
          <button>View user</button>
        </Link>
      </div>
      <div className="form__row">
        <div className="form__label">Internal User</div>
        <div>
          <Link to={`/internal-users/${encodeURIComponent(userId)}`}>
            {apiKey.user_alias || userId}
          </Link>
          <span className="dim mono" style={{ marginLeft: 8 }}>{userId}</span>
        </div>
      </div>
      <div className="form__row">
        <div className="form__label">Email</div>
        <div className="mono dim">{apiKey.user_email || (u && u.user_email) || '—'}</div>
      </div>
      {loadingOwner ? (
        <Spinner label="Loading owner details…" />
      ) : ownerError ? (
        <ErrorBanner error={ownerError} onRetry={owner.reload} />
      ) : u ? (
        <>
          <div className="form__row">
            <div className="form__label">User role</div>
            <RoleBadge role={u.user_role} />
          </div>
          <div className="form__row">
            <div className="form__label">User max_budget</div>
            <div className="mono">
              {userHasCap ? `$${Number(u.max_budget).toFixed(2)}` : 'unlimited'}
              {userHasCap && (
                <span className="dim" style={{ marginLeft: 8 }}>
                  ({((u.spend || 0) / Number(u.max_budget) * 100).toFixed(1)}% used ·
                  ${Number(u.spend || 0).toFixed(2)} running)
                </span>
              )}
            </div>
          </div>
          <div className="form__row">
            <div className="form__label">User RPM limit</div>
            <div className="mono">{u.rpm_limit ?? '—'}</div>
          </div>
          <div className="form__hint" style={{ marginTop: 12 }}>
            {anyUnset ? (
              userHasCap ? (
                <>
                  <strong>Fallback active:</strong> per-key caps that are unset
                  ({describeUnset({ hourlyUnset, weeklyUnset, monthlyUnset, rpmUnset, maxParallelUnset })})
                  fall back to this user's max_budget / rpm_limit / max_parallel_requests.
                  Spend attributed to the key is also tallied against the user.
                </>
              ) : (
                <>
                  Per-key caps that are unset
                  ({describeUnset({ hourlyUnset, weeklyUnset, monthlyUnset, rpmUnset, maxParallelUnset })})
                  have no fallback — the owner's max_budget is also unlimited.
                </>
              )
            ) : (
              <>All per-key caps are set; the user-level fallback is not engaged.</>
            )}
          </div>
        </>
      ) : null}
    </div>
  );
}

function describeUnset({ hourlyUnset, weeklyUnset, monthlyUnset, rpmUnset, maxParallelUnset }) {
  const parts = [];
  if (hourlyUnset) parts.push('hourly USD');
  if (weeklyUnset) parts.push('weekly USD');
  if (monthlyUnset) parts.push('monthly USD');
  if (rpmUnset) parts.push('RPM');
  if (maxParallelUnset) parts.push('max-parallel');
  return parts.length ? parts.join(', ') : 'none';
}

function KeyDetailsCard({ apiKey, onUpdated }) {
  const toast = useToast();
  const [savingStatus, setSavingStatus] = useState(false);
  const [newStatus, setNewStatus] = useState(apiKey.status);

  // expired is server-computed and not a mutable status — render it read-only.
  const isExpired = apiKey.status === 'expired';

  async function handleStatusChange(e) {
    const status = e.target.value;
    setNewStatus(status);
    setSavingStatus(true);
    try {
      await patchAPIKey(apiKey.id, { status });
      toast.success(`Status set to ${status}`);
      onUpdated();
    } catch (err) {
      toast.error(err.message || 'Failed to update status');
      setNewStatus(apiKey.status);
    } finally {
      setSavingStatus(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Key Details</h3>

      {/* Status control: inline select + spinner. Replaces the old
          immediate-PATCH-with-alert() pattern. */}
      <div className="form__row">
        <div className="form__label">Status</div>
        <div className={`status-control ${savingStatus ? 'status-control--saving' : ''}`}>
          {isExpired ? (
            <StatusBadge status="expired" />
          ) : (
            <>
              <StatusBadge status={apiKey.status} />
              <select
                value={newStatus}
                onChange={handleStatusChange}
                disabled={savingStatus}
              >
                <option value="active">active</option>
                <option value="disabled">disabled</option>
                <option value="revoked">revoked</option>
              </select>
              {savingStatus && <div className="spinner spinner--sm" />}
            </>
          )}
        </div>
      </div>

      <div className="grid grid--3" style={{ gap: 10, marginBottom: 16 }}>
        <KeyStat label="Created" value={formatDate(apiKey.created_at)} />
        <KeyStat label="Last used" value={apiKey.last_used_at ? formatDate(apiKey.last_used_at) : 'never'} />
        <KeyStat label="Expires" value={apiKey.expires_at ? formatDate(apiKey.expires_at) : 'never'} />
      </div>

      <div className="form__row">
        <div className="form__label">Prefix</div>
        <div className="row" style={{ gap: 8, alignItems: 'center' }}>
          <code className="mono">{apiKey.key_prefix}…</code>
          <CopyButton value={`${apiKey.key_prefix}…`} label="Copy" small />
        </div>
      </div>

      {apiKey.metadata && Object.keys(apiKey.metadata).length > 0 && (
        <div className="form__row">
          <div className="form__label">Metadata</div>
          <div className="row" style={{ gap: 8, alignItems: 'flex-start' }}>
            <pre className="copyable" style={{ whiteSpace: 'pre-wrap', flex: 1 }}>
              {JSON.stringify(apiKey.metadata, null, 2)}
            </pre>
            <CopyButton value={JSON.stringify(apiKey.metadata, null, 2)} label="Copy" small />
          </div>
        </div>
      )}
    </div>
  );
}

function KeyStat({ label, value }) {
  return (
    <div className="key-stat">
      <div className="key-stat__label">{label}</div>
      <div className="key-stat__value">{value}</div>
    </div>
  );
}

function formatDate(s) {
  return new Date(s).toLocaleString();
}

function PolicyCard({ apiKeyId, policy: initial, onUpdated }) {
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  // When a Model Group drives this key's allowed/blocked lists, fetch its
  // name + grant summary so the read-only card can surface which group is in
  // effect (the policy's own allowed/blocked fields are overridden and thus
  // not shown in that case). Mirrors the fetch pattern in PolicyForm.
  const [group, setGroup] = useState(null);
  const [groupLoading, setGroupLoading] = useState(false);
  useEffect(() => {
    const gid = initial?.model_group_id;
    if (!gid) { setGroup(null); return; }
    let cancelled = false;
    setGroupLoading(true);
    (async () => {
      try {
        const res = await getModelGroup(gid);
        if (!cancelled) setGroup(res?.group || null);
      } catch {
        if (!cancelled) setGroup(null);
      } finally {
        if (!cancelled) setGroupLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [initial?.model_group_id]);
  if (!initial && !editing) {
    return (
      <div className="card">
        <h3 className="card__title">Policy</h3>
        <p className="muted" style={{ marginBottom: 16 }}>
          No policy attached. This key has unlimited access.
        </p>
        <div className="form__actions">
          <button className="primary" onClick={() => setEditing(true)}>Attach Policy</button>
        </div>
      </div>
    );
  }
  if (editing) {
    return (
      <PolicyEditor
        apiKeyId={apiKeyId}
        initial={initial || {}}
        onCancel={() => setEditing(false)}
        onSaved={() => { setEditing(false); onUpdated(); toast.success('Policy saved'); }}
      />
    );
  }
  const usingGroup = !!initial?.model_group_id;
  return (
    <div className="card">
      <h3 className="card__title">Policy</h3>
      <div className="grid grid--2" style={{ gap: 12, marginBottom: 12 }}>
        <PolicyStat label="RPM Limit" value={initial.rpm_limit ?? 'unlimited'} />
        <PolicyStat label="Hourly Rate" value={initial.hourly_rate_limit ?? 'unlimited'} />
        <PolicyStat label="Hourly Budget" value={fmtUSD(initial.budget_hourly_usd)} />
        <PolicyStat label="Weekly Budget" value={fmtUSD(initial.budget_weekly_usd)} />
        <PolicyStat label="Monthly Budget" value={fmtUSD(initial.budget_monthly_usd)} />
        <PolicyStat label="Max Parallel" value={initial.max_parallel_requests ?? 'unlimited'} />
      </div>
      {usingGroup ? (
        <div className="form__row group-summary">
          <div className="form__label">Model access</div>
          {groupLoading ? (
            <div className="muted">Loading model group…</div>
          ) : group ? (
            <div>
              <div>
                <span className="badge badge--info">Model Group</span>{' '}
                <Link to={`/model-groups/${group.id}`} className="dim">{group.name}</Link>
              </div>
              <div className="muted" style={{ marginTop: 4 }}>
                Allowed: {Array.isArray(group.allowed_models) && group.allowed_models.length === 0
                  ? 'all models'
                  : (group.allowed_models || []).join(', ') || '—'}
              </div>
              {Array.isArray(group.blocked_models) && group.blocked_models.length > 0 && (
                <div className="muted">Blocked: {group.blocked_models.join(', ')}</div>
              )}
              {Array.isArray(group.model_routes) && group.model_routes.length > 0 && (
                <div className="muted">Routes: {group.model_routes.length} pinned</div>
              )}
              <div className="muted" style={{ marginTop: 4, fontSize: '0.85em' }}>
                This group overrides the key's allowed/blocked lists and per-model routes.
              </div>
            </div>
          ) : (
            <div className="muted">
              Attached Model Group (id: <span className="mono">{initial.model_group_id}</span>) could not be loaded.
            </div>
          )}
        </div>
      ) : (
        <>
          {initial.allowed_models && initial.allowed_models.length > 0 && (
            <div className="form__row">
              <div className="form__label">Allowed models</div>
              <ul className="list-bare">
                {initial.allowed_models.map((m) => {
                  const route = (initial.model_routes || []).find((r) => r.model === m);
                  return (
                    <li key={m} className="mono">
                      {m}
                      {route && route.providers && route.providers.length > 0 && (
                        <span className="muted" style={{ marginLeft: 8 }}>
                          → {route.providers.join(', ')}
                        </span>
                      )}
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
          {initial.blocked_models && initial.blocked_models.length > 0 && (
            <div className="form__row">
              <div className="form__label">Blocked models</div>
              <ul className="list-bare">
                {initial.blocked_models.map((m) => <li key={m} className="mono">{m}</li>)}
              </ul>
            </div>
          )}
        </>
      )}
      <div className="form__actions">
        <button onClick={() => setEditing(true)}>Edit Policy</button>
      </div>
    </div>
  );
}

function PolicyStat({ label, value }) {
  return (
    <div>
      <div className="form__label">{label}</div>
      <div className="mono">{value}</div>
    </div>
  );
}

function PolicyEditor({ apiKeyId, initial, onCancel, onSaved }) {
  const [policyForm, setPolicyForm] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  async function handleSubmit(e) {
    e.preventDefault();
    if (!policyForm) {
      onCancel();
      return;
    }
    setSubmitting(true);
    setError('');
    const policy = formToPolicy(policyForm, apiKeyId);
    try {
      await putAPIKeyPolicy(apiKeyId, policy);
      onSaved();
    } catch (err) {
      setError(err.message);
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Edit Policy</h3>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <PolicyForm initial={initial} onChange={setPolicyForm} />
        <div className="form__actions">
          <button type="button" onClick={onCancel} disabled={submitting}>Cancel</button>
          <button type="submit" className="primary" disabled={submitting}>
            {submitting ? 'Saving…' : 'Save Policy'}
          </button>
        </div>
      </form>
    </div>
  );
}

function UsageWindowsCard({ apiKeyId }) {
  const { data, error, loading, reload } = useAsync(() => getUsageWindows(apiKeyId), [apiKeyId]);
  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="row row--between" style={{ marginBottom: 12 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Budget Windows</h3>
        <button onClick={reload}>Refresh</button>
      </div>
      {loading && <Spinner label="Loading windows…" />}
      <ErrorBanner error={error} />
      {!loading && !error && data && data.windows && data.windows.length === 0 && (
        <p className="muted dim">No usage recorded yet for this key.</p>
      )}
      {!loading && !error && data && data.windows && data.windows.length > 0 && (
        <table className="table">
          <thead>
            <tr><th>Type</th><th>Window start</th><th>Window end</th><th>Requests</th><th>Tokens</th><th>Cost (USD)</th></tr>
          </thead>
          <tbody>
            {data.windows.map((w) => (
              <tr key={`${w.window_type}-${w.window_start}`}>
                <td><span className="badge badge--muted">{w.window_type}</span></td>
                <td className="mono">{new Date(w.window_start).toLocaleString()}</td>
                <td className="mono">{new Date(w.window_end).toLocaleString()}</td>
                <td className="mono">{w.request_count}</td>
                <td className="mono">{w.total_tokens.toLocaleString()}</td>
                <td className="mono">${w.cost_usd.toFixed(4)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}

function RegenerateModal({ apiKeyId, keyName, onClose, onDone }) {
  const toast = useToast();
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');
  const [secretCopied, setSecretCopied] = useState(false);

  async function handleRegen() {
    setSubmitting(true);
    setError('');
    try {
      const r = await regenerateAPIKey(apiKeyId);
      setResult(r);
      toast.success(`Secret regenerated for "${keyName}"`);
    } catch (err) {
      setError(err.message);
      toast.error(err.message || 'Failed to regenerate');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title="Regenerate Secret" onClose={onClose}>
      {result ? (
        <>
          <div className="form__row">
            <label className="form__label">New plaintext secret (shown once)</label>
            <div className="row" style={{ gap: 8, alignItems: 'flex-start' }}>
              <div className="copyable" style={{ flex: 1 }}>{result.secret}</div>
              <CopyButton value={result.secret} label="Copy" small />
            </div>
            <div className="form__hint">The key ID, policy, and metadata are unchanged.</div>
          </div>
          <label className="row gap-sm" style={{ cursor: 'pointer', marginTop: 12 }}>
            <input
              type="checkbox"
              checked={secretCopied}
              onChange={(e) => setSecretCopied(e.target.checked)}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>
              I've stored the new secret securely
            </span>
          </label>
          <div className="form__actions">
            <button className="primary" onClick={onDone} disabled={!secretCopied}>Done</button>
          </div>
        </>
      ) : (
        <>
          {error && <div className="error-banner">{error}</div>}
          <p className="muted">
            This will issue a new secret for the key. The old secret stops working
            immediately; the key ID, policy, and metadata are preserved.
          </p>
          <div className="form__actions">
            <button onClick={onClose} disabled={submitting}>Cancel</button>
            <button className="danger" onClick={handleRegen} disabled={submitting}>
              {submitting ? 'Regenerating…' : 'Regenerate'}
            </button>
          </div>
        </>
      )}
    </Modal>
  );
}

function DeleteModal({ apiKeyId, name, onClose, onDone }) {
  const toast = useToast();
  const navigate = useNavigate();
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  async function handleDelete() {
    if (confirm !== name) return;
    setSubmitting(true);
    setError('');
    try {
      await deleteAPIKey(apiKeyId);
      toast.success(`Key "${name}" deleted`);
      onDone();
      // SPA navigation instead of a hard full-page reload (window.location.href).
      navigate('/', { replace: true });
    } catch (err) {
      setError(err.message);
      toast.error(err.message || 'Failed to delete key');
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <Modal title="Delete Key" onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <p>
        Permanently delete <strong>{name}</strong>? This removes the key, its policy,
        and all associated usage windows (cascade).
      </p>
      <div className="form__row">
        <label className="form__label">Type the key name to confirm</label>
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

function fmtUSD(v) {
  if (v === null || v === undefined) return 'unset';
  return `$${Number(v).toFixed(2)}`;
}
