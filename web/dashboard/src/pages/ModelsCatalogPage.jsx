import React, { useState, useMemo, useEffect, useCallback } from 'react';
import {
  listModelsCatalog, getModelsCatalogCount, getModelPricing, putModelPricing,
  syncModelsFromV1, ApiError,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import SyncPricingModal from '../components/SyncPricingModal.jsx';
import ModelEntryModal from '../components/ModelEntryModal.jsx';

const DEFAULT_PAGE_SIZE = 25;

// ModelsCatalogPage — paginated catalog view, synchronized from the live
// caller-facing /v1/models endpoint.
//
// Sync flow:
//   - On first mount, the dashboard calls POST /v0/management/models-catalog/
//     sync-from-v1. The server auto-picks the first caller API key from the
//     config_store `api-keys:` list (operators do not need to paste one),
//     runs an in-process GET /v1/models through the full Auth + policy
//     pipeline, and upserts the returned list into models_catalog.
//   - The page then renders the paginated catalog directly from PG.
//
// When "Show available only" is checked the dashboard calls sync first
// (one-shot), then fetches the catalog with availableOnly:false (the synced
// rows ARE the available models). When unchecked, the page shows the full
// persisted catalog regardless of current availability.
export default function ModelsCatalogPage() {
  const [page, setPage] = useState(1);
  const [provider, setProvider] = useState('');
  const [officialProvider, setOfficialProvider] = useState('');
  const [availableOnly, setAvailableOnly] = useState(true);
  const [manualSyncing, setManualSyncing] = useState(false);
  const [syncMessage, setSyncMessage] = useState('');
  const [syncError, setSyncError] = useState('');
  const [initialSyncDone, setInitialSyncDone] = useState(false);
  const [showPricingSync, setShowPricingSync] = useState(false);
  const [editingModel, setEditingModel] = useState(null); // null | {mode, initial}
  const [showCreateModel, setShowCreateModel] = useState(false);

  // The catalog fetch always pulls from PG. When the user has "Show
  // available only" enabled, we re-sync before fetching so the catalog
  // reflects the current /v1/models output.
  const {
    data, error, loading, reload,
  } = useAsync(
    () => listModelsCatalog({ page, pageSize: DEFAULT_PAGE_SIZE, provider, officialProvider, availableOnly: false }),
    [page, provider, officialProvider],
  );
  const { data: countData } = useAsync(() => getModelsCatalogCount(), []);

  // runSync fires the in-process /v1/models -> models_catalog sync.
  // When silent=true the spinner does not show (used for the initial
  // background sync on mount).
  const runSync = useCallback(async (silent = false) => {
    if (!silent) setManualSyncing(true);
    setSyncError('');
    try {
      const result = await syncModelsFromV1();
      setSyncMessage(result?.message || '');
      setSyncMessage((_) => `Synced ${result.synced || 0} models from /v1/models`);
      reload();
    } catch (err) {
      setSyncError(err.message || 'Sync failed');
    } finally {
      if (!silent) setManualSyncing(false);
    }
  }, [reload]);

  // Initial mount: auto-sync once so the catalog reflects current availability
  // before the user sees a (potentially stale) page. Subsequent syncs are
  // user-initiated via the "Sync now" button.
  useEffect(() => {
    if (initialSyncDone) return;
    setInitialSyncDone(true);
    runSync(true);
  }, [initialSyncDone, runSync]);

  // When the "available only" toggle is flipped on, re-sync before the next
  // catalog fetch so the filtered view is fresh.
  useEffect(() => {
    if (availableOnly) {
      runSync(true);
    }
  }, [availableOnly, runSync]);

  const models = data?.models || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;

  function handlePageChange(newPage) {
    if (newPage < 1 || newPage > totalPages) return;
    setPage(newPage);
  }
  function handleProviderChange(e) {
    setProvider(e.target.value);
    setPage(1);
  }
  function handleOfficialProviderChange(e) {
    setOfficialProvider(e.target.value);
    setPage(1);
  }
  function toggleAvailableOnly(e) {
    setAvailableOnly(e.target.checked);
    setPage(1);
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Available Models</h1>
          <div className="main__subtitle">
            Synced from the live caller-facing <code>GET /v1/models</code> endpoint.
            The server auto-uses the first <code>api-keys</code> entry from
            config_store as the auth token.
          </div>
        </div>
        <div className="row gap-sm">
          <button className="primary" onClick={() => setShowCreateModel(true)}>
            + Add model
          </button>
          <button onClick={() => setShowPricingSync(true)}>
            Sync pricing…
          </button>
          <button onClick={() => runSync(false)} disabled={manualSyncing}>
            {manualSyncing ? 'Syncing…' : 'Sync now'}
          </button>
          <button onClick={reload}>Refresh</button>
        </div>
      </div>

      <div className="card">
        <div className="row gap-lg" style={{ flexWrap: 'wrap' }}>
          <div className="row gap-sm">
            <label className="form__label" style={{ marginTop: 6 }}>Upstream Provider</label>
            <input
              type="text" style={{ width: 200 }}
              value={provider}
              onChange={handleProviderChange}
              placeholder="e.g. openai (proxy/router)"
            />
          </div>
          <div className="row gap-sm">
            <label className="form__label" style={{ marginTop: 6 }}>Provider</label>
            <input
              type="text" style={{ width: 200 }}
              value={officialProvider}
              onChange={handleOfficialProviderChange}
              placeholder="e.g. openai (official)"
            />
          </div>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input
              type="checkbox"
              checked={availableOnly}
              onChange={toggleAvailableOnly}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>
              Sync from /v1/models (auto-uses first caller key)
            </span>
          </label>
          {countData && (
            <span className="dim mono" style={{ marginLeft: 'auto' }}>
              catalog rows: {countData.count}
            </span>
          )}
        </div>
        {syncMessage && (
          <div className="dim" style={{ marginTop: 8, fontSize: 12 }}>{syncMessage}</div>
        )}
      </div>

      {syncError && (
        <div className="error-banner">
          <strong>Sync failed:</strong> {syncError}
        </div>
      )}
      <ErrorBanner error={error} onRetry={reload} />

      <div className="card" style={{ padding: 0 }}>
        {loading && <Spinner label="Loading catalog…" />}
        {!loading && !error && models.length === 0 && (
          <EmptyState
            title="Catalog is empty"
            hint="Click 'Sync now' to mirror /v1/models into PostgreSQL, or start the server with PGSTORE_DSN configured."
          />
        )}
        {!loading && !error && models.length > 0 && (
          <>
            <table className="table">
              <thead>
                <tr>
                  <th>Model ID</th>
                  <th>Upstream Provider</th>
                  <th>Provider</th>
                  <th>Display name</th>
                  <th>Context</th>
                  <th>Max output</th>
                  <th>Type</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {models.map((m, i) => (
                  <ModelRow
                    key={`${m.id}|${m.provider}|${i}`}
                    model={m}
                    onEdit={() => setEditingModel({ mode: 'edit', initial: m })}
                  />
                ))}
              </tbody>
            </table>
            <div style={{ padding: '0 16px 16px' }}>
              <Pager
                page={page}
                totalPages={totalPages}
                total={total}
                pageSize={DEFAULT_PAGE_SIZE}
                onPageChange={handlePageChange}
              />
            </div>
          </>
        )}
      </div>

      {showPricingSync && (
        <SyncPricingModal
          onClose={() => setShowPricingSync(false)}
          onApplied={() => reload()}
        />
      )}
      {showCreateModel && (
        <ModelEntryModal
          mode="create"
          initial={null}
          onClose={() => setShowCreateModel(false)}
          onSaved={() => { setShowCreateModel(false); reload(); }}
        />
      )}
      {editingModel && (
        <ModelEntryModal
          mode="edit"
          initial={editingModel.initial}
          onClose={() => setEditingModel(null)}
          onSaved={() => { setEditingModel(null); reload(); }}
          onDeleted={() => { setEditingModel(null); reload(); }}
        />
      )}
    </>
  );
}

function ModelRow({ model, onEdit }) {
  return (
    <tr>
      <td>
        <div className="mono">{model.id}</div>
        {model.user_defined && (
          <span className="badge badge--muted" style={{ marginTop: 2 }}>user-defined</span>
        )}
      </td>
      <td><span className="badge badge--muted">{model.provider}</span></td>
      <td><span className="badge">{model.official_provider || '—'}</span></td>
      <td>{model.display_name || '—'}</td>
      <td className="mono">{model.context_length ? model.context_length.toLocaleString() : '—'}</td>
      <td className="mono">{model.max_completion_tokens ? model.max_completion_tokens.toLocaleString() : '—'}</td>
      <td className="muted">{model.type}</td>
      <td>
        <div className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
          {onEdit && (
            <button onClick={onEdit} style={{ padding: '4px 10px', fontSize: 12 }}>
              Edit
            </button>
          )}
          <PricingButton modelId={model.id} />
        </div>
      </td>
    </tr>
  );
}

function PricingButton({ modelId }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button onClick={() => setOpen(true)} style={{ padding: '4px 10px', fontSize: 12 }}>
        Pricing
      </button>
      {open && <PricingModal modelId={modelId} onClose={() => setOpen(false)} />}
    </>
  );
}

