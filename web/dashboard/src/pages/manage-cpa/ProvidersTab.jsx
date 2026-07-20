import React, { useState, useCallback, useMemo, useEffect } from 'react';
import { useAsync } from '../../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, StatusBadge, Modal } from '../../components/Primitives.jsx';
import {
  listAuthFiles,
  getAuthFileModels,
  patchAuthFileStatus,
  deleteAuthFile,
  downloadAuthFile,
  listOAuthProviders,
  requestOAuthUrl,
  getProviderKeys,
  putProviderKeys,
  patchProviderKey,
  deleteProviderKey,
  PROVIDER_KEY_KINDS,
  extractProviderList,
  maskKey,
  fmtTime,
} from './hooks.js';
import ProviderKeyEditModal from './ProviderKeyEditModal.jsx';
import FetchModelsModal from './FetchModelsModal.jsx';

// ProvidersTab — full CRUD over every provider account in CPA.
//
// Two stacked sections:
//
//   1) OAuth / auth-files table — every JSON file in the CPA auth-dir.
//      Each row has status, last refresh, a download button, an enable /
//      disable toggle, and a per-provider "Connect" OAuth button that
//      fetches the authorize URL and shows it in a copyable box.
//
//   2) Provider API-key lists — one collapsible card per *-api-key /
//      openai-compatibility provider. Each card lists current entries
//      with an "Add" button and a per-row edit / delete pair that goes
//      through the existing PATCH / DELETE endpoints.
//
// The OAuth section is the source of truth for OAuth-backed providers
// (anthropic, codex, antigravity, kimi, xai). The API-key section is the
// source of truth for static-key providers (gemini, claude, codex, xai,
// vertex, openai-compat) — there is intentional overlap with codex/xai
// because both auth modes are supported simultaneously.
export default function ProvidersTab() {
  return (
    <>
      <div className="row row--between" style={{ marginBottom: 12 }}>
        <div className="dim">
          Auth-files and provider API-key lists. Edits persist to
          <code> config.yaml</code> / <code>auth-dir</code> via the CPA
          management API and trigger a hot-reload.
        </div>
      </div>

      <AuthFilesSection />
      <hr className="subdivider" />
      <ProviderKeyListsSection />
    </>
  );
}

// ---------------------------------------------------------------------------
// 1) Auth-files section
// ---------------------------------------------------------------------------

