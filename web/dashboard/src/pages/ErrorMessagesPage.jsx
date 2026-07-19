import React, { useState, useMemo, useEffect, useCallback } from 'react';
import {
  listErrorMessages, getErrorMessage, putErrorMessage, deleteErrorMessage,
  previewErrorMessage,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import {
  Spinner, ErrorBanner, EmptyState, Modal,
} from '../components/Primitives.jsx';

// ErrorMessagesPage — table of operator-customizable error responses keyed
// by HTTP status code.
//
// Rows shown:
//   - All known codes from errormessages.KnownCodes (curated defaults
//     flagged is_default=true when no override is in PG).
//   - Any operator-added custom codes (status codes outside the curated list).
//
// Operator actions per row:
//   - Edit: opens a modal with Title + Message + optional BodyTemplate +
//     Enabled toggle, plus a live preview pane that calls the server's
//     preview endpoint so the operator sees exactly what the proxy will
//     emit.
//   - Reset to default: DELETE the override — the proxy falls back to the
//     curated default.
export default function ErrorMessagesPage() {
  const { data, error, loading, reload } = useAsync(() => listErrorMessages(), []);
  const [editing, setEditing] = useState(null); // null | { statusCode, isDefault }
  const [creating, setCreating] = useState(false);

  const rows = data?.messages || [];
  const knownCodes = data?.known_codes || [];

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Error Messages</h1>
          <div className="main__subtitle">
            Customize the JSON error body the proxy returns for each HTTP
            status code. Default text ships in-repo; overrides are persisted
            in PostgreSQL and applied without a server restart.
          </div>
        </div>
        <div className="row gap-sm">
          <button className="primary" onClick={() => setCreating(true)}>+ Add code</button>
          <button onClick={reload}>Refresh</button>
        </div>
      </div>

      {loading && <Spinner label="Loading error messages…" />}
      <ErrorBanner error={error} onRetry={reload} />

      {!loading && !error && rows.length === 0 && (
        <EmptyState
          title="No error messages configured"
          hint="The proxy will use built-in defaults for every status code."
        />
      )}

      {!loading && !error && rows.length > 0 && (
        <div className="card" style={{ padding: 0 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Code</th>
                <th>Title</th>
                <th>Message</th>
                <th>Body template</th>
                <th>Status</th>
                <th></th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.status_code}>
                  <td>
                    <div className="mono">{row.status_code}</div>
                    {row.is_default && (
                      <span className="badge badge--muted" style={{ marginTop: 2, fontSize: 10 }}>default</span>
                    )}
                  </td>
                  <td>{row.title || '—'}</td>
                  <td className="dim" style={{ maxWidth: 380, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {row.message || '—'}
                  </td>
                  <td className="dim" style={{ maxWidth: 200, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {row.body_template ? <code className="mono" style={{ fontSize: 11 }}>{row.body_template}</code> : '—'}
                  </td>
                  <td>
                    {row.is_default ? (
                      <span className="badge badge--muted">default</span>
                    ) : row.enabled ? (
                      <span className="badge badge--active">enabled</span>
                    ) : (
                      <span className="badge badge--disabled">disabled</span>
                    )}
                  </td>
                  <td>
                    <div className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
                      <button
                        onClick={() => setEditing({ statusCode: row.status_code, isDefault: row.is_default, initial: row })}
                        style={{ padding: '4px 10px', fontSize: 12 }}
                      >
                        Edit
                      </button>
                      {!row.is_default && (
                        <button
                          onClick={() => handleReset(row.status_code, reload)}
                          className="danger"
                          style={{ padding: '4px 10px', fontSize: 12 }}
                        >
                          Reset
                        </button>
                      )}
                    </div>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {creating && (
        <EditModal
          mode="create"
          initial={null}
          knownCodes={knownCodes}
          onClose={() => setCreating(false)}
          onSaved={() => { setCreating(false); reload(); }}
        />
      )}
      {editing && (
        <EditModal
          mode="edit"
          initial={editing.initial}
          knownCodes={knownCodes}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload(); }}
        />
      )}
    </>
  );
}

async function handleReset(code, reload) {
  if (!confirm(`Reset HTTP ${code} to its built-in default? This removes the operator override.`)) {
    return;
  }
  try {
    await deleteErrorMessage(code);
    reload();
  } catch (err) {
    alert(err.message || 'Reset failed');
  }
}

function EditModal({ mode, initial, knownCodes, onClose, onSaved }) {
  const isCreate = mode === 'create';
  const [code, setCode] = useState(isCreate ? '' : String(initial?.status_code || ''));
  const [title, setTitle] = useState(initial?.title || '');
  const [message, setMessage] = useState(initial?.message || '');
  const [bodyTemplate, setBodyTemplate] = useState(initial?.body_template || '');
  const [details, setDetails] = useState('');
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [preview, setPreview] = useState(null);
  const [previewing, setPreviewing] = useState(false);
  const [previewError, setPreviewError] = useState('');

  const codeInt = useMemo(() => {
    const n = Number(code);
    return Number.isFinite(n) ? n : 0;
  }, [code]);

  const canSave = isCreate ? (codeInt >= 100 && codeInt <= 599) : true;

  async function refreshPreview() {
    if (codeInt < 100 || codeInt > 599) {
      setPreviewError('Enter a status code between 100 and 599 to preview.');
      setPreview(null);
      return;
    }
    setPreviewing(true);
    setPreviewError('');
    try {
      const result = await previewErrorMessage(codeInt, {
        title, message, body_template: bodyTemplate, details,
      });
      setPreview(result?.rendered);
    } catch (err) {
      setPreviewError(err.message || 'Preview failed');
      setPreview(null);
    } finally {
      setPreviewing(false);
    }
  }

  // Auto-refresh preview when inputs change (debounced via simple effect).
  useEffect(() => {
    const t = setTimeout(refreshPreview, 400);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code, title, message, bodyTemplate, details]);

  async function handleSubmit(e) {
    e.preventDefault();
    if (!canSave || saving) return;
    setSaving(true);
    setError('');
    try {
      await putErrorMessage(codeInt, {
        title, message, body_template: bodyTemplate, enabled,
      });
      onSaved();
    } catch (err) {
      setError(err.message || 'Save failed');
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal title={isCreate ? 'Add error message' : `Edit HTTP ${initial?.status_code}`} onClose={onClose}>
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        <div className="grid grid--2">
          <div className="form__row">
            <label className="form__label">Status code *</label>
            <input
              type="number" min="100" max="599" required
              value={code}
              onChange={(e) => setCode(e.target.value)}
              disabled={!isCreate}
              placeholder="e.g. 429"
            />
            {isCreate && !knownOnly(knownCodes, codeInt) && (
              <div className="form__hint">Custom code (outside the curated preset list).</div>
            )}
          </div>
          <div className="form__row">
            <label className="form__label">Enabled</label>
            <select value={enabled ? 'true' : 'false'} onChange={(e) => setEnabled(e.target.value === 'true')}>
              <option value="true">enabled</option>
              <option value="false">disabled (fall back to default)</option>
            </select>
          </div>
        </div>

        <div className="form__row">
          <label className="form__label">Title</label>
          <input type="text" value={title} onChange={(e) => setTitle(e.target.value)} placeholder="e.g. Too Many Requests" />
        </div>

        <div className="form__row">
          <label className="form__label">Message</label>
          <textarea rows={3} value={message} onChange={(e) => setMessage(e.target.value)}
            placeholder="Human-readable detail shown to the caller. Supports {{details}} / {{code}} / {{title}} placeholders." />
          <div className="form__hint">
            Placeholders: <code>{'{{details}}'}</code> (caller-supplied context),
            <code>{' {{code}}'}</code>, <code>{'{{title}}'}</code>.
          </div>
        </div>

        <div className="form__row">
          <label className="form__label">Body template (optional, JSON)</label>
          <textarea rows={4} value={bodyTemplate} onChange={(e) => setBodyTemplate(e.target.value)}
            placeholder={'{\n  "error": {\n    "type": "rate_limit_exceeded",\n    "message": "{{message}} {{details}}"\n  }\n}'} />
          <div className="form__hint">
            When set, the proxy parses this template as JSON after substituting
            placeholders and emits it verbatim. Leave empty to use the default
            shape
            <code> {`{ "error": { type, code, title, message } }`} </code>.
          </div>
        </div>

        <div className="form__row">
          <label className="form__label">Preview details (test value for {'{{details}}'})</label>
          <input type="text" value={details} onChange={(e) => setDetails(e.target.value)}
            placeholder="e.g. 60 requests per minute" />
        </div>

        {/* Live preview pane */}
        <div className="card" style={{ marginTop: 12, background: 'var(--bg)', padding: 12 }}>
          <div className="row row--between" style={{ marginBottom: 8 }}>
            <span className="form__label" style={{ margin: 0 }}>Live preview</span>
            {previewing && <span className="dim row gap-sm" style={{ fontSize: 11 }}><span className="spinner" /> rendering…</span>}
          </div>
          {previewError && <div className="dim" style={{ fontSize: 12, color: 'var(--danger)' }}>{previewError}</div>}
          {preview && (
            <pre className="copyable" style={{ whiteSpace: 'pre-wrap', margin: 0 }}>
              {JSON.stringify(preview, null, 2)}
            </pre>
          )}
          {!preview && !previewError && !previewing && (
            <div className="dim" style={{ fontSize: 12 }}>Edit fields above to render the response.</div>
          )}
        </div>

        <div className="form__actions">
          <button type="button" onClick={onClose} disabled={saving}>Cancel</button>
          <button type="submit" className="primary" disabled={saving || !canSave}>
            {saving ? 'Saving…' : (isCreate ? 'Add message' : 'Save changes')}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function knownOnly(list, code) {
  if (!list) return true;
  return list.includes(code);
}
