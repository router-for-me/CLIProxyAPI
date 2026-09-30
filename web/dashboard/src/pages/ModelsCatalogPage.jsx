import React, { useState, useMemo, useEffect, useCallback, useRef } from 'react';
import {
  listModelsCatalog, getModelsCatalogSummary, getModelsCatalogDistinct,
  getModelPricing, putModelPricing,
  syncModelsFromV1, getModelsCatalogSyncStatus, getStoredCallerKey, setStoredCallerKey,
  ApiError,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState, Modal, CatalogSkeleton } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import SyncPricingModal from '../components/SyncPricingModal.jsx';
import PricingSourcesModal from '../components/PricingSourcesModal.jsx';
import ModelEntryModal from '../components/ModelEntryModal.jsx';
import GlobalModelModal from '../components/GlobalModelModal.jsx';

const DEFAULT_PAGE_SIZE = 25;
const AUTO_REFRESH_INTERVAL_MS = 60 * 1000;
const DENSITY_STORAGE = 'nixllm.dashboard.modelsDensity';
const AUTOREFRESH_STORAGE = 'nixllm.dashboard.modelsAutorefresh';

// Empty-state copy per active scope.
const EMPTY_COPY = {
  live: {
    title: 'No live models available',
    hint: 'The in-memory registry has no live clients. Start a provider client, or switch to another scope to browse the persisted catalog.',
  },
  stale: {
    title: 'No stale models',
    hint: 'Every persisted catalog row is currently live in the registry.',
  },
  priced: {
    title: 'No priced models',
    hint: 'No catalog rows have non-zero pricing yet. Use "Sync pricing…" or set pricing inline.',
  },
  unpriced: {
    title: 'No unpriced models',
    hint: 'Every catalog row has pricing set.',
  },
  all: {
    title: 'Catalog is empty',
    hint: "Click 'Sync now' to mirror /v1/models into PostgreSQL, or start the server with PGSTORE_DSN configured.",
  },
};