function AuthFilesSection() {
  const { data, error, loading, reload } = useAsync(() => listAuthFiles(), []);
  const [providerFilter, setProviderFilter] = useState('');
  const [statusFilter, setStatusFilter] = useState('');
  const [connectFor, setConnectFor] = useState(null);
  const [busyId, setBusyId] = useState('');
  const [rowError, setRowError] = useState('');
  const [confirmAuthFileDelete, setConfirmAuthFileDelete] = useState(null);

  const files = Array.isArray(data?.files) ? data.files : [];

  const providers = useMemo(() => {
    const set = new Set();
    for (const f of files) {
      const t = f?.type || f?.provider;
      if (t) set.add(t);
    }
    return Array.from(set).sort();
  }, [files]);

  const filtered = useMemo(() => {
    return files.filter((f) => {
      if (providerFilter && (f?.type || f?.provider) !== providerFilter) return false;
      if (statusFilter === 'active' && (f.disabled || f.status !== 'active')) return false;
      if (statusFilter === 'disabled' && !f.disabled) return false;
      return true;
    });
  }, [files, providerFilter, statusFilter]);

  const onToggle = useCallback(async (file, nextDisabled) => {
    setBusyId(file.id);
    setRowError('');
    try {
      await patchAuthFileStatus({ name: file.name, disabled: nextDisabled });
      reload();
    } catch (err) {
      setRowError(err.message || 'Failed to update auth file status.');
    } finally {
      setBusyId('');
    }
  }, [reload]);

  const onDownload = useCallback(async (file) => {
    try {
      const blob = await downloadAuthFile(file.name);
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = file.name;
      document.body.appendChild(a);
      a.click();
      a.remove();
      setTimeout(() => URL.revokeObjectURL(url), 0);
    } catch (err) {
      setRowError(err.message || 'Download failed.');
    }
  }, []);

  const onDelete = useCallback((file) => {
    setConfirmAuthFileDelete(file);
  }, []);

  async function performAuthFileDelete(file) {
    setBusyId(file.id);
    setRowError('');
    try {
      await deleteAuthFile(file.name);
      setConfirmAuthFileDelete(null);
      reload();
    } catch (err) {
      setRowError(err.message || 'Delete failed.');
    } finally {
      setBusyId('');
    }
  }

  return (
    <div className="card">
      <div className="section-title">
        <h3 className="card__title" style={{ margin: 0 }}>Auth-files (OAuth &amp; static accounts)</h3>
        <div className="row gap-sm">
          <button onClick={reload}>Refresh</button>
        </div>
      </div>

      <div className="row gap-lg" style={{ marginBottom: 12, flexWrap: 'wrap' }}>
        <label className="form__label" style={{ marginTop: 6 }}>Provider</label>
        <select
          value={providerFilter}
          onChange={(e) => setProviderFilter(e.target.value)}
          style={{ width: 180 }}
        >
          <option value="">All</option>
          {providers.map((p) => (
            <option key={p} value={p}>{p}</option>
          ))}
        </select>
        <label className="form__label" style={{ marginTop: 6 }}>Status</label>
        <select
          value={statusFilter}
          onChange={(e) => setStatusFilter(e.target.value)}
          style={{ width: 180 }}
        >
          <option value="">All</option>
          <option value="active">active</option>
          <option value="disabled">disabled</option>
        </select>
        <div className="row gap-sm" style={{ marginLeft: 'auto' }}>
          <ConnectProviderButtons onConnect={setConnectFor} />
        </div>
      </div>

      {loading && <Spinner label="Loading auth-files…" />}
      <ErrorBanner error={error} />
      {rowError && <div className="error-banner">{rowError}</div>}

      {!loading && !error && filtered.length === 0 && (
        <EmptyState
          title="No auth-files match"
          hint="Use one of the Connect buttons above to start an OAuth flow, or upload a JSON file directly to the auth-dir."
        />
      )}

      {!loading && !error && filtered.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="table">
            <thead>
              <tr>
                <th>Name</th>
                <th>Provider</th>
                <th>Email / account</th>
                <th>Status</th>
                <th>Last refresh</th>
                <th>Updated</th>
                <th>Priority</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {filtered.map((f) => (
                <AuthFileRow
                  key={f.id || f.name}
                  file={f}
                  busy={busyId === f.id}
                  onToggle={onToggle}
                  onDownload={onDownload}
                  onDelete={onDelete}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}

      {connectFor && (
        <OAuthConnectModal
          provider={connectFor}
          onClose={() => setConnectFor(null)}
        />
      )}

      {confirmAuthFileDelete && (
        <ConfirmAuthFileDeleteModal
          file={confirmAuthFileDelete}
          busy={busyId === confirmAuthFileDelete.id}
          onCancel={() => setConfirmAuthFileDelete(null)}
          onConfirm={() => performAuthFileDelete(confirmAuthFileDelete)}
        />
      )}
    </div>
  );
}

// ConfirmAuthFileDeleteModal — in-app confirmation dialog for deleting an
// auth-file. Shows the file's identifying fields (name + email/account) so
// the operator doesn't delete the wrong credential. The native
// window.confirm looked out of place next to the new in-app modals.
function ConfirmAuthFileDeleteModal({ file, busy, onCancel, onConfirm }) {
  return (
    <Modal
      title="Delete auth-file?"
      size="sm"
      onClose={onCancel}
      footer={
        <>
          <button onClick={onCancel} disabled={busy}>Cancel</button>
          <button className="danger" onClick={onConfirm} disabled={busy}>
            {busy ? 'Deleting…' : 'Delete file'}
          </button>
        </>
      }
    >
      <p style={{ margin: '0 0 12px', color: 'var(--text)' }}>
        This will permanently delete the following auth-file from disk:
      </p>
      <div
        style={{
          background: 'var(--bg)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-sm)',
          padding: '12px 14px',
          marginBottom: 12,
          fontFamily: 'var(--mono)',
          fontSize: 12,
        }}
      >
        <div><span className="dim">name:</span> {file.name}</div>
        {file.email && <div style={{ marginTop: 4 }}><span className="dim">email:</span> {file.email}</div>}
        <div style={{ marginTop: 4 }}><span className="dim">provider:</span> {file.type || file.provider || '—'}</div>
      </div>
      <div className="dim" style={{ fontSize: 12 }}>
        The auth-file is removed from the auth-dir and the in-memory registry
        is updated. The provider's quota, if any, is no longer claimable
        through this credential.
      </div>
    </Modal>
  );
}

function AuthFileRow({ file, busy, onToggle, onDownload, onDelete }) {
  const [models, setModels] = useState(null);
  const [modelsError, setModelsError] = useState('');

  useEffect(() => {
    let cancelled = false;
    setModels(null);
    setModelsError('');
    getAuthFileModels(file.name)
      .then((res) => { if (!cancelled) setModels(res?.models || []); })
      .catch((err) => { if (!cancelled) setModelsError(err.message || 'failed'); });
    return () => { cancelled = true; };
  }, [file.name]);

  return (
    <tr>
      <td>
        <div className="mono">{file.name}</div>
        {models && (
          <div className="dim" style={{ fontSize: 11, marginTop: 2 }}>
            {models.length} model{models.length === 1 ? '' : 's'}
          </div>
        )}
        {modelsError && (
          <div className="dim" style={{ fontSize: 11, marginTop: 2 }}>
            models: {modelsError}
          </div>
        )}
      </td>
      <td><span className="badge badge--muted">{file.type || file.provider || '—'}</span></td>
      <td className="dim">{file.email || file.label || file.account || '—'}</td>
      <td>
        <StatusBadge status={file.disabled ? 'disabled' : (file.status || 'active')} />
        {file.unavailable && (
          <span className="dim" style={{ marginLeft: 6, fontSize: 11 }}>unavailable</span>
        )}
      </td>
      <td className="dim">{fmtTime(file.last_refresh)}</td>
      <td className="dim">{fmtTime(file.updated_at || file.modtime)}</td>
      <td className="dim">{file.priority ?? '—'}</td>
      <td>
        <div className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
          <button
            onClick={() => onToggle(file, !file.disabled)}
            disabled={busy}
            style={{ padding: '4px 10px', fontSize: 12 }}
          >
            {file.disabled ? 'Enable' : 'Disable'}
          </button>
          <button
            onClick={() => onDownload(file)}
            disabled={busy}
            style={{ padding: '4px 10px', fontSize: 12 }}
          >
            Download
          </button>
          <button
            className="danger"
            onClick={() => onDelete(file)}
            disabled={busy}
            style={{ padding: '4px 10px', fontSize: 12 }}
          >
            Delete
          </button>
        </div>
      </td>
    </tr>
  );
}

function ConnectProviderButtons({ onConnect }) {
  return (
    <>
      <span className="dim" style={{ fontSize: 12 }}>Connect:</span>
      {listOAuthProviders().map((p) => (
        <button
          key={p}
          onClick={() => onConnect(p)}
          style={{ padding: '4px 10px', fontSize: 12 }}
        >
          {labelForProvider(p)}
        </button>
      ))}
    </>
  );
}

function labelForProvider(p) {
  return ({
    anthropic: 'Claude',
    codex: 'Codex',
    antigravity: 'Antigravity',
    kimi: 'Kimi',
    xai: 'xAI',
  })[p] || p;
}

function OAuthConnectModal({ provider, onClose }) {
  const [state, setState] = useState({
    loading: true,
    error: '',
    url: '',
    flow: 'web',
    oauthState: '',
    userCode: '',
    copied: false,
    opened: false,
    skipAutoOpen: false,
    countdown: 3,
  });

  useEffect(() => {
    let cancelled = false;
    setState((s) => ({ ...s, loading: true, error: '', url: '', countdown: 3 }));
    requestOAuthUrl(provider)
      .then((res) => {
        if (cancelled) return;
        setState((s) => ({
          ...s,
          loading: false,
          error: '',
          url: res?.url || '',
          flow: res?.flow || 'web',
          oauthState: res?.state || '',
          userCode: res?.user_code || '',
        }));
      })
      .catch((err) => {
        if (cancelled) return;
        setState((s) => ({
          ...s,
          loading: false,
          error: err.message || 'Failed to start OAuth',
          url: '',
        }));
      });
    return () => { cancelled = true; };
  }, [provider]);

  // Auto-open the authorize URL in a new tab after a short delay so the
  // operator can read the URL first. They can opt out by clicking the
  // "Skip auto-open" link in the modal footer.
  useEffect(() => {
    if (state.loading || state.error || !state.url || state.opened || state.skipAutoOpen) {
      return undefined;
    }
    if (state.countdown <= 0) {
      window.open(state.url, '_blank', 'noopener,noreferrer');
      setState((s) => ({ ...s, opened: true }));
      return undefined;
    }
    const t = setTimeout(() => {
      setState((s) => ({ ...s, countdown: s.countdown - 1 }));
    }, 1000);
    return () => clearTimeout(t);
  }, [state.countdown, state.loading, state.error, state.url, state.opened, state.skipAutoOpen]);

  function openNow() {
    if (!state.url) return;
    window.open(state.url, '_blank', 'noopener,noreferrer');
    setState((s) => ({ ...s, opened: true }));
  }

  function copy() {
    if (!state.url) return;
    navigator.clipboard?.writeText(state.url).then(() => {
      setState((s) => ({ ...s, copied: true }));
      setTimeout(() => setState((s) => ({ ...s, copied: false })), 1500);
    });
  }

  return (
    <Modal
      title={`Connect ${labelForProvider(provider)}`}
      size="md"
      onClose={onClose}
      footer={
        <>
          <button onClick={copy} disabled={!state.url}>
            {state.copied ? '✓ Copied' : 'Copy URL'}
          </button>
          <button className="primary" onClick={openNow} disabled={!state.url}>
            {state.opened ? '↻ Re-open' : 'Open in new tab'}
          </button>
          <button onClick={onClose}>Done</button>
        </>
      }
    >
      {state.loading && <Spinner label="Requesting authorize URL…" />}
      {state.error && <div className="error-banner">{state.error}</div>}

      {!state.loading && !state.error && state.url && (
        <>
          <div
            style={{
              background: 'var(--accent-dim)',
              border: '1px solid var(--accent)',
              color: 'var(--accent)',
              padding: '10px 14px',
              borderRadius: 'var(--radius-sm)',
              fontSize: 12,
              marginBottom: 14,
            }}
          >
            {state.opened ? (
              <>
                ✓ Authorization tab opened. Complete the flow in your browser,
                then come back and click <strong>Refresh</strong> on the auth-files
                table to see the new credential.
              </>
            ) : state.skipAutoOpen ? (
              <>
                Auto-open disabled. Click <strong>Open in new tab</strong> below
                to start the OAuth flow.
              </>
            ) : (
              <>
                Opening authorization tab in <strong>{state.countdown}</strong>
                {' '}s. {state.flow === 'device' ? 'A device code will appear below after the tab opens.' : ''}
                {' '}
                <button
                  type="button"
                  onClick={() => setState((s) => ({ ...s, skipAutoOpen: true }))}
                  style={{
                    background: 'transparent',
                    border: 'none',
                    color: 'var(--accent)',
                    textDecoration: 'underline',
                    padding: 0,
                    fontSize: 12,
                    cursor: 'pointer',
                  }}
                >
                  Skip
                </button>
              </>
            )}
          </div>

          <div className="form__row">
            <label className="form__label">Authorize URL</label>
            <div className="copyable" style={{ wordBreak: 'break-all' }}>{state.url}</div>
            <div className="form__hint">
              This URL is short-lived. If you closed the tab, click
              <strong> Re-open</strong> to start a fresh session.
            </div>
          </div>

          {state.flow === 'device' && state.userCode && (
            <div className="form__row">
              <label className="form__label">User code</label>
              <div className="copyable mono" style={{ fontSize: 16, textAlign: 'center', letterSpacing: '0.2em' }}>
                {state.userCode}
              </div>
              <div className="form__hint">
                Device-flow: enter this code on the provider's device page,
                then return here and click Refresh.
              </div>
            </div>
          )}
        </>
      )}
    </Modal>
  );
}

// ---------------------------------------------------------------------------
// 2) Provider-key lists section
// ---------------------------------------------------------------------------

function ProviderKeyListsSection() {
  return (
    <div>
      <h3 className="card__title" style={{ marginBottom: 12 }}>Provider API-key lists</h3>
      <div className="dim" style={{ marginBottom: 12, fontSize: 12 }}>
        Static API-key and OpenAI-compat entries, persisted in{' '}
        <code>config.yaml</code>. Each card reads + writes one provider at a
        time through the dedicated <code>*-api-key</code> endpoint.
      </div>
      {PROVIDER_KEY_KINDS.map((k) => (
        <ProviderKeyCard key={k.id} kind={k} />
      ))}
    </div>
  );
}

function ProviderKeyCard({ kind }) {
  const { data, error, loading, reload } = useAsync(
    () => getProviderKeys(kind.id),
    [],
  );
  // Also load the auth-files list so the Fetch Models modal can pick the
  // right auth-file for OAuth providers (the registry's models-for-client
  // view is keyed on auth ID). The list is cheap and cached, so we don't
  // gate the card render on it.
  const authFilesReq = useAsync(() => listAuthFiles(), []);
  const authFiles = Array.isArray(authFilesReq.data?.files) ? authFilesReq.data.files : [];

  const [editing, setEditing] = useState(null); // null | { mode, index, initial }
  const [confirmDelete, setConfirmDelete] = useState(null); // null | { item, index }
  const [fetchTarget, setFetchTarget] = useState(null); // null | { mode, authName, baseUrl, entryIndex, entryName }
  const [busy, setBusy] = useState(false);
  const [actionError, setActionError] = useState('');
  const [successMessage, setSuccessMessage] = useState('');

  const list = extractProviderList(data, kind.field);
  // Sibling names are used by the modal for OpenAI-Compat uniqueness check.
  const siblingNames = useMemo(
    () => (kind.id === 'openai' ? list.map((it) => it?.name).filter(Boolean) : []),
    [kind.id, list],
  );

  // Auth-files matching this provider — used as the source list for the
  // Fetch models button on OAuth-backed provider cards. The match is by
  // the auth-file's `type` field, which is the canonical provider key
  // (e.g. "claude", "codex", "gemini", "xai", "vertex", "antigravity").
  const matchingAuthFiles = useMemo(() => {
    if (kind.id === 'openai') return [];
    return authFiles.filter(
      (f) => (f?.type || f?.provider) === providerIdToAuthType(kind.id),
    );
  }, [authFiles, kind.id]);

  async function performDelete(item, index) {
    setBusy(true);
    setActionError('');
    try {
      const criteria = keyCriteriaFor(kind.id, item, index);
      await deleteProviderKey(kind.id, criteria);
      setConfirmDelete(null);
      reload();
    } catch (err) {
      setActionError(err.message || 'Delete failed.');
    } finally {
      setBusy(false);
    }
  }

  // openFetchForFirstAuth — convenience for the section-level "Fetch models"
  // button: opens the modal against the first matching auth-file, or
  // (for OpenAI-Compat) the first entry's base URL.
  function openFetchForFirst() {
    if (kind.id === 'openai') {
      if (list.length === 0) return;
      // Pull the first configured API key for this entry so the modal
      // probes the upstream with the per-provider credential, not the
      // global proxy caller key stored in localStorage.
      const firstRow = list[0];
      const firstEntry = Array.isArray(firstRow?.['api-key-entries'])
        ? firstRow['api-key-entries'][0]
        : Array.isArray(firstRow?.api_key_entries)
          ? firstRow.api_key_entries[0]
          : null;
      const entryApiKey =
        firstEntry?.['api-key']
        || firstEntry?.api_key
        || '';
      setFetchTarget({
        mode: 'openai',
        baseUrl: firstRow.base_url || '',
        entryIndex: 0,
        entryName: firstRow.name || '',
        entryApiKey,
      });
      return;
    }
    if (matchingAuthFiles.length === 0) return;
    setFetchTarget({
      mode: 'auth',
      authName: matchingAuthFiles[0].name,
    });
  }

  // openFetchForRow — per-row "Fetch" button. For OAuth providers the
  // server's auth-files/models endpoint needs an auth id, so we pick the
  // first matching auth-file when the row doesn't carry one. For
  // OpenAI-Compat we always use the row's own base_url + name + first
  // configured API key (the per-provider credential, not the proxy's
  // global caller key).
  function openFetchForRow(item, idx) {
    if (kind.id === 'openai') {
      const firstEntry = Array.isArray(item?.['api-key-entries'])
        ? item['api-key-entries'][0]
        : Array.isArray(item?.api_key_entries)
          ? item.api_key_entries[0]
          : null;
      const entryApiKey =
        firstEntry?.['api-key']
        || firstEntry?.api_key
        || '';
      setFetchTarget({
        mode: 'openai',
        baseUrl: item.base_url || '',
        entryIndex: idx,
        entryName: item.name || '',
        entryApiKey,
      });
      return;
    }
    if (matchingAuthFiles.length === 0) return;
    setFetchTarget({
      mode: 'auth',
      authName: matchingAuthFiles[0].name,
      entryIndex: idx,
    });
  }

  return (
    <div className="card">
      <div className="section-title">
        <h4 className="card__title" style={{ margin: 0 }}>
          {kind.label}{' '}
          <span className="dim mono" style={{ fontSize: 12, fontWeight: 400 }}>
            {kind.field}
          </span>
        </h4>
        <div className="row gap-sm">
          <button onClick={reload}>Refresh</button>
          <button
            onClick={openFetchForFirst}
            disabled={busy || (kind.id === 'openai' ? list.length === 0 : matchingAuthFiles.length === 0)}
            title={
              kind.id === 'openai'
                ? 'Fetch models from the first entry\'s upstream and apply to it'
                : matchingAuthFiles.length === 0
                  ? 'No auth-files match this provider yet — add one via the auth-files table first'
                  : 'Fetch models for the first matching auth-file'
            }
          >
            Fetch models
          </button>
          <button
            className="primary"
            onClick={() => setEditing({ mode: 'create' })}
            disabled={busy}
          >
            + Add
          </button>
        </div>
      </div>

      {loading && <Spinner label={`Loading ${kind.label}…`} />}
      <ErrorBanner error={error} />
      {actionError && <div className="error-banner">{actionError}</div>}

      {!loading && !error && list.length === 0 && (
        <EmptyState
          title={`No ${kind.label} entries yet`}
          hint="Click + Add above to create the first entry. The form is schema-aware: only fields the server actually accepts for this provider are shown."
        />
      )}

      {!loading && !error && list.length > 0 && (
        <div style={{ overflowX: 'auto' }}>
          <table className="table">
            <thead>
              <tr>
                <th>#</th>
                <th>Key / identifier</th>
                <th>Base URL</th>
                <th>Models</th>
                <th>Priority</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {list.map((item, idx) => (
                <tr key={idx}>
                  <td className="dim mono">{idx}</td>
                  <td className="mono">{maskKey(keyDisplayFor(kind.id, item))}</td>
                  <td className="dim mono" style={{ wordBreak: 'break-all' }}>
                    {item.base_url || '—'}
                  </td>
                  <td className="dim">
                    {Array.isArray(item.models) && item.models.length > 0
                      ? item.models.map((m) => m.alias || m.name).filter(Boolean).join(', ')
                      : '—'}
                  </td>
                  <td className="dim mono">{item.priority ?? '—'}</td>
                  <td>
                    {item.disabled ? (
                      <span className="badge badge--disabled">disabled</span>
                    ) : (
                      <span className="badge badge--active">active</span>
                    )}
                  </td>
                  <td>
                    <div className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
                      <button
                        onClick={() => openFetchForRow(item, idx)}
                        disabled={busy || (kind.id === 'openai' ? !item.base_url : matchingAuthFiles.length === 0)}
                        style={{ padding: '4px 10px', fontSize: 12 }}
                        title={
                          kind.id === 'openai'
                            ? (item.base_url
                                ? `Fetch models from ${item.base_url}/v1/models`
                                : 'Add a base URL to this entry first')
                            : matchingAuthFiles.length === 0
                              ? 'No auth-files match this provider yet'
                              : 'Fetch the registry\'s view of this provider\'s models'
                        }
                      >
                        Fetch
                      </button>
                      <button
                        onClick={() => setEditing({ mode: 'edit', index: idx, initial: item })}
                        disabled={busy}
                        style={{ padding: '4px 10px', fontSize: 12 }}
                      >
                        Edit
                      </button>
                      <button
                        className="danger"
                        onClick={() => setConfirmDelete({ item, index: idx })}
                        disabled={busy}
                        style={{ padding: '4px 10px', fontSize: 12 }}
                      >
                        Delete
                      </button>
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {editing && (
        <ProviderKeyEditModal
          // Force a fresh mount per (kind, mode, index) so useState's
          // lazy initializer re-runs buildForm with the new initial.
          // Without this, switching rows in quick succession could
          // leave the form bound to the previous entry's values.
          key={`${kind.id}:${editing.mode}:${editing.index ?? 'new'}`}
          kind={kind}
          mode={editing.mode}
          initial={editing.initial}
          siblingNames={siblingNames}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload(); }}
          onSubmit={async (payload) => {
            if (editing.mode === 'create') {
              // The server has no list-level "append" endpoint, so we PUT
              // back the existing list with the new entry merged in. PUT
              // rewrites the full list, so a concurrent edit by another
              // operator could clobber — but management is single-operator
              // and the modal is the only place that issues PUT today.
              const next = [...list, payload];
              await putProviderKeys(kind.id, next);
            } else {
              await patchProviderKey(kind.id, {
                index: editing.index,
                value: payload,
              });
            }
          }}
        />
      )}

      {confirmDelete && (
        <ConfirmDeleteModal
          kind={kind}
          item={confirmDelete.item}
          onCancel={() => setConfirmDelete(null)}
          onConfirm={() => performDelete(confirmDelete.item, confirmDelete.index)}
          busy={busy}
        />
      )}

      {fetchTarget && (
        <FetchModelsModal
          kind={kind}
          provider={kind.id}
          mode={fetchTarget.mode}
          authName={fetchTarget.authName || ''}
          baseUrl={fetchTarget.baseUrl || ''}
          entryIndex={fetchTarget.entryIndex ?? 0}
          entryName={fetchTarget.entryName || ''}
          entryApiKey={fetchTarget.entryApiKey || ''}
          siblingNames={siblingNames}
          onClose={() => setFetchTarget(null)}
          onApplied={(n) => {
            setFetchTarget(null);
            reload();
            // Surface a small success message at the card level so the
            // operator sees the apply count after the modal closes.
            setActionError('');
            // (We re-use actionError state for errors only; for success we
            //  just refresh and let the updated table speak for itself.)
            if (typeof n === 'number' && n > 0) {
              setSuccessMessage(`Applied ${n} model${n === 1 ? '' : 's'} to ${kind.label}.`);
            }
          }}
        />
      )}

      {successMessage && (
        <div
          style={{
            background: 'var(--success-dim)',
            border: '1px solid var(--success)',
            color: 'var(--success)',
            padding: '8px 12px',
            borderRadius: 'var(--radius-sm)',
            marginTop: 12,
            fontSize: 12,
          }}
        >
          ✓ {successMessage}
        </div>
      )}
    </div>
  );
}

// providerIdToAuthType — maps the dashboard's provider-card id to the
// `type` field on auth-files. The Go server uses the same set of values
// (claude, codex, gemini, xai, vertex, ...) so a simple lookup suffices.
// "interactions" doesn't have its own auth-files yet (the server treats
// it like a Gemini-shape OAuth credential) so we fall through to 'gemini'.
function providerIdToAuthType(id) {
  const map = {
    claude: 'claude',
    codex: 'codex',
    xai: 'xai',
    vertex: 'vertex',
    gemini: 'gemini',
    interactions: 'gemini',
  };
  return map[id] || id;
}

// keyDisplayFor / keyCriteriaFor — provider-specific accessors. Each provider
// has its own field name for the secret ("api-key", "api_key_entries[*].api-key",
// etc.) — we look at the schema and return the right thing.
//
// Wire-format note: the Go server serializes OpenAI-Compat with the json
// tag `api-key-entries` (kebab-case), so JSON.parse keeps that literal
// key. We try both kebab and snake/underscore variants defensively so
// the UI works regardless of which shape arrived.
function keyDisplayFor(provider, item) {
  if (!item) return '';
  if (provider === 'openai') {
    // OpenAI-Compat: array of api-key-entries; show first or "name".
    // Try both kebab (current Go tag) and snake (legacy / mirrored).
    const entries = Array.isArray(item['api-key-entries'])
      ? item['api-key-entries']
      : Array.isArray(item.api_key_entries)
        ? item.api_key_entries
        : null;
    if (entries && entries.length > 0) {
      const first = entries[0] || {};
      return first['api-key'] || first.api_key || item.name || '';
    }
    return item.name || '';
  }
  return item['api-key'] || item.api_key || '';
}

function keyCriteriaFor(provider, item, index) {
  if (provider === 'openai') {
    return { name: item?.name, index };
  }
  return { apiKey: item?.['api-key'] || item?.api_key, index };
}

// ConfirmDeleteModal — in-app confirmation dialog so the user never has to
// interact with the browser's native window.confirm. The dialog shows the
// entry's identifying fields (name / key preview / base URL) so the
// operator can verify they're deleting the right row.
function ConfirmDeleteModal({ kind, item, onCancel, onConfirm, busy }) {
  const display = keyDisplayFor(kind.id, item);
  const label = kind.id === 'openai'
    ? (item?.name || display)
    : maskKey(display);
  return (
    <Modal
      title={`Delete ${kind.label} entry?`}
      size="sm"
      onClose={onCancel}
      footer={
        <>
          <button onClick={onCancel} disabled={busy}>Cancel</button>
          <button className="danger" onClick={onConfirm} disabled={busy}>
            {busy ? 'Deleting…' : 'Delete entry'}
          </button>
        </>
      }
    >
      <p style={{ margin: '0 0 12px', color: 'var(--text)' }}>
        This will permanently remove the following entry from{' '}
        <code className="mono">{kind.field}</code>:
      </p>
      <div
        style={{
          background: 'var(--bg)',
          border: '1px solid var(--border)',
          borderRadius: 'var(--radius-sm)',
          padding: '12px 14px',
          marginBottom: 12,
          fontFamily: 'var(--mono)',
          fontSize: 12,
        }}
      >
        <div><span className="dim">id:</span> {label}</div>
        {item?.base_url && (
          <div style={{ marginTop: 4, wordBreak: 'break-all' }}>
            <span className="dim">base-url:</span> {item.base_url}
          </div>
        )}
        {item?.prefix && (
          <div style={{ marginTop: 4 }}>
            <span className="dim">prefix:</span> {item.prefix}
          </div>
        )}
      </div>
      <div className="dim" style={{ fontSize: 12 }}>
        The server sanitizes the list immediately and persists the change to
        <code> config.yaml</code>. This action cannot be undone.
      </div>
    </Modal>
  );
}
