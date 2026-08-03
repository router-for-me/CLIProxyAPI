import React, { useState, useEffect, useRef } from 'react';
import {
  listBackupResources, exportData, importData,
} from '../api/client.js';
import { ErrorBanner } from '../components/Primitives.jsx';

// Resource category labels shown beside each checkbox. Keys match the backend
// backup resource identifiers returned by /export/resources.
const RESOURCE_LABELS = {
  api_keys: 'API Keys & policies',
  internal_users: 'Internal Users & budget windows',
  model_groups: 'Model Groups',
  models_catalog: 'Models Catalog & pricing',
  upstream_providers: 'Upstream Providers',
  management_tokens: 'Management API tokens',
  error_messages: 'Error Messages',
  pricing_sources: 'Pricing Sources',
  auth_files: 'Auth files (OAuth / file-backed)',
  usage: 'Usage analytics (events, errors, windows)',
  alerts: 'Alerts feed & settings',
  model_health: 'Model Health snapshots & history',
  sync_log: 'Upstream Sync Log',
};

// ImportExportPage — export all (or selected) PG-backed data to a portable
// JSON bundle, and restore such a bundle. Import wipes and replaces the
// selected categories, so the UI requires an explicit confirmation before the
// bundle is sent.
export default function ImportExportPage() {
  const [resources, setResources] = useState([]);
  const [selected, setSelected] = useState(null); // null => nothing yet
  const [exporting, setExporting] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState(null);
  const [result, setResult] = useState(null);
  const [confirming, setConfirming] = useState(false);
  const [bundleText, setBundleText] = useState('');
  const fileRef = useRef(null);

  useEffect(() => {
    let alive = true;
    listBackupResources()
      .then((data) => {
        if (!alive) return;
        const list = data?.resources || [];
        setResources(list);
        if (list.length) {
          // Default: everything selected.
          const all = {};
          list.forEach((r) => { all[r] = true; });
          setSelected(all);
        }
      })
      .catch((e) => { if (alive) setError(e); });
    return () => { alive = false; };
  }, []);

  const selectedList = resources.filter((r) => selected && selected[r]);

  function toggleResource(res) {
    setSelected((prev) => {
      const next = { ...(prev || {}) };
      if (next[res]) delete next[res];
      else next[res] = true;
      return next;
    });
  }

  function selectAll() {
    const all = {};
    resources.forEach((r) => { all[r] = true; });
    setSelected(all);
  }

  function selectNone() {
    setSelected({});
  }

  async function handleExport() {
    setError(null);
    setExporting(true);
    try {
      await exportData(selectedList, { download: true });
      setResult({ type: 'export', rows: selectedList.length });
    } catch (e) {
      setError(e);
    } finally {
      setExporting(false);
    }
  }

  function onFileSelected(event) {
    const file = event.target.files && event.target.files[0];
    setResult(null);
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => setBundleText(String(reader.result || ''));
    reader.onerror = () => setError(new Error('Could not read the selected file.'));
    reader.readAsText(file);
  }

  async function handleImport() {
    setError(null);
    setResult(null);
    if (!bundleText.trim()) {
      setError(new Error('Choose a bundle file before importing.'));
      return;
    }
    let bundle;
    try {
      bundle = JSON.parse(bundleText);
    } catch {
      setError(new Error('The selected file is not valid JSON.'));
      return;
    }
    setImporting(true);
    try {
      const resp = await importData(bundle, selectedList);
      setResult({ type: 'import', resp });
    } catch (e) {
      setError(e);
    } finally {
      setImporting(false);
      setConfirming(false);
    }
  }

  function renderReport() {
    if (!result || result.type !== 'import') return null;
    const resourcesReport = result.resp?.import?.resources || {};
    const name = (k) => RESOURCE_LABELS[k] || k;
    const entries = Object.entries(resourcesReport);
    return (
      <div className="card" style={{ marginTop: 16 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Import result</h3>
        {entries.length === 0 ? (
          <p className="muted">No resources were restored.</p>
        ) : (
          <table className="table" style={{ marginTop: 12 }}>
            <thead>
              <tr><th>Category</th><th>Inserted</th><th>Skipped</th></tr>
            </thead>
            <tbody>
              {entries.map(([k, r]) => (
                <tr key={k}>
                  <td>{name(k)}</td>
                  <td>{r.inserted}</td>
                  <td>{r.skipped}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>
    );
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Import / Export</h1>
          <div className="main__subtitle">
            Export the PostgreSQL-backed data to a portable JSON bundle, or
            restore a previously exported bundle. Choose which categories to
            include — by default everything is selected.
          </div>
        </div>
      </div>

      <ErrorBanner error={error} />

      <div className="grid grid--2">
        {/* ---------- Export ---------- */}
        <div className="card">
          <h3 className="card__title">Export</h3>
          <ResourceChecklist
            resources={resources}
            selected={selected}
            onToggle={toggleResource}
            onAll={selectAll}
            onNone={selectNone}
          />
          <div className="row" style={{ marginTop: 16 }}>
            <button className="primary" onClick={handleExport} disabled={exporting}>
              {exporting ? 'Exporting…' : 'Export to file'}
            </button>
            {result && result.type === 'export' && (
              <span className="muted">Downloaded {result.rows || 'all'} categories.</span>
            )}
          </div>
        </div>

        {/* ---------- Import ---------- */}
        <div className="card">
          <h3 className="card__title">Import</h3>
          <ResourceChecklist
            resources={resources}
            selected={selected}
            onToggle={toggleResource}
            onAll={selectAll}
            onNone={selectNone}
          />
          <div className="row" style={{ marginTop: 16 }}>
            <input
              ref={fileRef}
              type="file"
              accept="application/json,.json"
              style={{ display: 'none' }}
              onChange={onFileSelected}
            />
            <button onClick={() => fileRef.current && fileRef.current.click()}>
              Choose bundle file
            </button>
            <span className="muted">
              {bundleText ? 'Bundle loaded.' : 'No file chosen.'}
            </span>
          </div>

          {!confirming ? (
            <button
              className="danger"
              style={{ marginTop: 16 }}
              onClick={() => setConfirming(true)}
              disabled={!bundleText.trim() || importing}
            >
              {importing ? 'Importing…' : 'Import'}
            </button>
          ) : (
            <div className="card" style={{ marginTop: 16, borderColor: '#c00' }}>
              <div style={{ marginBottom: 12 }}>
                <strong>Confirm import</strong>
                <p className="muted">
                  Importing will wipe and replace the selected categories with
                  the contents of the bundle. This cannot be undone. Continue?
                </p>
              </div>
              <div className="row gap-sm">
                <button className="danger" onClick={handleImport} disabled={importing}>
                  {importing ? 'Importing…' : 'Yes, import'}
                </button>
                <button onClick={() => setConfirming(false)} disabled={importing}>Cancel</button>
              </div>
            </div>
          )}
        </div>
      </div>

      {renderReport()}
    </>
  );
}

// ResourceChecklist — checkbox list of backup categories with Select all / none.
function ResourceChecklist({ resources, selected, onToggle, onAll, onNone }) {
  if (!resources || resources.length === 0) {
    return <p className="muted">Loading categories…</p>;
  }
  const count = resources.filter((r) => selected && selected[r]).length;
  return (
    <div>
      <div className="row gap-sm" style={{ marginBottom: 8 }}>
        <button type="button" className="linklike" onClick={onAll}>Select all</button>
        <span className="muted">·</span>
        <button type="button" className="linklike" onClick={onNone}>Select none</button>
        <span className="muted" style={{ marginLeft: 'auto' }}>{count} selected</span>
      </div>
      <div className="checklist">
        {resources.map((r) => (
          <label key={r} className="checklist__item">
            <input
              type="checkbox"
              checked={!!(selected && selected[r])}
              onChange={() => onToggle(r)}
            />
            <span>{RESOURCE_LABELS[r] || r}</span>
          </label>
        ))}
      </div>
    </div>
  );
}