const SORT_COLUMNS = [
  { key: 'id', label: 'Model ID' },
  { key: 'provider', label: 'Upstream Provider' },
  { key: 'official_provider', label: 'Provider' },
  { key: 'context_length', label: 'Context' },
  { key: 'max_completion_tokens', label: 'Max output' },
];

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
// "scope" is the single server-side catalog filter (live | stale | all |
// priced | unpriced), selected from the stat cards. The auto-sync on mount
// (silent) keeps models_catalog fresh so the filtered view reflects current
// availability.
export default function ModelsCatalogPage() {
  const [page, setPage] = useState(1);
  const [provider, setProvider] = useState('');
  const [officialProvider, setOfficialProvider] = useState('');
  const [scope, setScope] = useState('live'); // live | stale | all | priced | unpriced
  const [groupById, setGroupById] = useState(false); // distinct_ids
  const [query, setQuery] = useState('');
  const [sortKey, setSortKey] = useState('id');
  const [sortAsc, setSortAsc] = useState(true);
  const [manualSyncing, setManualSyncing] = useState(false);
  const [syncMessage, setSyncMessage] = useState('');
  const [syncError, setSyncError] = useState('');
  const [syncErrorType, setSyncErrorType] = useState('');
  const [initialSyncDone, setInitialSyncDone] = useState(false);
  const [showPricingSync, setShowPricingSync] = useState(false);
  const [showPricingSources, setShowPricingSources] = useState(false);
  const [editingModel, setEditingModel] = useState(null); // null | {mode, initial}
  const [showCreateModel, setShowCreateModel] = useState(false);
  const [globalModelId, setGlobalModelId] = useState(null); // null | model id for GlobalModelModal
  const [syncStatus, setSyncStatus] = useState(null);
  const [summary, setSummary] = useState(null);
  const [expandedId, setExpandedId] = useState(null);
  const [density, setDensity] = useState(() => readDensity());
  const [autoRefresh, setAutoRefresh] = useState(() => readAutoRefresh());
  // Sync caller key: PG-first deployments keep client keys hashed in the
  // api_keys table, so the server can never auto-pick a plaintext key. The
  // operator supplies one via the "Set sync key…" modal; it is remembered in
  // localStorage (same mechanism as FetchModelsInline) and passed through the
  // caller_key body field on every sync.
  const [showSyncKey, setShowSyncKey] = useState(false);
  const [moreOpen, setMoreOpen] = useState(false);
  const moreRef = useRef(null);
  // Debounce the free-text search so typing 8 chars doesn't fire 8 queries.
  const [debouncedQuery, setDebouncedQuery] = useState('');
  useEffect(() => {
    const t = setTimeout(() => setDebouncedQuery(query.trim()), 250);
    return () => clearTimeout(t);
  }, [query]);

  const sortParam = useMemo(() => {
    return (sortAsc ? '' : '-') + sortKey;
  }, [sortKey, sortAsc]);

  const {
    data, error, loading, reload,
  } = useAsync(
    () => listModelsCatalog({
      page, pageSize: DEFAULT_PAGE_SIZE, provider, officialProvider,
      scope, distinctIds: groupById, q: debouncedQuery, sort: sortParam,
    }),
    [page, provider, officialProvider, scope, groupById, debouncedQuery, sortParam],
  );
  const { data: statusData, reload: reloadStatus } = useAsync(() => getModelsCatalogSyncStatus(), []);
  const { data: summaryData, reload: reloadSummary } = useAsync(() => getModelsCatalogSummary(), []);
  const { data: distinctProviders } = useAsync(() => getModelsCatalogDistinct('provider'), []);
  const { data: distinctOfficial } = useAsync(() => getModelsCatalogDistinct('official_provider'), []);

  useEffect(() => { if (statusData) setSyncStatus(statusData); }, [statusData]);
  useEffect(() => { if (summaryData) setSummary(summaryData); }, [summaryData]);

  const refreshSyncStatus = useCallback(() => { reloadStatus(); reloadSummary(); }, [reloadStatus, reloadSummary]);

  // runSync fires the in-process /v1/models -> models_catalog sync.
  // When silent=true the spinner does not show (used for the initial
  // background sync on mount).
  // callerKey, when supplied, is used for THIS sync (e.g. just typed in the
  // sync-key modal even if the operator chose not to remember it); otherwise
  // the operator-stored key is used so PG-only deployments can sync even
  // though cfg.APIKeys is always empty there.
  const runSync = useCallback(async (silent = false, callerKey = '') => {
    if (!silent) setManualSyncing(true);
    setSyncError('');
    setSyncErrorType('');
    try {
      const key = callerKey || getStoredCallerKey();
      const result = await syncModelsFromV1(key);
      const n = result?.synced ?? 0;
      setSyncMessage(`Synced ${n} models from /v1/models`);
      reload();
      refreshSyncStatus();
    } catch (err) {
      const type = err?.payload?.error?.type || '';
      setSyncErrorType(type);
      setSyncError(err.message || 'Sync failed');
    } finally {
      if (!silent) setManualSyncing(false);
    }
  }, [reload, refreshSyncStatus]);

  // Initial mount: auto-sync once so the catalog reflects current availability
  // before the user sees a (potentially stale) page. Subsequent syncs are
  // user-initiated via the "Sync now" button.
  useEffect(() => {
    if (initialSyncDone) return;
    setInitialSyncDone(true);
    runSync(true);
  }, [initialSyncDone, runSync]);

  // Close the "More ▾" overflow menu on outside click / Escape.
  useEffect(() => {
    if (!moreOpen) return undefined;
    function onDocClick(e) {
      if (moreRef.current && !moreRef.current.contains(e.target)) {
        setMoreOpen(false);
      }
    }
    function onKey(e) {
      if (e.key === 'Escape') setMoreOpen(false);
    }
    document.addEventListener('mousedown', onDocClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [moreOpen]);

  // Auto-refresh: re-fetch catalog + sync status every 60s while visible.
  useAutoRefresh(() => { reload(); refreshSyncStatus(); }, AUTO_REFRESH_INTERVAL_MS, autoRefresh);

  const models = data?.models || [];
  const total = data?.total ?? 0;
  const totalPages = data?.total_pages ?? 0;
  const liveIDs = data?.live_ids || {};
  const initialSyncRunning = initialSyncDone && !syncStatus?.last_synced_at && !syncError;

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
  function handleSortChange(col) {
    if (col === sortKey) {
      setSortAsc((v) => !v);
    } else {
      setSortKey(col);
      setSortAsc(true);
    }
  }
  function handleDensityChange(next) {
    setDensity(next);
    writeDensity(next);
  }
  function handleToggleAutoRefresh() {
    setAutoRefresh((v) => { const nv = !v; writeAutoRefresh(nv); return nv; });
  }
  function handleStatClick(nextScope) {
    setScope(nextScope);
    setPage(1);
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Available Models</h1>
          <div className="main__subtitle">
            Catalog mirrored from the live caller-facing <code>GET /v1/models</code> endpoint.
            The server auto-uses the first <code>api-keys</code> entry from
            config_store; PG-first deployments should use "Set sync key…" to
            supply a plaintext key once (PG keys are stored hashed).
          </div>
        </div>
        <div className="row gap-sm" style={{ flexWrap: 'wrap' }}>
          <button className="primary" onClick={() => runSync(false)} disabled={manualSyncing}>
            {manualSyncing ? 'Syncing…' : 'Sync now'}
          </button>
          <button onClick={() => setShowCreateModel(true)}>+ Add model</button>
          <div className={`model-routes__bulk ${moreOpen ? 'model-routes__bulk--open' : ''}`} ref={moreRef}>
            <button
              type="button"
              className="model-routes__bulkbtn"
              onClick={() => setMoreOpen((v) => !v)}
              aria-expanded={moreOpen}
            >
              More ▾
            </button>
            {moreOpen && (
              <div className="model-routes__bulkmenu" role="menu">
                <button type="button" role="menuitem" onClick={() => { setShowPricingSync(true); setMoreOpen(false); }}>
                  Sync pricing…
                </button>
                <button type="button" role="menuitem" onClick={() => { setShowPricingSources(true); setMoreOpen(false); }}>
                  Pricing sources…
                </button>
                <button
                  type="button"
                  role="menuitem"
                  onClick={() => { setShowSyncKey(true); setMoreOpen(false); }}
                  title="Optionally set a plaintext caller key to probe /v1/models as. Needed on PG-first deployments where client keys are stored hashed."
                >
                  Set sync key…
                </button>
              </div>
            )}
          </div>
        </div>
      </div>

      <SyncStatusPill status={syncStatus} loading={initialSyncRunning} />

      {/* Header stat cards — clickable to flip the stale filter / scope. */}
      <StatsGrid summary={summary} scope={scope} onStatClick={handleStatClick} />

      <div className="card">
        {/* Toolbar: search + provider dropdowns + stale filter + density + autorefresh */}
        <div className="catalog-toolbar">
          <input
            type="text"
            className="search-input"
            placeholder="Search model id, name, provider…"
            value={query}
            onChange={(e) => { setQuery(e.target.value); setPage(1); }}
          />
          <select value={provider} onChange={handleProviderChange} style={{ width: 180 }}>
            <option value="">All upstream providers</option>
            {(distinctProviders?.values || []).map((p) => (
              <option key={p} value={p}>{p}</option>
            ))}
          </select>
          <select value={officialProvider} onChange={handleOfficialProviderChange} style={{ width: 180 }}>
            <option value="">All providers</option>
            {(distinctOfficial?.values || []).map((p) => (
              <option key={p} value={p}>{p}</option>
            ))}
          </select>
          <div className="seg-group">
            <button
              className={`seg-btn ${!groupById ? 'seg-btn--active' : ''}`}
              onClick={() => { setGroupById(false); setPage(1); }}
              title="One row per (model ID, provider)"
            >By provider</button>
            <button
              className={`seg-btn ${groupById ? 'seg-btn--active' : ''}`}
              onClick={() => { setGroupById(true); setPage(1); }}
              title="One row per model ID"
            >By model ID</button>
          </div>
          <div className="seg-group">
            <button
              className={`seg-btn ${density === 'compact' ? 'seg-btn--active' : ''}`}
              onClick={() => handleDensityChange('compact')}
            >Compact</button>
            <button
              className={`seg-btn ${density === 'comfortable' ? 'seg-btn--active' : ''}`}
              onClick={() => handleDensityChange('comfortable')}
            >Comfortable</button>
          </div>
          <div className="catalog-toolbar__spacer" />
          <span
            className={`autorefresh-chip ${autoRefresh ? '' : 'autorefresh-chip--off'}`}
            role="button"
            tabIndex={0}
            onClick={handleToggleAutoRefresh}
            onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); handleToggleAutoRefresh(); } }}
            title={autoRefresh ? 'Auto-refresh every 60s. Click to pause.' : 'Auto-refresh paused. Click to resume.'}
          >
            <span className="autorefresh-chip__dot" />
            {autoRefresh ? 'Auto-refresh on' : 'Auto-refresh off'}
          </span>
          <span className="catalog-toolbar__count">
            {loading ? 'loading…' : `${total} result${total === 1 ? '' : 's'}`}
          </span>
        </div>
        {syncMessage && !syncError && (
          <div className="dim" style={{ marginTop: 8, fontSize: 12 }}>{syncMessage}</div>
        )}
      </div>

      {syncError && (
        <SyncErrorBanner type={syncErrorType} message={syncError} onSetSyncKey={() => setShowSyncKey(true)} />
      )}
      <ErrorBanner error={error} onRetry={reload} />

      {showSyncKey && (
        <SyncKeyModal
          initialKey={getStoredCallerKey()}
          onClose={() => setShowSyncKey(false)}
          onSave={(key, remember) => {
            if (remember && key) setStoredCallerKey(key);
            else if (remember && !key) setStoredCallerKey('');
            setShowSyncKey(false);
            // key is the typed plaintext, used for this sync even when not
            // remembered (matches FetchModelsInline "fetch now, persist opt-in").
            runSync(false, key);
          }}
          onClear={() => {
            setStoredCallerKey('');
            setShowSyncKey(false);
            runSync(false, '');
          }}
        />
      )}

      <div className="card" style={{ padding: 0 }}>
        {loading && (
          <table className={`table table--${density}`}>
            <thead>
              <tr>{SORT_COLUMNS.map((c) => <th key={c.key}>{c.label}</th>)}<th>Pricing</th><th></th></tr>
            </thead>
            <tbody>
              <CatalogSkeleton columns={SORT_COLUMNS.length + 2} rows={6} />
            </tbody>
          </table>
        )}
        {!loading && !error && models.length === 0 && (
          <EmptyState title={EMPTY_COPY[scope].title} hint={EMPTY_COPY[scope].hint} />
        )}
        {!loading && !error && models.length > 0 && (
          <>
            <table className={`table table--${density}`}>
              <thead>
                <tr>
                  {SORT_COLUMNS.map((c) => (
                    <th
                      key={c.key}
                      className={`th-sortable ${sortKey === c.key ? 'th-sort--active' : ''}`}
                      onClick={() => handleSortChange(c.key)}
                    >
                      {c.label}
                      <span className="th-sort__icon">
                        {sortKey === c.key ? (sortAsc ? '▲' : '▼') : '↕'}
                      </span>
                    </th>
                  ))}
                  <th>Pricing</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {models.map((m) => (
                  <ModelRow
                    key={`${m.id}|${m.provider}`}
                    model={m}
                    liveIDs={liveIDs}
                    density={density}
                    isGlobal={groupById}
                    expanded={expandedId === `${m.id}|${m.provider}`}
                    onToggleExpand={() => setExpandedId((cur) =>
                      cur === `${m.id}|${m.provider}` ? null : `${m.id}|${m.provider}`,
                    )}
                    onEdit={() => setEditingModel({ mode: 'edit', initial: m })}
                    onGlobalEdit={(mid) => setGlobalModelId(mid)}
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
          onApplied={() => { reload(); reloadSummary(); }}
        />
      )}
      {showPricingSources && (
        <PricingSourcesModal
          onClose={() => setShowPricingSources(false)}
          onChanged={() => { reload(); reloadSummary(); }}
        />
      )}
      {showCreateModel && (
        <ModelEntryModal
          mode="create"
          initial={null}
          onClose={() => setShowCreateModel(false)}
          onSaved={() => { setShowCreateModel(false); reload(); reloadSummary(); }}
        />
      )}
      {editingModel && (
        <ModelEntryModal
          mode="edit"
          initial={editingModel.initial}
          onClose={() => setEditingModel(null)}
          onSaved={() => { setEditingModel(null); reload(); reloadSummary(); }}
          onDeleted={() => { setEditingModel(null); reload(); reloadSummary(); }}
        />
      )}
      {globalModelId && (
        <GlobalModelModal
          modelId={globalModelId}
          onClose={() => setGlobalModelId(null)}
          onSaved={() => { setGlobalModelId(null); reload(); reloadSummary(); }}
        />
      )}
    </>
  );
}

function ModelRow({ model, liveIDs, density, isGlobal, expanded, onToggleExpand, onEdit, onGlobalEdit }) {
  const isLive = liveIDs ? !!liveIDs[String(model.id || '').toLowerCase()] : true;
  return (
    <>
      <tr
        className={expanded ? 'mc-row--expanded' : ''}
        onClick={onToggleExpand}
        aria-expanded={expanded}
        tabIndex={0}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onToggleExpand(); } }}
        title={expanded ? 'Collapse details' : 'Expand details'}
      >
        <td>
          <div className="mono mc-model-id">
            <ExpandChevron expanded={expanded} />
            <LiveBadge live={isLive} />
            <span>{model.id}</span>
          </div>
          {model.user_defined && (
            <span className="badge badge--muted" style={{ marginTop: 2 }}>user-defined</span>
          )}
        </td>
        <td><span className="badge badge--muted">{model.provider}</span></td>
        <td><span className="badge badge--info">{model.official_provider || '—'}</span></td>
        <td className="mono">{formatTokens(model.context_length)}</td>
        <td className="mono">{formatTokens(model.max_completion_tokens)}</td>
        <td><InlinePricing model={model} /></td>
        <td onClick={(e) => e.stopPropagation()}>
          <div className="row-actions">
            <button
              className="row-actions__btn row-actions__btn--primary"
              onClick={onEdit}
              title="Edit this model's catalog entry"
            >Edit</button>
            {isGlobal && (
              <button
                className="row-actions__btn"
                onClick={() => onGlobalEdit?.(model.id)}
                title="Edit pricing / fields for every row with this model ID"
              >Global edit</button>
            )}
            <PricingButton modelId={model.id} />
          </div>
        </td>
      </tr>
      {expanded && (
        <tr className="row-expanded">
          <td colSpan={7}>
            <div className="row-expanded__grid">
              <ExpandedField label="Display name" value={model.display_name || '—'} />
              <ExpandedField label="Type" value={model.type || '—'} />
              <ExpandedField label="Name" value={model.name || '—'} />
              <ExpandedField label="Version" value={model.version || '—'} />
              <ExpandedField label="Object" value={model.object || '—'} />
              <ExpandedField label="Input token limit" value={model.input_token_limit ? model.input_token_limit.toLocaleString() : '—'} />
              <ExpandedField label="Output token limit" value={model.output_token_limit ? model.output_token_limit.toLocaleString() : '—'} />
              <ExpandedField label="Supports web search" value={model.supports_web_search ? 'yes' : 'no'} />
              <ExpandedField label="Input modalities" value={(model.input_modalities || []).join(', ') || '—'} />
              <ExpandedField label="Output modalities" value={(model.output_modalities || []).join(', ') || '—'} />
              <ExpandedField label="Updated" value={model.updated_at ? new Date(model.updated_at).toLocaleString() : '—'} />
            </div>
          </td>
        </tr>
      )}
    </>
  );
}

