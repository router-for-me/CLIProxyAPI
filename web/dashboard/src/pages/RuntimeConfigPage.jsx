// RuntimeConfigPage — structured view of the active runtime_config
// singleton. Replaces the legacy RawConfigTab (Phase 4 of the PG-first
// control plane).
//
// The page is a thin wrapper around the runtime-config management API:
//
//   GET  /v0/management/runtime-config       — load the active snapshot
//   POST /v0/management/runtime-config       — apply a settings patch
//                                              under expected_revision
//   GET  /v0/management/config-revisions     — history list (linked)
//
// On a 409 Conflict response from the server, RevisionConflictModal
// appears with three actions (View current / Discard mine / Force save).
// reload_status surfaces the bridge re-render outcome ("applied",
// "pending", or "skipped" — none when PG is offline).
import React, { useEffect, useState } from 'react';
import {
  getRuntimeConfig,
  updateRuntimeConfig,
  extractRevisionConflict,
} from '../api/client.js';
import RevisionConflictModal from '../components/RevisionConflictModal.jsx';
import { Spinner, ErrorBanner } from '../components/Primitives.jsx';

export default function RuntimeConfigPage() {
  const [snapshot, setSnapshot] = useState(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(null);
  const [conflict, setConflict] = useState(null);

  const reload = async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const resp = await getRuntimeConfig();
      setSnapshot(resp?.snapshot ?? null);
    } catch (err) {
      setLoadError(err);
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void reload(); }, []);

  const handleApply = async (settingsPatch, extraPatch, { forceRevision } = {}) => {
    if (!snapshot) return;
    setSaving(true);
    setSaveError(null);
    setConflict(null);
    const expected = forceRevision ?? snapshot.revision;
    const merged = {
      ...snapshot.settings,
      ...settingsPatch,
    };
    const mergedExtra = { ...(snapshot.extra || {}) };
    if (extraPatch) Object.assign(mergedExtra, extraPatch);
    try {
      const resp = await updateRuntimeConfig({
        expectedRevision: expected,
        settings: merged,
        extra: mergedExtra,
      });
      // Server returns the new snapshot.
      setSnapshot(resp.snapshot ?? snapshot);
    } catch (err) {
      const c = extractRevisionConflict(err);
      if (c) {
        setConflict(c);
      } else {
        setSaveError(err);
      }
    } finally {
      setSaving(false);
    }
  };

  const handleDiscard = async () => {
    setConflict(null);
    await reload();
  };

  const handleForceSave = async () => {
    if (!conflict) return;
    await handleApply({}, null, { forceRevision: conflict.activeRevision });
  };

  if (loading) return <Spinner label="Loading runtime config…" />;
  if (loadError) return <ErrorBanner error={loadError} onRetry={reload} />;
  if (!snapshot) return <ErrorBanner error={new Error('No snapshot returned')} />;

  const port = snapshot.settings?.port;
  const host = snapshot.settings?.host;
  const tls = snapshot.settings?.tls || {};
  const remote = snapshot.settings?.['remote-management'] || {};

  return (
    <div className="page">
      <div className="row row--between" style={{ alignItems: 'baseline' }}>
        <h2>Runtime config</h2>
        <div className="muted">revision {snapshot.revision} · {snapshot.updated_source || 'system'}</div>
      </div>

      {saveError && <ErrorBanner error={saveError} onRetry={() => setSaveError(null)} />}
      {saving && <Spinner label="Saving…" />}

      <section className="card" style={{ marginTop: 16 }}>
        <h3>Server binding</h3>
        <div className="form-grid">
          <label>Port
            <input
              type="number"
              defaultValue={port ?? ''}
              onBlur={(e) => handleApply({ port: Number(e.target.value) || 0 })}
            />
          </label>
          <label>Host
            <input
              type="text"
              defaultValue={host ?? ''}
              onBlur={(e) => handleApply({ host: e.target.value })}
            />
          </label>
        </div>
      </section>

      <section className="card" style={{ marginTop: 16 }}>
        <h3>TLS</h3>
        <div className="form-grid">
          <label>Enable
            <input
              type="checkbox"
              defaultChecked={!!tls.enable}
              onChange={(e) => handleApply({ tls: { ...tls, enable: e.target.checked } })}
            />
          </label>
          <label>Cert path
            <input
              type="text"
              defaultValue={tls.cert ?? ''}
              onBlur={(e) => handleApply({ tls: { ...tls, cert: e.target.value } })}
            />
          </label>
          <label>Key path
            <input
              type="text"
              defaultValue={tls.key ?? ''}
              onBlur={(e) => handleApply({ tls: { ...tls, key: e.target.value } })}
            />
          </label>
        </div>
      </section>

      <section className="card" style={{ marginTop: 16 }}>
        <h3>Remote management</h3>
        <div className="form-grid">
          <label>Allow remote
            <input
              type="checkbox"
              defaultChecked={!!remote['allow-remote']}
              onChange={(e) =>
                handleApply({ 'remote-management': { ...remote, 'allow-remote': e.target.checked } })
              }
            />
          </label>
          <label>Secret key
            <input
              type="password"
              defaultValue={remote['secret-key'] ?? ''}
              onBlur={(e) =>
                handleApply({ 'remote-management': { ...remote, 'secret-key': e.target.value } })
              }
            />
          </label>
        </div>
      </section>

      <RevisionConflictModal
        conflict={conflict}
        onClose={() => setConflict(null)}
        onDiscard={handleDiscard}
        onForceSave={handleForceSave}
      />
    </div>
  );
}
