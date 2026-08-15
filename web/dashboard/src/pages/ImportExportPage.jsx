import React, { useState, useEffect, useRef } from 'react';
import {
  listBackupResources, exportData, importData, bundleSummary,
} from '../api/client.js';
import { ErrorBanner } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';

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

function resourceLabel(k) {
  return RESOURCE_LABELS[k] || k;
}

// buildPreviewEntries — render the pre-import preview in the canonical server
// resource order, then any extra keys the bundle carries that this server does
// not know about. Each entry is [resourceKey, rowCount].
function buildPreviewEntries(resources, preview) {
  if (!preview) return [];
  const entries = [];
  resources.forEach((r) => {
    if (r in preview) entries.push([r, preview[r]]);
  });
  Object.keys(preview).forEach((k) => {
    if (!entries.some(([rk]) => rk === k)) entries.push([k, preview[k]]);
  });
  return entries;
}

// ImportExportPage — export all (or selected) PG-backed data to a portable
// JSON bundle, and restore such a bundle. Import wipes and replaces the
// selected categories, so the UI requires an explicit confirmation before the
// bundle is sent.
export default function ImportExportPage() {
  const [resources, setResources] = useState([]);
  // Export and Import keep independent selections: exporting defaults to every
  // category, while importing starts empty (an empty selection restores every
  // category present in the bundle).
  const [exportSel, setExportSel] = useState(null); // null => nothing yet
  const [importSel, setImportSel] = useState({});
  const [exporting, setExporting] = useState(false);
  const [importing, setImporting] = useState(false);
  const [error, setError] = useState(null);
  const [result, setResult] = useState(null);
  const [confirming, setConfirming] = useState(false);
  const [bundleText, setBundleText] = useState('');
  // Pre-import preview parsed from the bundle header summary (row counts only,
  // no full content parse) and the import progress bar state.
  const [preview, setPreview] = useState(null); // { resourceKey: rowCount } | null
  const [progress, setProgress] = useState(null); // { active, total } | null
  const fileRef = useRef(null);
  const toast = useToast();

  useEffect(() => {
    let alive = true;
    listBackupResources()
      .then((data) => {
        if (!alive) return;
        const list = data?.resources || [];
        setResources(list);
        if (list.length) {
          // Export defaults to everything selected.
          const all = {};
          list.forEach((r) => { all[r] = true; });
          setExportSel(all);
        }
      })
      .catch((e) => { if (alive) setError(e); });
    return () => { alive = false; };
  }, []);

  const exportList = resources.filter((r) => exportSel && exportSel[r]);
  const importList = resources.filter((r) => importSel && importSel[r]);

  // toggleIn returns a copy of value with res toggled, for use with any of the
  // selection setters.
  function toggleIn(value, res) {
    const next = { ...(value || {}) };
    if (next[res]) delete next[res];
    else next[res] = true;
    return next;
  }

  function allSelected() {
    const all = {};
    resources.forEach((r) => { all[r] = true; });
    return all;
  }

  async function handleExport() {
    setError(null);
    setExporting(true);
    try {
      await exportData(exportList, { download: true });
      setResult({ type: 'export', rows: exportList.length });
    } catch (e) {
      setError(e);
    } finally {
      setExporting(false);
    }
  }

  function onFileSelected(event) {
    const file = event.target.files && event.target.files[0];
    setResult(null);
    setProgress(null); // a fresh bundle invalidates the previous import's progress
    setConfirming(false); // a fresh bundle invalidates the open confirm dialog
    if (!file) return;
    const reader = new FileReader();
    reader.onload = () => {
      const text = String(reader.result || '');
      setBundleText(text);
      // Parse just the header summary for the pre-import preview. Invalid JSON
      // is surfaced at import time; there is simply nothing to preview then.
      try {
        setPreview(bundleSummary(JSON.parse(text)));
      } catch {
        setPreview(null);
      }
    };
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
    // The backend returns a single JSON report after completion rather than
    // streaming progress, so the bar is filled in post-hoc from the final
    // report. Live per-chunk streaming is deferred to a later task.
    setProgress({ active: true, total: null });
    try {
      const resp = await importData(bundle, importList);
      const report = resp?.import;
      const inserted = report?.total_inserted || 0;
      setResult({ type: 'import', resp });
      setProgress({ active: false, total: inserted });
      if (report?.partial) {
        toast.error(`Import completed with skipped chunks — ${inserted} inserted. See the per-category Error column.`);
      } else {
        toast.success(`Import complete — ${inserted} ${inserted === 1 ? 'row' : 'rows'} inserted.`);
      }
    } catch (e) {
      setError(e);
      setProgress(null);
    } finally {
      setImporting(false);
      setConfirming(false);
    }
  }

  const previewEntries = buildPreviewEntries(resources, preview);
  const previewTotal = previewEntries.reduce((sum, [, c]) => sum + (Number(c) || 0), 0);
  const importSelCount = importList.length;

  function renderReport() {
    if (!result || result.type !== 'import') return null;
    const imp = result.resp?.import || {};
    const resourcesReport = imp.resources || {};
    const entries = Object.entries(resourcesReport);
    const partial = !!imp.partial;
    // Total rows that were skipped across every category, for the banner copy.
    const skippedTotal = entries.reduce(
      (sum, [, r]) => sum + (Number(r?.skipped) || 0), 0,
    );
    return (
      <div className="card" style={{ marginTop: 16 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Import result</h3>
        {partial && (
          <div
            style={{
              marginTop: 12,
              padding: '10px 14px',
              borderRadius: 'var(--radius-sm)',
              fontSize: 13,
              background: 'var(--warning-dim)',
              border: '1px solid var(--warning)',
              color: 'var(--warning)',
            }}
          >
            Some data could not be restored — {skippedTotal} row{skippedTotal === 1 ? '' : 's'} skipped.
            Check the Error column below for the affected categories.
          </div>
        )}
        {entries.length === 0 ? (
          <p className="muted">No resources were restored.</p>
        ) : (
          <table className="table" style={{ marginTop: 12 }}>
            <thead>
              <tr><th>Category</th><th>Inserted</th><th>Skipped</th><th>Error</th></tr>
            </thead>
            <tbody>
              {entries.map(([k, r]) => (
                <tr key={k}>
                  <td>{resourceLabel(k)}</td>
                  <td>{r.inserted}</td>
                  <td>{r.skipped}</td>
                  <td>{r.error ? r.error : <span className="muted">—</span>}</td>
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
            include — export defaults to everything, import starts with nothing.
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
            value={exportSel}
            onChange={(res) => setExportSel((prev) => toggleIn(prev, res))}
            onAll={() => setExportSel(allSelected())}
            onNone={() => setExportSel({})}
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
            value={importSel}
            onChange={(res) => setImportSel((prev) => toggleIn(prev, res))}
            onAll={() => setImportSel(allSelected())}
            onNone={() => setImportSel({})}
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

          {previewEntries.length > 0 && (
            <div className="card" style={{ marginTop: 16, borderColor: 'var(--warning)' }}>
              <div style={{ marginBottom: 8 }}>
                <strong>This file contains</strong>
                <p className="muted">
                  Importing will wipe and replace these categories — {previewTotal} rows total.
                </p>
              </div>
              <table className="table">
                <thead>
                  <tr><th>Category</th><th>Rows</th></tr>
                </thead>
                <tbody>
                  {previewEntries.map(([k, count]) => (
                    <tr key={k}>
                      <td>{resourceLabel(k)}</td>
                      <td>{count}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          {progress && (
            <div className="row gap-sm" style={{ marginTop: 16 }}>
              <progress
                value={progress.active ? undefined : 1}
                max={1}
                style={{ flex: 1 }}
              />
              <span className="muted">
                {progress.active ? 'Importing…' : `${progress.total} rows inserted`}
              </span>
            </div>
          )}

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
                {importSelCount === 0 && (
                  <p className="muted" style={{ marginTop: 8 }}>
                    No categories are checked — the entire bundle will be restored.
                  </p>
                )}
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

// ResourceChecklist — controlled checkbox list of backup categories with
// Select all / none. value is a {resourceKey: bool} map; onChange(res) toggles
// one entry.
function ResourceChecklist({ resources, value, onChange, onAll, onNone }) {
  if (!resources || resources.length === 0) {
    return <p className="muted">Loading categories…</p>;
  }
  const count = resources.filter((r) => value && value[r]).length;
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
              checked={!!(value && value[r])}
              onChange={() => onChange(r)}
            />
            <span>{RESOURCE_LABELS[r] || r}</span>
          </label>
        ))}
      </div>
    </div>
  );
}