function ExpandedField({ label, value }) {
  return (
    <div className="row-expanded__field">
      <div className="row-expanded__label">{label}</div>
      <div className="row-expanded__value">{value}</div>
    </div>
  );
}

function InlinePricing({ model }) {
  const p = model.pricing;
  if (!p) {
    return <span className="pricing-inline pricing-inline--unset" title="Click Pricing to set">unpriced</span>;
  }
  return (
    <span className="pricing-inline mono" title="Click Pricing to edit">
      <span>I: ${fmtUSD(p.input_per_1m_usd)}/M</span>
      <span>O: ${fmtUSD(p.output_per_1m_usd)}/M</span>
    </span>
  );
}

function PricingButton({ modelId }) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        className="row-actions__btn row-actions__btn--pricing"
        onClick={() => setOpen(true)}
        title="Set USD-per-token pricing for this model"
      >Pricing</button>
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

// StatsGrid renders the 5 header stat cards sourced from /models-catalog/summary.
// Clicking a card sets the active scope directly.
function StatsGrid({ summary, scope, onStatClick }) {
  if (!summary) return null;
  const cards = [
    { key: 'all', label: 'All', value: summary.total ?? 0, hint: 'persisted catalog rows', active: scope === 'all' },
    { key: 'live', label: 'Live', value: summary.live ?? 0, hint: 'in registry right now', active: scope === 'live' },
    { key: 'stale', label: 'Stale', value: summary.stale ?? 0, hint: 'persisted but not live', active: scope === 'stale' },
    { key: 'priced', label: 'Priced', value: summary.priced ?? 0, hint: 'has non-zero pricing', active: scope === 'priced' },
    { key: 'unpriced', label: 'Unpriced', value: summary.unpriced ?? 0, hint: 'no pricing set', active: scope === 'unpriced' },
  ];
  return (
    <div className="stats-grid">
      {cards.map((c) => (
        <div
          key={c.key}
          className={`stat-card ${c.active ? 'stat-card--active' : ''}`}
          role="button"
          tabIndex={0}
          onClick={() => onStatClick(c.key)}
          onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') { e.preventDefault(); onStatClick(c.key); } }}
        >
          <div className="stat-card__value">{c.value.toLocaleString()}</div>
          <div className="stat-card__label">{c.label}</div>
          <div className="stat-card__hint">{c.hint}</div>
        </div>
      ))}
    </div>
  );
}