function PricingModal({ modelId, onClose }) {
  const { data: pricing, error, loading, reload } = useAsync(() => getModelPricing(modelId), [modelId]);
  const [form, setForm] = useState(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  useEffect(() => {
    if (pricing && !form) {
      setForm({
        input_per_1m_usd: String(pricing.input_per_1m_usd ?? 0),
        output_per_1m_usd: String(pricing.output_per_1m_usd ?? 0),
        cached_input_per_1m_usd: String(pricing.cached_input_per_1m_usd ?? 0),
        cached_read_per_1m_usd: String(pricing.cached_read_per_1m_usd ?? 0),
        reasoning_per_1m_usd: String(pricing.reasoning_per_1m_usd ?? 0),
      });
    }
  }, [pricing]);

  async function handleSave(e) {
    e.preventDefault();
    setSaving(true);
    setSaveError('');
    try {
      await putModelPricing(modelId, {
        input_per_1m_usd: Number(form.input_per_1m_usd) || 0,
        output_per_1m_usd: Number(form.output_per_1m_usd) || 0,
        cached_input_per_1m_usd: Number(form.cached_input_per_1m_usd) || 0,
        cached_read_per_1m_usd: Number(form.cached_read_per_1m_usd) || 0,
        reasoning_per_1m_usd: Number(form.reasoning_per_1m_usd) || 0,
      });
      onClose();
    } catch (err) {
      setSaveError(err.message);
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal title={`Pricing — ${modelId}`} onClose={onClose}>
      {loading && <Spinner label="Loading pricing…" />}
      <ErrorBanner error={error} onRetry={reload} />
      {saveError && <div className="error-banner">{saveError}</div>}
      {form && (
        <form onSubmit={handleSave}>
          <p className="form__hint" style={{ marginBottom: 16 }}>
            All fields are USD per 1,000,000 tokens. A zero value means "no cost
            recorded" — usage events for this model contribute $0 toward budget caps.
          </p>
          <div className="grid grid--2">
            <div className="form__row">
              <label className="form__label">Input / 1M</label>
              <input type="number" step="0.000001" min="0" value={form.input_per_1m_usd}
                onChange={(e) => setForm({ ...form, input_per_1m_usd: e.target.value })} />
            </div>
            <div className="form__row">
              <label className="form__label">Output / 1M</label>
              <input type="number" step="0.000001" min="0" value={form.output_per_1m_usd}
                onChange={(e) => setForm({ ...form, output_per_1m_usd: e.target.value })} />
            </div>
            <div className="form__row">
              <label className="form__label">Cached input (cache creation) / 1M</label>
              <input type="number" step="0.000001" min="0" value={form.cached_input_per_1m_usd}
                onChange={(e) => setForm({ ...form, cached_input_per_1m_usd: e.target.value })} />
            </div>
            <div className="form__row">
              <label className="form__label">Cached read / 1M</label>
              <input type="number" step="0.000001" min="0" value={form.cached_read_per_1m_usd}
                onChange={(e) => setForm({ ...form, cached_read_per_1m_usd: e.target.value })} />
            </div>
            <div className="form__row">
              <label className="form__label">Reasoning / 1M</label>
              <input type="number" step="0.000001" min="0" value={form.reasoning_per_1m_usd}
                onChange={(e) => setForm({ ...form, reasoning_per_1m_usd: e.target.value })} />
            </div>
          </div>
          <div className="form__actions">
            <button type="button" onClick={onClose} disabled={saving}>Cancel</button>
            <button type="submit" className="primary" disabled={saving}>
              {saving ? 'Saving…' : 'Save Pricing'}
            </button>
          </div>
        </form>
      )}
    </Modal>
  );
}
