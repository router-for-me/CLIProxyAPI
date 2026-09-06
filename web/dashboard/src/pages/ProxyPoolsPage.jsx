import React, { useState, useEffect, useCallback, useRef } from 'react';
import {
  listProxyPools,
  createProxyPool,
  updateProxyPool,
  deleteProxyPool,
  testProxyPool,
  batchImportProxyPools,
  deployRelayProxyPool,
  ApiError,
} from '../api/client.js';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../components/Primitives.jsx';

// parseProxyLine mirrors the server-side batch-import parse: full URLs pass
// through (allowed schemes only), bare host:port and user:pass@host:port
// normalize to http://. Exported for the import-preview UI and unit tests.
const ALLOWED_SCHEMES = ['http', 'https', 'socks5', 'socks5h'];

export function parseProxyLine(line) {
  const trimmed = String(line || '').trim();
  if (!trimmed) throw new Error('empty line');
  if (trimmed.includes('://')) {
    let parsed;
    try {
      parsed = new URL(trimmed);
    } catch {
      throw new Error('invalid proxy URL');
    }
    if (!ALLOWED_SCHEMES.includes(parsed.protocol.replace(':', ''))) {
      throw new Error('scheme must be one of http, https, socks5, socks5h');
    }
    return parsed.toString();
  }
  const at = trimmed.lastIndexOf('@');
  let hostPart = trimmed;
  let credPart = '';
  if (at >= 0) {
    hostPart = trimmed.slice(at + 1);
    credPart = trimmed.slice(0, at);
  }
  const hostPieces = hostPart.split(':');
  if (hostPieces.length !== 2 || !hostPieces[0] || !hostPieces[1]) {
    throw new Error('expected host:port or user:pass@host:port');
  }
  if (credPart) {
    const credPieces = credPart.split(':');
    if (credPieces.length !== 2 || !credPieces[0] || !credPieces[1]) {
      throw new Error('credentials must be user:pass');
    }
    return `http://${encodeURIComponent(credPieces[0])}:${encodeURIComponent(credPieces[1])}@${hostPart}`;
  }
  return `http://${hostPart}`;
}

// formatTestStatus maps the stored test_status into a display label.
export function formatTestStatus(status) {
  if (status === 'active') return 'Active';
  if (status === 'error') return 'Error';
  return 'Not tested';
}

// redactProxyURL hides credentials in the table view.
function redactProxyURL(url) {
  return String(url || '').replace(/\/\/[^@/]*@/, '//redacted@');
}

function formatDateTime(value) {
  if (!value) return 'Never';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return 'Never';
  return date.toLocaleString();
}

const TYPE_LABELS = { http: 'HTTP/SOCKS', vercel: 'Vercel relay', cloudflare: 'Cloudflare relay', deno: 'Deno relay' };

const RELAY_FIELDS = {
  vercel: [{ key: 'vercel_token', label: 'Vercel API token', type: 'password' }],
  cloudflare: [
    { key: 'cf_account_id', label: 'Cloudflare Account ID', type: 'text' },
    { key: 'cf_api_token', label: 'Cloudflare API token', type: 'password' },
  ],
  deno: [
    { key: 'deno_token', label: 'Deno Deploy API token', type: 'password' },
    { key: 'deno_org_domain', label: 'Org domain (e.g. myorg.deno.net)', type: 'text' },
  ],
};

function emptyForm() {
  return { name: '', proxy_url: '', no_proxy: '', type: 'http', is_active: true, strict_proxy: true };
}

