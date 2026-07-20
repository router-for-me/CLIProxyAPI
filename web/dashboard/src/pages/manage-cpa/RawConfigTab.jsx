import React, { useState, useEffect, useCallback } from 'react';
import { useAsync } from '../../hooks/useAsync.js';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';
import { getCpaConfigYaml, putCpaConfigYaml } from './hooks.js';

// RawConfigTab — view + edit the raw config.yaml on disk via the
// /v0/management/config.yaml endpoint.
//
// Modes:
//   - view  : read-only <pre> with copy + download buttons
//   - edit  : <textarea> pre-filled with the same bytes, with a guarded
//             Save that PUTs the whole document back through the server
//
// The server validates the body before persisting (LoadConfigOptional),
// so a malformed YAML returns 422 with a human-readable message. We
// surface that message verbatim so the operator knows exactly which line
// or key needs fixing. The on-disk file is only replaced after the
// validation passes; the in-memory config + watchers are reloaded.
export default function RawConfigTab() {
  const { data, error, loading, reload } = useAsync(
    () => getCpaConfigYaml(),
    [],
  );
  const [mode, setMode] = useState('view');
  const [draft, setDraft] = useState('');
  const [dirty, setDirty] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [saveOk, setSaveOk] = useState('');

  // Keep the draft in sync with the freshly loaded bytes whenever we
  // re-enter edit mode or reload from the server.
  useEffect(() => {
    if (typeof data === 'string' && !dirty) {
      setDraft(data);
    }
  }, [data, dirty]);

  const enterEdit = useCallback(() => {
    if (typeof data === 'string') setDraft(data);
    setSaveError('');
    setSaveOk('');
    setMode('edit');
  }, [data]);

  const cancelEdit = useCallback(() => {
    setDraft(typeof data === 'string' ? data : '');
    setDirty(false);
    setSaveError('');
    setSaveOk('');
    setMode('view');
  }, [data]);

  const onSave = useCallback(async () => {
    setSaving(true);
    setSaveError('');
    setSaveOk('');
    try {
      const res = await putCpaConfigYaml(draft);
      // Server returns { ok: true, changed: [...] } on success.
      const changed = Array.isArray(res?.changed) ? res.changed.join(', ') : '';
      setSaveOk(changed ? `Saved. Reloaded: ${changed}.` : 'Saved.');
      setDirty(false);
      setMode('view');
      reload();
    } catch (err) {
      setSaveError(err.message || 'Save failed.');
    } finally {
      setSaving(false);
    }
  }, [draft, reload]);

  const onCopy = useCallback(() => {
    if (!data) return;
    navigator.clipboard?.writeText(data).then(() => {
      setSaveOk('Copied to clipboard.');
      setTimeout(() => setSaveOk(''), 2000);
    });
  }, [data]);

  const onDownload = useCallback(() => {
    if (!data) return;
    const blob = new Blob([data], { type: 'application/yaml' });
    const url = URL.createObjectURL(blob);
    const a = document.createElement('a');
    a.href = url;
    a.download = 'config.yaml';
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(() => URL.revokeObjectURL(url), 0);
  }, [data]);

  return (
    <div className="card">
      <div className="section-title">
        <h3 className="card__title" style={{ margin: 0 }}>
          config.yaml
          <span className="dim mono" style={{ fontSize: 12, fontWeight: 400, marginLeft: 8 }}>
            raw
          </span>
        </h3>
        <div className="row gap-sm">
          <ModeToggle mode={mode} setMode={setMode} />
          {mode === 'view' ? (
            <>
              <button onClick={onCopy} disabled={!data || loading}>Copy</button>
              <button onClick={onDownload} disabled={!data || loading}>Download</button>
              <button className="primary" onClick={enterEdit} disabled={!data || loading}>
                Edit
              </button>
            </>
          ) : (
            <>
              <button onClick={cancelEdit} disabled={saving}>Cancel</button>
              <button
                className="primary"
                onClick={onSave}
                disabled={saving || !dirty || !draft}
              >
                {saving ? 'Saving…' : 'Save'}
              </button>
            </>
          )}
        </div>
      </div>

      {saveError && <div className="error-banner">{saveError}</div>}
      {saveOk && (
        <div
          style={{
            background: 'var(--success-dim)',
            border: '1px solid var(--success)',
            color: 'var(--success)',
            padding: '8px 12px',
            borderRadius: 'var(--radius-sm)',
            marginBottom: 12,
            fontSize: 12,
          }}
        >
          {saveOk}
        </div>
      )}

      {loading && <Spinner label="Loading config.yaml…" />}
      <ErrorBanner error={error} />

      {!loading && !error && mode === 'view' && (
        <pre className="code-block" aria-label="config.yaml">
          {data || ''}
        </pre>
      )}

      {!loading && !error && mode === 'edit' && (
        <>
          <textarea
            className="yaml-textarea"
            value={draft}
            onChange={(e) => { setDraft(e.target.value); setDirty(true); setSaveError(''); }}
            spellCheck={false}
            aria-label="Edit config.yaml"
          />
          <div className="form__hint" style={{ marginTop: 6 }}>
            Save round-trips through the server's YAML validator. Invalid
            documents are rejected with a 422 and the on-disk file is left
            untouched.
          </div>
        </>
      )}
    </div>
  );
}

function ModeToggle({ mode, setMode }) {
  return (
    <div className="row gap-sm" role="tablist" aria-label="Config view mode">
      <button
        onClick={() => setMode('view')}
        className={mode === 'view' ? 'primary' : ''}
        style={{ padding: '4px 10px', fontSize: 12 }}
        role="tab"
        aria-selected={mode === 'view'}
      >
        View
      </button>
      <button
        onClick={() => setMode('edit')}
        className={mode === 'edit' ? 'primary' : ''}
        style={{ padding: '4px 10px', fontSize: 12 }}
        role="tab"
        aria-selected={mode === 'edit'}
      >
        Edit
      </button>
    </div>
  );
}
