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
import { ToggleRow, PasswordInput } from './manage-cpa/FormPrimitives.jsx';

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

// validatePoolForm checks the add/edit modal fields inline. Returns a map of
// { fieldName: message } — absent keys are valid. Pure; exported for tests.
export function validatePoolForm(form) {
  const errors = {};
  const name = String(form.name || '').trim();
  if (!name) {
    errors.name = 'Name is required.';
  } else if (name.length < 2) {
    errors.name = 'Name must be at least 2 characters.';
  }

  const rawURL = String(form.proxy_url || '').trim();
  const poolType = String(form.type || 'http');
  if (!rawURL) {
    errors.proxy_url = poolType === 'http' ? 'Proxy URL is required.' : 'Relay base URL is required.';
  } else {
    let parsed = null;
    try {
      parsed = new URL(rawURL);
    } catch {
      parsed = null;
    }
    if (!parsed || !parsed.protocol || !parsed.host) {
      errors.proxy_url = 'Must be an absolute URL with scheme and host.';
    } else if (poolType === 'http') {
      const scheme = parsed.protocol.replace(':', '');
      if (!ALLOWED_SCHEMES.includes(scheme)) {
        errors.proxy_url = 'Scheme must be one of http, https, socks5, socks5h.';
      }
    } else if (parsed.protocol !== 'https:') {
      errors.proxy_url = 'Relay base URL must be an https URL.';
    }
  }

  const noProxy = String(form.no_proxy || '').trim();
  if (noProxy) {
    const parts = noProxy.split(',').map((p) => p.trim()).filter(Boolean);
    const hasWildcard = parts.includes('*');
    if (hasWildcard && parts.length > 1) {
      errors.no_proxy = '"*" must be the only entry — it bypasses the proxy for every host.';
    }
  }
  return errors;
}