export default function ProxyPoolsPage() {
  const [pools, setPools] = useState([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);
  const [busy, setBusy] = useState(false);
  const [testingId, setTestingId] = useState(null);
  const [healthProgress, setHealthProgress] = useState(null);
  const [notice, setNotice] = useState('');

  const [showForm, setShowForm] = useState(false);
  const [editing, setEditing] = useState(null);
  const [form, setForm] = useState(emptyForm());
  const [formError, setFormError] = useState('');

  const [showImport, setShowImport] = useState(false);
  const [importText, setImportText] = useState('');
  const [importResult, setImportResult] = useState(null);

  const [showRelayMenu, setShowRelayMenu] = useState(false);
  const [relayPlatform, setRelayPlatform] = useState(null);
  const [relayForm, setRelayForm] = useState({ project_name: '' });
  const [relayBusy, setRelayBusy] = useState(false);
  const relayMenuRef = useRef(null);

  const reload = useCallback(async () => {
    setError(null);
    try {
      const res = await listProxyPools({ includeUsage: true });
      setPools(res.pools);
    } catch (err) {
      setError(err);
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { reload(); }, [reload]);

  useEffect(() => {
    const onClickAway = (e) => {
      if (relayMenuRef.current && !relayMenuRef.current.contains(e.target)) setShowRelayMenu(false);
    };
    document.addEventListener('mousedown', onClickAway);
    return () => document.removeEventListener('mousedown', onClickAway);
  }, []);

  const flash = (message) => {
    setNotice(message);
    setTimeout(() => setNotice(''), 4000);
  };

  const openCreate = () => { setEditing(null); setForm(emptyForm()); setFormError(''); setShowForm(true); };
  const openEdit = (pool) => {
    setEditing(pool);
    setForm({
      name: pool.name || '',
      proxy_url: pool.proxy_url || '',
      no_proxy: pool.no_proxy || '',
      type: pool.type || 'http',
      is_active: pool.is_active !== false,
      strict_proxy: pool.strict_proxy !== false,
    });
    setFormError('');
    setShowForm(true);
  };

  const saveForm = async () => {
    setFormError('');
    if (!form.name.trim() || !form.proxy_url.trim()) {
      setFormError('Name and proxy URL are required.');
      return;
    }
    setBusy(true);
    try {
      if (editing) {
        await updateProxyPool(editing.id, form);
      } else {
        await createProxyPool(form);
      }
      setShowForm(false);
      await reload();
      flash(editing ? 'Proxy pool updated' : 'Proxy pool created');
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : 'Save failed');
    } finally {
      setBusy(false);
    }
  };

  const handleTest = async (pool) => {
    setTestingId(pool.id);
    try {
      const res = await testProxyPool(pool.id);
      flash(res?.ok ? `Test passed for ${pool.name}` : `Test failed: ${res?.last_error || 'unknown error'}`);
      await reload();
    } catch (err) {
      flash(err instanceof ApiError ? err.message : 'Test failed');
    } finally {
      setTestingId(null);
    }
  };

  const handleDelete = async (pool) => {
    if (!window.confirm(`Delete proxy pool "${pool.name}"?`)) return;
    try {
      await deleteProxyPool(pool.id);
      await reload();
      flash('Proxy pool deleted');
    } catch (err) {
      if (err instanceof ApiError && err.status === 409) {
        const bound = err.payload?.bound_entry_count ?? 0;
        flash(`Cannot delete: still bound by ${bound} upstream row(s)/entrie(s).`);
      } else {
        flash(err instanceof ApiError ? err.message : 'Delete failed');
      }
    }
  };

  const toggleActive = async (pool) => {
    setPools((prev) => prev.map((p) => (p.id === pool.id ? { ...p, is_active: !p.is_active } : p)));
    try {
      await updateProxyPool(pool.id, { name: pool.name, proxy_url: pool.proxy_url, is_active: !pool.is_active });
    } catch {
      setPools((prev) => prev.map((p) => (p.id === pool.id ? { ...p, is_active: pool.is_active } : p)));
      flash('Failed to update active state');
    }
  };

  const runHealthCheck = async () => {
    const targets = [...pools];
    if (targets.length === 0) return;
    setHealthProgress({ done: 0, total: targets.length });
    let done = 0;
    const queue = [...targets];
    const worker = async () => {
      while (queue.length > 0) {
        const pool = queue.shift();
        if (!pool) break;
        try { await testProxyPool(pool.id); } catch { /* result is persisted server-side */ }
        done += 1;
        setHealthProgress({ done, total: targets.length });
      }
    };
    await Promise.all(Array.from({ length: Math.min(10, targets.length) }, worker));
    setHealthProgress(null);
    await reload();
    flash('Health check complete');
  };

  const runImport = async () => {
    setImportResult(null);
    const lines = importText.split(/\r?\n/).map((l) => l.trim()).filter(Boolean);
    if (lines.length === 0) {
      setImportResult({ error: 'Paste at least one proxy line.' });
      return;
    }
    setBusy(true);
    try {
      const res = await batchImportProxyPools(lines);
      setImportResult(res);
      await reload();
    } catch (err) {
      setImportResult({ error: err instanceof ApiError ? err.message : 'Import failed' });
    } finally {
      setBusy(false);
    }
  };

  const openRelayDeploy = (platform) => {
    setShowRelayMenu(false);
    setRelayPlatform(platform);
    setRelayForm({ project_name: '' });
  };

  const runRelayDeploy = async () => {
    setRelayBusy(true);
    try {
      const payload = { platform: relayPlatform, project_name: relayForm.project_name };
      for (const f of RELAY_FIELDS[relayPlatform]) {
        if (relayForm[f.key]) payload[f.key] = relayForm[f.key];
      }
      const res = await deployRelayProxyPool(payload);
      setRelayPlatform(null);
      await reload();
      flash(`Relay deployed at ${res?.deploy_url || 'the platform'}`);
    } catch (err) {
      setRelayForm((prev) => ({ ...prev, error: err instanceof ApiError ? err.message : 'Deploy failed' }));
    } finally {
      setRelayBusy(false);
    }
  };

  if (loading) return <Spinner label="Loading proxy pools…" />;

  return (
    <div className="page">
      <header className="page-header">
        <div>
          <h1>Proxy Pools</h1>
          <p className="muted">
            Named egress-proxy pools bound to upstream provider entries. Relay pools forward traffic via
            deployed workers (x-relay-target/x-relay-path).
          </p>
        </div>
        <div className="actions">
          <button onClick={reload} disabled={busy}>Refresh</button>
          <button onClick={runHealthCheck} disabled={pools.length === 0 || !!healthProgress}>
            {healthProgress ? `Checking ${healthProgress.done}/${healthProgress.total}…` : 'Health check'}
          </button>
          <button onClick={() => setShowImport(true)}>Batch import</button>
          <div className="relay-menu" ref={relayMenuRef}>
            <button className="primary" onClick={() => setShowRelayMenu((v) => !v)}>Deploy relay ▾</button>
            {showRelayMenu && (
              <div className="relay-menu-items">
                <button onClick={() => openRelayDeploy('vercel')}>Vercel</button>
                <button onClick={() => openRelayDeploy('cloudflare')}>Cloudflare</button>
                <button onClick={() => openRelayDeploy('deno')}>Deno Deploy</button>
              </div>
            )}
          </div>
          <button className="primary" onClick={openCreate}>+ Add pool</button>
        </div>
      </header>

      {notice && <div className="notice">{notice}</div>}
      {error && <ErrorBanner error={error} onRetry={reload} />}

      {pools.length === 0 ? (
        <EmptyState
          title="No proxy pools yet"
          hint="Create a pool, batch-import proxies, or deploy a relay worker to route upstream traffic through them."
          actions={<button className="primary" onClick={openCreate}>+ Add pool</button>}
        />
      ) : (
        <table className="table">
          <thead>
            <tr>
              <th>Name</th>
              <th>Type</th>
              <th>URL</th>
              <th>no_proxy</th>
              <th>Test</th>
              <th>Last tested</th>
              <th>Active</th>
              <th>Strict</th>
              <th>Bound</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {pools.map((pool) => (
              <tr key={pool.id}>
                <td>{pool.name}</td>
                <td>{TYPE_LABELS[pool.type] || pool.type}</td>
                <td><code>{redactProxyURL(pool.proxy_url)}</code></td>
                <td>{pool.no_proxy || '—'}</td>
                <td title={pool.last_error || ''}>{formatTestStatus(pool.test_status)}</td>
                <td>{formatDateTime(pool.last_tested_at)}</td>
                <td>
                  <input
                    type="checkbox"
                    checked={pool.is_active}
                    onChange={() => toggleActive(pool)}
                    aria-label={`Activate ${pool.name}`}
                  />
                </td>
                <td>{pool.strict_proxy ? 'yes' : 'no'}</td>
                <td>{pool.bound_entry_count || 0}</td>
                <td className="row-actions">
                  <button onClick={() => handleTest(pool)} disabled={testingId === pool.id}>
                    {testingId === pool.id ? 'Testing…' : 'Test'}
                  </button>
                  <button onClick={() => openEdit(pool)}>Edit</button>
                  <button className="danger" onClick={() => handleDelete(pool)}>Delete</button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showForm && (
        <Modal title={editing ? `Edit ${editing.name}` : 'Add proxy pool'} onClose={() => setShowForm(false)}>
          <div className="form">
            <label>
              Name
              <input
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="residential-pool-1"
              />
            </label>
            <label>
              Type
              <select value={form.type} onChange={(e) => setForm({ ...form, type: e.target.value })}>
                <option value="http">HTTP/SOCKS proxy</option>
                <option value="vercel">Vercel relay</option>
                <option value="cloudflare">Cloudflare relay</option>
                <option value="deno">Deno relay</option>
              </select>
            </label>
            <label>
              {form.type === 'http' ? 'Proxy URL' : 'Relay base URL (https)'}
              <input
                value={form.proxy_url}
                onChange={(e) => setForm({ ...form, proxy_url: e.target.value })}
                placeholder={form.type === 'http' ? 'socks5://user:pass@host:1080' : 'https://myrelay.example.workers.dev'}
              />
            </label>
            {form.type === 'http' && (
              <label>
                no_proxy (comma-separated host list)
                <input
                  value={form.no_proxy}
                  onChange={(e) => setForm({ ...form, no_proxy: e.target.value })}
                  placeholder="api.anthropic.com,.internal"
                />
              </label>
            )}
            <label className="check">
              <input
                type="checkbox"
                checked={form.strict_proxy}
                onChange={(e) => setForm({ ...form, strict_proxy: e.target.checked })}
              />
              Strict (proxy failure fails the request; off = one direct fallback)
            </label>
            <label className="check">
              <input
                type="checkbox"
                checked={form.is_active}
                onChange={(e) => setForm({ ...form, is_active: e.target.checked })}
              />
              Active
            </label>
            {formError && <p className="error-text">{formError}</p>}
            <div className="modal-actions">
              <button onClick={() => setShowForm(false)}>Cancel</button>
              <button className="primary" onClick={saveForm} disabled={busy}>
                {busy ? 'Saving…' : 'Save'}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {showImport && (
        <Modal title="Batch import proxies" onClose={() => { setShowImport(false); setImportResult(null); }}>
          <div className="form">
            <p className="muted">
              One proxy per line: <code>scheme://host:port</code>, <code>host:port</code>, or{' '}
              <code>user:pass@host:port</code>. Duplicates by URL are skipped.
            </p>
            <textarea
              rows={8}
              value={importText}
              onChange={(e) => setImportText(e.target.value)}
              placeholder={'1.2.3.4:8080\nuser:pass@host:3128\nsocks5://host:1080'}
            />
            {importResult?.error && <p className="error-text">{importResult.error}</p>}
            {importResult && !importResult.error && (
              <p>
                Created {importResult.created}, skipped {importResult.skipped}, failed {importResult.failed}
                {Array.isArray(importResult.errors) && importResult.errors.length > 0 && (
                  <ul className="error-text">
                    {importResult.errors.map((e) => (
                      <li key={e.line}>Line {e.line}: {e.error}</li>
                    ))}
                  </ul>
                )}
              </p>
            )}
            <div className="modal-actions">
              <button onClick={() => { setShowImport(false); setImportResult(null); }}>Close</button>
              <button className="primary" onClick={runImport} disabled={busy}>
                {busy ? 'Importing…' : 'Import'}
              </button>
            </div>
          </div>
        </Modal>
      )}

      {relayPlatform && (
        <Modal title={`Deploy relay to ${TYPE_LABELS[relayPlatform] || relayPlatform}`} onClose={() => setRelayPlatform(null)}>
          <div className="form">
            <p className="muted">
              Deploys a relay worker with the x-relay-target contract and creates a pool for it. Platform
              credentials are used once and never stored.
            </p>
            {RELAY_FIELDS[relayPlatform].map((f) => (
              <label key={f.key}>
                {f.label}
                <input
                  type={f.type}
                  value={relayForm[f.key] || ''}
                  onChange={(e) => setRelayForm({ ...relayForm, [f.key]: e.target.value })}
                />
              </label>
            ))}
            <label>
              Project name
              <input
                value={relayForm.project_name}
                onChange={(e) => setRelayForm({ ...relayForm, project_name: e.target.value })}
                placeholder="relay-<auto>"
              />
            </label>
            {relayForm.error && <p className="error-text">{relayForm.error}</p>}
            <div className="modal-actions">
              <button onClick={() => setRelayPlatform(null)}>Cancel</button>
              <button className="primary" onClick={runRelayDeploy} disabled={relayBusy}>
                {relayBusy ? 'Deploying…' : 'Deploy'}
              </button>
            </div>
          </div>
        </Modal>
      )}
    </div>
  );
}