// LiveBadge renders the small dot in front of a model ID indicating whether
// the in-memory registry currently reports it as available. When live_ids is
// missing we default to neutral (no badge) rather than implying stale.
function LiveBadge({ live }) {
  if (live) {
    return <span className="live-dot live-dot--on" title="Live in registry" aria-label="live" />;
  }
  return <span className="live-dot live-dot--off" title="Not in current registry" aria-label="stale" />;
}

// ExpandChevron renders a small chevron affording that a catalog row is
// expandable. Rotates 90° when the row is expanded so the affordance gives
// continuous feedback.
function ExpandChevron({ expanded }) {
  return (
    <svg
      className={`mc-expand-chevron${expanded ? ' mc-expand-chevron--open' : ''}`}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="M6 3.5l4 4.5-4 4.5" />
    </svg>
  );
}

// formatRelativeTime renders a short "2m ago" / "just now" string from an
// RFC3339 timestamp. Returns '—' when the input is empty/invalid.
function formatRelativeTime(iso) {
  if (!iso) return '—';
  const t = new Date(iso);
  const ms = Date.now() - t.getTime();
  if (Number.isNaN(ms)) return '—';
  if (ms < 30 * 1000) return 'just now';
  const mins = Math.floor(ms / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return `${mins}m ago`;
  const hours = Math.floor(mins / 60);
  if (hours < 24) return `${hours}h ago`;
  const days = Math.floor(hours / 24);
  return `${days}d ago`;
}

// formatTokens renders a token count as a compact short form (e.g. 128000 →
// "128K"; 4000 → "4K"; 1000000 → "1M"). Returns '—' for zero/missing.
function formatTokens(n) {
  if (!n || n <= 0) return '—';
  if (n >= 1_000_000) {
    const v = n / 1_000_000;
    return (v % 1 === 0 ? v.toFixed(0) : v.toFixed(1)) + 'M';
  }
  if (n >= 1000) {
    const v = n / 1000;
    return (v % 1 === 0 ? v.toFixed(0) : v.toFixed(1)) + 'K';
  }
  return String(n);
}

// fmtUSD renders a USD-per-1M-tokens float as a compact readable string.
function fmtUSD(v) {
  if (v == null || v === 0) return '0';
  if (v < 0.01) return v.toFixed(4);
  if (v < 1) return v.toFixed(3);
  return v.toFixed(2);
}

// SyncStatusPill renders the "last synced N ago · key sk-abcd…" summary
// under the header, plus a refreshing hint while the initial mount sync is
// still in flight (no prior sync recorded).
function SyncStatusPill({ status, loading }) {
  if (loading) {
    return (
      <div className="sync-pill sync-pill--loading">
        <span className="spinner spinner--sm" /> Refreshing catalog from /v1/models…
      </div>
    );
  }
  if (!status) return null;
  const hasError = !!status.last_error;
  const ts = formatRelativeTime(status.last_synced_at);
  return (
    <div className={`sync-pill ${hasError ? 'sync-pill--error' : ''}`}>
      {hasError ? (
        <>
          <span className="sync-pill__dot sync-pill__dot--err" />
          <span>Last sync failed{status.last_error_type ? ` · ${status.last_error_type}` : ''}</span>
        </>
      ) : (
        <>
          <span className="sync-pill__dot" />
          <span>Last synced <strong>{ts}</strong> · {status.last_synced_count ?? 0} models</span>
        </>
      )}
      {status.caller_key_prefix && (
        <span className="dim mono" style={{ marginLeft: 4 }}>· key {status.caller_key_prefix}</span>
      )}
      {typeof status.live_available_count === 'number' && (
        <span className="dim" style={{ marginLeft: 4 }}>· {status.live_available_count} live in registry</span>
      )}
    </div>
  );
}

// SyncErrorBanner renders a sync failure with an actionable hint derived
// from the error type so operators know what to fix.
function SyncErrorBanner({ type, message, onSetSyncKey }) {
  let hint = '';
  let action = null;
  if (type === 'no_caller_key') {
    // PG-first: cfg.APIKeys is always empty (api-keys never round-trips
    // through the runtime_config snapshot) and PG-managed keys are stored
    // hashed, so auto-pick cannot succeed. The operator must supply the
    // plaintext once via the sync-key modal — the server passes it as the
    // caller_key body field and re-validates the hash.
    hint = 'PG-managed keys are stored hashed, so the server cannot auto-pick a caller key. Set one to continue — it will be remembered for future syncs.';
    action = (
      <button className="primary" style={{ marginTop: 8 }} onClick={onSetSyncKey}>
        Set sync key…
      </button>
    );
  } else if (type === 'v1_models_probe_failed') {
    hint = 'The in-process /v1/models probe could not run. Check that a provider client is connected and the caller key is valid.';
  } else if (type === 'v1_models_decode_failed') {
    hint = 'The /v1/models response was not valid JSON. This usually means the registry is mid-refresh — retry.';
  }
  return (
    <div className="error-banner">
      <strong>Sync failed:</strong> {message}
      {hint && <div className="dim" style={{ marginTop: 4, fontSize: 12 }}>{hint}</div>}
      {action}
    </div>
  );
}

// SyncKeyModal collects the plaintext caller key the /v1/models probe should
// authenticate as. It mirrors the manage-cpa FetchModelsInline bearer-token
// pattern: password input + optional "remember" checkbox persisted via
// setStoredCallerKey (localStorage). onSave(key, shouldRemember) is invoked on
// submit: key is the typed plaintext (used for the immediate sync even when
// not remembered, matching FetchModelsInline); shouldRemember decides whether
// it is persisted for future syncs. onClear() resets the stored key.
function SyncKeyModal({ initialKey = '', onClose, onSave, onClear }) {
  const [key, setKey] = useState(initialKey);
  const [remember, setRemember] = useState(true);
  const hasStored = !!initialKey;
  const trimmed = key.trim();

  function handleSave() {
    onSave(trimmed, remember);
  }

  return (
    <Modal title="Sync caller key" onClose={onClose} size="sm" footer={(
      <div className="form__actions">
        <button onClick={onClose}>Cancel</button>
        <button className="primary" onClick={handleSave} disabled={!trimmed && !hasStored}>
          Save &amp; sync
        </button>
      </div>
    )}>
      <div className="form__row">
        <label className="form__label">Plaintext caller API key</label>
        <input
          type="password"
          value={key}
          onChange={(e) => setKey(e.target.value)}
          placeholder="sk-…"
          spellCheck={false}
          autoComplete="off"
        />
        <div className="form__hint">
          The key is used to probe <code>GET /v1/models</code> and is validated
          against the <code>api_keys</code> table (hash match). It is never sent
          to any third party — only to this server&apos;s management API as the
          <code> caller_key</code> body field.
        </div>
      </div>
      <label className="row gap-sm" style={{ fontSize: 12, color: 'var(--text-dim)' }}>
        <input
          type="checkbox"
          checked={remember}
          onChange={(e) => setRemember(e.target.checked)}
          style={{ width: 'auto' }}
        />
        <span>Remember this key for future syncs (stored only in this browser).</span>
      </label>
      {hasStored && (
        <button
          type="button"
          className="linklike"
          style={{ marginTop: 10, fontSize: 12 }}
          onClick={onClear}
        >
          Clear stored key
        </button>
      )}
    </Modal>
  );
}

// readDensity / writeDensity persist the user's density preference so it
// survives reloads. Defaults to 'comfortable'.
function readDensity() {
  try { return localStorage.getItem(DENSITY_STORAGE) || 'comfortable'; } catch { return 'comfortable'; }
}
function writeDensity(v) {
  try { localStorage.setItem(DENSITY_STORAGE, v); } catch { /* ignore */ }
}

// readAutoRefresh / writeAutoRefresh persist the user's auto-refresh toggle.
function readAutoRefresh() {
  try { return localStorage.getItem(AUTOREFRESH_STORAGE) !== 'off'; } catch { return true; }
}
function writeAutoRefresh(v) {
  try { localStorage.setItem(AUTOREFRESH_STORAGE, v ? 'on' : 'off'); } catch { /* ignore */ }
}