// effectivePreview renders the URL as the backend will actually store and
// use it: http pools get the composite suffix (?no_proxy=…&strict=…), relay
// pools keep their plain https base. Invalid URLs yield '' so the preview
// stays quiet while the operator is still typing.
export function effectivePreview(form) {
  const rawURL = String(form.proxy_url || '').trim();
  if (!rawURL) return '';
  let parsed;
  try {
    parsed = new URL(rawURL);
  } catch {
    return '';
  }
  if (String(form.type || 'http') !== 'http') {
    return parsed.toString();
  }
  const attrs = [];
  const parts = String(form.no_proxy || '').split(',').map((p) => p.trim()).filter(Boolean);
  if (parts.length > 0) {
    attrs.push(`no_proxy=${encodeURIComponent(parts.join(',').toLowerCase())}`);
  }
  attrs.push(form.strict_proxy ? 'strict=true' : 'strict=false');
  const sep = rawURL.includes('?') ? '&' : '?';
  return rawURL + sep + attrs.join('&');
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
  const [touchedFields, setTouchedFields] = useState({});

  // Delete confirmation modal (replaces window.confirm): shows the pool
  // identity + a bound-count warning, and keeps server-side 409 messages
  // inside the modal so the operator sees why the delete was rejected.
  const [confirmDelete, setConfirmDelete] = useState(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');

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

  const openCreate = () => {
    setEditing(null);
    setForm(emptyForm());
    setFormError('');
    setTouchedFields({});
    setShowForm(true);
  };
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
    setTouchedFields({});
    setShowForm(true);
  };

  // Inline per-field errors: computed live; Save blocks until clean.
  const fieldErrors = validatePoolForm(form);
  const hasFieldErrors = Object.keys(fieldErrors).length > 0;

  const setField = (name, value) => {
    setForm((f) => ({ ...f, [name]: value }));
    setTouchedFields((t) => ({ ...t, [name]: true }));
  };

  const saveForm = async () => {
    setFormError('');
    setTouchedFields({ name: true, proxy_url: true, no_proxy: true });
    if (hasFieldErrors) return;
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

  const openDelete = (pool) => {
    setConfirmDelete(pool);
    setDeleteError('');
  };

  const confirmDeletePool = async () => {
    if (!confirmDelete) return;
    setDeleting(true);
    setDeleteError('');
    try {
      await deleteProxyPool(confirmDelete.id);
      setConfirmDelete(null);
      await reload();
      flash('Proxy pool deleted');
    } catch (err) {
      // Keep the modal open and surface the reason in place: a 409 carries
      // the live bound_entry_count; anything else is the server message.
      if (err instanceof ApiError && err.status === 409) {
        const bound = err.payload?.bound_entry_count;
        setDeleteError(
          Number.isFinite(bound)
            ? `Still bound by ${bound} upstream row(s)/entrie(s) — unbind them first.`
            : 'Still bound by upstream provider rows or entries.',
        );
      } else {
        setDeleteError(err instanceof ApiError ? err.message : 'Delete failed');
      }
    } finally {
      setDeleting(false);
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
              <th>Bound</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {pools.map((pool) => (
              <tr key={pool.id}>
                <td className="pool-name" title={pool.name}>{pool.name}</td>
                <td>{TYPE_LABELS[pool.type] || pool.type}</td>
                <td><code className="pool-url" title={redactProxyURL(pool.proxy_url)}>{redactProxyURL(pool.proxy_url)}</code></td>
                <td><code className="pool-noproxy">{pool.no_proxy || '—'}</code></td>
                <td>
                  <span
                    className={`badge ${pool.test_status === 'active' ? 'badge--active' : pool.test_status === 'error' ? 'badge--revoked' : 'badge--muted'}`}
                    title={pool.last_error || 'Not tested yet — use the Test action.'}
                  >
                    {formatTestStatus(pool.test_status)}
                  </span>
                </td>
                <td>{formatDateTime(pool.last_tested_at)}</td>
                <td>
                  <ToggleRow
                    label=""
                    checked={pool.is_active}
                    onChange={() => toggleActive(pool)}
                  />
                </td>
                <td>
                  <span className={`badge ${pool.bound_entry_count > 0 ? 'badge--info' : 'badge--muted'}`}>
                    {pool.bound_entry_count || 0}
                  </span>
                </td>
                <td className="row-actions">
                  <button
                    className="row-actions__btn"
                    onClick={() => handleTest(pool)}
                    disabled={testingId === pool.id}
                    title={testingId === pool.id ? 'Testing…' : 'Test connectivity'}
                  >
                    {testingId === pool.id ? 'Testing…' : '⚡ Test'}
                  </button>
                  <button
                    className="row-actions__btn row-actions__btn--primary"
                    onClick={() => openEdit(pool)}
                    title="Edit pool"
                  >
                    ✎ Edit
                  </button>
                  <button
                    className="btn-icon"
                    onClick={() => openDelete(pool)}
                    title="Delete pool"
                    aria-label={`Delete ${pool.name}`}
                  >
                    🗑
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {showForm && (
        <Modal title={editing ? `Edit ${editing.name}` : 'Add proxy pool'} onClose={() => setShowForm(false)}>
          <form className="form" onSubmit={(e) => { e.preventDefault(); saveForm(); }}>
            <div className="form-section">
              <div className="form-section__title">Pool</div>
              <label>
                Name
                <input
                  value={form.name}
                  onChange={(e) => setField('name', e.target.value)}
                  placeholder="residential-pool-1"
                  autoFocus
                />
                {touchedFields.name && fieldErrors.name && <span className="error-text">{fieldErrors.name}</span>}
                {!fieldErrors.name && <span className="muted">Shown in the provider editor's proxy pool picker.</span>}
              </label>
              <label>
                Type
                <select value={form.type} onChange={(e) => setField('type', e.target.value)}>
                  <option value="http">HTTP/SOCKS proxy</option>
                  <option value="vercel">Vercel relay</option>
                  <option value="cloudflare">Cloudflare relay</option>
                  <option value="deno">Deno relay</option>
                </select>
                {form.type !== 'http' && (
                  <span className="muted">
                    Relay base URL must be https — traffic is forwarded via x-relay-target headers to the relay worker.
                  </span>
                )}
              </label>
              <label>
                {form.type === 'http' ? 'Proxy URL' : 'Relay base URL (https)'}
                <PasswordInput
                  value={form.proxy_url}
                  onChange={(v) => setField('proxy_url', v)}
                  placeholder={form.type === 'http' ? 'socks5://user:pass@host:1080' : 'https://myrelay.example.workers.dev'}
                />
                {touchedFields.proxy_url && fieldErrors.proxy_url && <span className="error-text">{fieldErrors.proxy_url}</span>}
              </label>
              {form.type === 'http' && (
                <label>
                  no_proxy
                  <input
                    value={form.no_proxy}
                    onChange={(e) => setField('no_proxy', e.target.value)}
                    placeholder="api.anthropic.com,.internal"
                    spellCheck={false}
                  />
                  <span className="muted">
                    Comma-separated hosts that bypass this proxy: <code>api.anthropic.com</code> exact,{' '}
                    <code>.internal</code> subdomain suffix, <code>*</code> everything. Empty = proxy all targets.
                  </span>
                </label>
              )}
              {form.type === 'http' && effectivePreview(form) && (
                <div className="pool-preview" title="What the renderer stores for this pool">
                  Stored as <code>{redactProxyURL(effectivePreview(form))}</code>
                </div>
              )}
            </div>

            <div className="form-section">
              <div className="form-section__title">Behavior</div>
              <ToggleRow
                label="Strict mode"
                hint="On (default): a proxy failure fails the request. Off: one direct fallback retry — the server IP may reach the target."
                checked={form.strict_proxy}
                onChange={(v) => setField('strict_proxy', v)}
              />
              <ToggleRow
                label="Active"
                hint="Inactive pools are skipped at render time; bound entries fall back to their manual proxy URL."
                checked={form.is_active}
                onChange={(v) => setField('is_active', v)}
              />
            </div>

            {formError && <p className="error-text">{formError}</p>}
            <div className="modal-actions">
              <button type="button" onClick={() => setShowForm(false)}>Cancel</button>
              <button className="primary" type="submit" disabled={busy || hasFieldErrors}>
                {busy ? 'Saving…' : editing ? 'Save changes' : 'Create pool'}
              </button>
            </div>
          </form>
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

      {confirmDelete && (
        <Modal title="Delete proxy pool" onClose={() => setConfirmDelete(null)}>
          <div className="form">
            <p>
              Delete <strong>{confirmDelete.name}</strong>?
            </p>
            <p className="muted">
              <code>{redactProxyURL(confirmDelete.proxy_url)}</code>
              {confirmDelete.bound_entry_count > 0 && (
                <> — currently bound by <strong>{confirmDelete.bound_entry_count}</strong> upstream row(s)/entrie(s). The server will reject this delete until they are unbound.</>
              )}
            </p>
            {deleteError && <p className="error-text">{deleteError}</p>}
            <div className="modal-actions">
              <button type="button" onClick={() => setConfirmDelete(null)} disabled={deleting}>Cancel</button>
              <button className="danger" type="button" onClick={confirmDeletePool} disabled={deleting}>
                {deleting ? 'Deleting…' : 'Delete pool'}
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
