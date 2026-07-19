import React, { useState } from 'react';
import { useParams, Link } from 'react-router-dom';
import {
  getAPIKey, patchAPIKey, putAPIKeyPolicy, regenerateAPIKey, deleteAPIKey, getUsageWindows,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, StatusBadge, Modal } from '../components/Primitives.jsx';
import PolicyForm, { formToPolicy } from '../components/PolicyForm.jsx';

export default function ApiKeyDetailPage() {
  const { id } = useParams();
  const { data, error, loading, reload } = useAsync(() => getAPIKey(id), [id]);

  if (loading) return <Spinner label="Loading key…" />;
  if (error) return <ErrorBanner error={error} onRetry={reload} />;
  if (!data) return null;

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">{data.name}</h1>
          <div className="main__subtitle mono dim">{data.id}</div>
        </div>
        <div className="row gap-sm">
          <Link to="/"><button>Back</button></Link>
          <button onClick={reload}>Refresh</button>
        </div>
      </div>

      <div className="grid grid--2">
        <KeyDetailsCard apiKey={data} onUpdated={reload} />
        <PolicyCard apiKeyId={data.id} policy={data.policy} onUpdated={reload} />
      </div>

      <UsageWindowsCard apiKeyId={data.id} />
    </>
  );
}

function KeyDetailsCard({ apiKey, onUpdated }) {
  const [showRegen, setShowRegen] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const [newStatus, setNewStatus] = useState(apiKey.status);

  async function handleStatusChange(e) {
    const status = e.target.value;
    setNewStatus(status);
    try {
      await patchAPIKey(apiKey.id, { status });
      onUpdated();
    } catch (err) {
      alert(err.message);
      setNewStatus(apiKey.status);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Key Details</h3>
      <div className="form__row">
        <div className="form__label">Prefix</div>
        <div className="mono"><code>{apiKey.key_prefix}…</code></div>
      </div>
      <div className="form__row">
        <div className="form__label">Status</div>
        <div className="row gap-sm">
          <StatusBadge status={apiKey.status} />
          <select value={newStatus} onChange={handleStatusChange} style={{ width: 'auto' }}>
            <option value="active">active</option>
            <option value="disabled">disabled</option>
            <option value="revoked">revoked</option>
          </select>
        </div>
      </div>
      <div className="form__row">
        <div className="form__label">Created</div>
        <div>{new Date(apiKey.created_at).toLocaleString()}</div>
      </div>
      <div className="form__row">
        <div className="form__label">Last used</div>
        <div>{apiKey.last_used_at ? new Date(apiKey.last_used_at).toLocaleString() : 'never'}</div>
      </div>
      <div className="form__row">
        <div className="form__label">Expires</div>
        <div>{apiKey.expires_at ? new Date(apiKey.expires_at).toLocaleString() : 'never'}</div>
      </div>
      {apiKey.metadata && Object.keys(apiKey.metadata).length > 0 && (
        <div className="form__row">
          <div className="form__label">Metadata</div>
          <pre className="copyable" style={{ whiteSpace: 'pre-wrap' }}>
            {JSON.stringify(apiKey.metadata, null, 2)}
          </pre>
        </div>
      )}
      <div className="form__actions">
        <button onClick={() => setShowRegen(true)}>Regenerate</button>
        <button className="danger" onClick={() => setShowDelete(true)}>Delete</button>
      </div>

      {showRegen && (
        <RegenerateModal
          apiKeyId={apiKey.id}
          onClose={() => setShowRegen(false)}
          onDone={() => { setShowRegen(false); onUpdated(); }}
        />
      )}
      {showDelete && (
        <DeleteModal
          apiKeyId={apiKey.id}
          name={apiKey.name}
          onClose={() => setShowDelete(false)}
          onDone={() => { setShowDelete(false); window.location.href = '/'; }}
        />
      )}
    </div>
  );
}

function RegenerateModal({ apiKeyId, onClose, onDone }) {
  const [submitting, setSubmitting] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');

  async function handleRegen() {
    setSubmitting(true);
    setError('');
    try {
      const r = await regenerateAPIKey(apiKeyId);
      setResult(r);
    } catch (err) {
      setError(err.message);
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
            <div className="copyable">{result.secret}</div>
            <div className="form__hint">The key ID, policy, and metadata are unchanged.</div>
          </div>
          <div className="form__actions">
            <button className="primary" onClick={onDone}>Done</button>
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
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');

  async function handleDelete() {
    if (confirm !== name) return;
    setSubmitting(true);
    setError('');
    try {
      await deleteAPIKey(apiKeyId);
      onDone();
    } catch (err) {
      setError(err.message);
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

function PolicyCard({ apiKeyId, policy: initial, onUpdated }) {
  const [editing, setEditing] = useState(false);
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
        onSaved={() => { setEditing(false); onUpdated(); }}
      />
    );
  }
  return (
    <div className="card">
      <h3 className="card__title">Policy</h3>
      <div className="grid grid--2" style={{ gap: 12, marginBottom: 12 }}>
        <PolicyStat label="RPM Limit" value={initial.rpm_limit ?? 'unlimited'} />
        <PolicyStat label="Hourly Rate" value={initial.hourly_rate_limit ?? 'unlimited'} />
        <PolicyStat label="Hourly Budget" value={fmtUSD(initial.budget_hourly_usd)} />
        <PolicyStat label="Weekly Budget" value={fmtUSD(initial.budget_weekly_usd)} />
        <PolicyStat label="Monthly Budget" value={fmtUSD(initial.budget_monthly_usd)} />
      </div>
      {initial.allowed_models && initial.allowed_models.length > 0 && (
        <div className="form__row">
          <div className="form__label">Allowed models</div>
          <ul className="list-bare">
            {initial.allowed_models.map((m) => <li key={m} className="mono">{m}</li>)}
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
            {data.windows.map((w, i) => (
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

function fmtUSD(v) {
  if (v === null || v === undefined) return 'unset';
  return `$${Number(v).toFixed(2)}`;
}
