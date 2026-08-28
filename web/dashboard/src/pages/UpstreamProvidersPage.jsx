import React, { useState, useMemo, useRef, useEffect, useCallback } from 'react';
import {
  listUpstreamProviders,
  createUpstreamProvider,
  updateUpstreamProvider,
  deleteUpstreamProvider,
  requestOAuthUrl,
  submitOAuthCallback,
  oauthChannelToAuthProvider,
  listAuthFiles,
  getAuthFileModels,
  fetchAuthFileJSON,
  uploadAuthFile,
  uploadAuthFileRaw,
  getOAuthModelAlias,
  patchOAuthModelAlias,
  deleteOAuthModelAlias,
  getModelDefinitions,
} from '../api/client.js';
import { ApiError } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';
import {
  Field,
  PasswordInput,
  ToggleRow,
  ChipListEditor,
  KeyValueEditor,
  ModelListEditor,
} from './manage-cpa/FormPrimitives.jsx';
import FetchModelsInline from './manage-cpa/FetchModelsInline.jsx';

// ============================================================================
// Provider type catalog
// ============================================================================

const API_KEY_TYPES = [
  { value: 'gemini-api-key', label: 'Gemini (API Key)', simple: 'gemini' },
  { value: 'interactions-api-key', label: 'Interactions (API Key)', simple: 'interactions' },
  { value: 'codex-api-key', label: 'Codex (API Key)', simple: 'codex' },
  { value: 'xai-api-key', label: 'xAI (API Key)', simple: 'xai' },
  { value: 'claude-api-key', label: 'Claude (API Key)', simple: 'claude' },
  { value: 'vertex-api-key', label: 'Vertex (API Key)', simple: 'vertex' },
  { value: 'openai-compatibility', label: 'OpenAI Compatibility', simple: 'openai' },
];
const OAUTH_TYPES = [
  { value: 'oauth:claude', label: 'Claude (OAuth)', simple: 'claude' },
  { value: 'oauth:codex', label: 'Codex (OAuth)', simple: 'codex' },
  { value: 'oauth:kimi', label: 'Kimi (OAuth)', simple: 'kimi' },
  { value: 'oauth:xai', label: 'xAI (OAuth)', simple: 'xai' },
  { value: 'oauth:vertex', label: 'Vertex (OAuth)', simple: 'vertex' },
  { value: 'oauth:aistudio', label: 'AI Studio (OAuth)', simple: 'aistudio' },
  { value: 'oauth:antigravity', label: 'Antigravity (OAuth)', simple: 'antigravity' },
];
const ALL_TYPES = [...API_KEY_TYPES, ...OAUTH_TYPES];
const TYPE_LABEL = Object.fromEntries(ALL_TYPES.map((t) => [t.value, t.label]));
const TYPE_SIMPLE = Object.fromEntries(ALL_TYPES.map((t) => [t.value, t.simple]));

const isOAuth = (t) => String(t || '').startsWith('oauth:');
const isOpenAI = (t) => t === 'openai-compatibility';
const isClaude = (t) => t === 'claude-api-key' || t === 'oauth:claude';

// Types where the FetchModelsInline probe is meaningful (has a base_url +
// api_key the operator can probe, or an auth-file the registry tracks).
const FETCHABLE_TYPES = new Set([
  'gemini-api-key', 'interactions-api-key', 'codex-api-key', 'xai-api-key',
  'claude-api-key', 'vertex-api-key', 'openai-compatibility',
]);

// URL_RE matches the *start* of a value: a real URL scheme, or the literal
// "direct"/"none" proxy bypass sentinels. Empty values are allowed (they
// mean "use the provider default" for base_url, or "use the global proxy"
// for proxy_url). The $ anchor is intentionally omitted so a full URL like
// "https://api.example.com" passes — only the prefix is checked.
const URL_RE = /^(https?:\/\/|socks5h?:\/\/|direct|none)/i;

// ============================================================================
// Page
// ============================================================================

export default function UpstreamProvidersPage() {
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => listUpstreamProviders(), []);
  const [search, setSearch] = useState('');
  const [typeFilter, setTypeFilter] = useState('');
  const [editing, setEditing] = useState(null);
  const [confirmDelete, setConfirmDelete] = useState(null);
  const [importing, setImporting] = useState(false);
  // Table sort: { key, dir } — null = leave server order. Keys map 1:1 to
  // row fields so the sort happens purely client-side over the filtered list.
  const [sort, setSort] = useState({ key: 'updated_at', dir: 'desc' });
  // Client-side pagination. Rows/page is operator-selectable; the current page
  // resets whenever filters/search/sort change so the operator never lands on
  // an out-of-range page after narrowing the list.
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  // Bulk-action selection. Stored as a Set of upstream_provider.id values so
  // it survives filter/sort/page changes (operators can narrow, act, then
  // re-broaden without losing the selection). `null` when no bulk action is
  // in flight; otherwise the action identifier ("delete" | "enable" | "disable").
  const [selectedIds, setSelectedIds] = useState(new Set());
  const [bulkAction, setBulkAction] = useState(null);   // pending confirm
  const [bulkRunning, setBulkRunning] = useState(false); // operation in flight
  // Confirm-modal for destructive bulk delete (non-destructive Enable/Disable
  // confirm inline via a single toast, matching how single-row toggles work).
  const [confirmBulkDelete, setConfirmBulkDelete] = useState(null);

  const providers = data?.providers || [];

  // typeFilter accepts three shapes:
  //   ''                 → no filter
  //   'api' / 'oauth'    → category filter (from the stat tiles)
  //   '<provider_type>'  → exact match (from the dropdown + channel chips)
  function matchesTypeFilter(providerType) {
    if (!typeFilter) return true;
    if (typeFilter === 'api') return !isOAuth(providerType);
    if (typeFilter === 'oauth') return isOAuth(providerType);
    return providerType === typeFilter;
  }

  const filtered = useMemo(() => {
    let out = providers;
    if (typeFilter) out = out.filter((p) => matchesTypeFilter(p.provider_type));
    if (search.trim()) {
      const q = search.trim().toLowerCase();
      out = out.filter((p) =>
        [p.provider_type, p.name, p.label, p.email, p.file_name, p.base_url]
          .filter(Boolean).some((v) => v.toLowerCase().includes(q)));
    }
    if (sort && sort.key) {
      const k = sort.key;
      const dir = sort.dir === 'asc' ? 1 : -1;
      out = [...out].sort((a, b) => {
        const av = a?.[k];
        const bv = b?.[k];
        if (av == null && bv == null) return 0;
        if (av == null) return 1;       // nulls last
        if (bv == null) return -1;
        if (typeof av === 'number' && typeof bv === 'number') return (av - bv) * dir;
        return String(av).localeCompare(String(bv), undefined, { numeric: true, sensitivity: 'base' }) * dir;
      });
    }
    return out;
  }, [providers, search, typeFilter, sort]);

  // Reset to page 1 whenever the result set's shape changes (filter, search,
  // sort, or the underlying provider list). Without this the operator can
  // narrow the list and land on an empty page.
  useEffect(() => { setPage(1); }, [search, typeFilter, sort, providers.length]);

  const totalFiltered = filtered.length;
  const totalPages = Math.max(1, Math.ceil(totalFiltered / pageSize));
  const safePage = Math.min(page, totalPages);
  const pageStart = totalFiltered === 0 ? 0 : (safePage - 1) * pageSize + 1;
  const pageEnd = Math.min(totalFiltered, safePage * pageSize);
  const pagedRows = useMemo(
    () => filtered.slice(pageStart - 1, pageEnd),
    [filtered, pageStart, pageEnd],
  );

  // Resolve the Set of selected IDs back to provider rows. Drops any
  // selections that no longer exist in `providers` (e.g. after a reload
  // following a delete from another session). Recomputed on every render so
  // the bulk toolbar always reflects current truth.
  const selectedProviders = useMemo(() => {
    if (selectedIds.size === 0) return [];
    const byId = new Map(providers.map((p) => [p.id, p]));
    const out = [];
    for (const id of selectedIds) {
      const p = byId.get(id);
      if (p) out.push(p);
    }
    return out;
  }, [selectedIds, providers]);
  const selectedCount = selectedProviders.length;
  // Header checkbox is in three states: empty (none of the paged rows
  // selected), all (every paged row selected), indeterminate (mixed).
  const pagedSelectedCount = pagedRows.reduce(
    (n, p) => n + (selectedIds.has(p.id) ? 1 : 0), 0,
  );
  const allPagedSelected = pagedRows.length > 0 && pagedSelectedCount === pagedRows.length;
  const somePagedSelected = pagedSelectedCount > 0 && !allPagedSelected;

  const toggleRowSelected = (id) => {
    setSelectedIds((s) => {
      const next = new Set(s);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };
  const toggleAllPaged = () => {
    setSelectedIds((s) => {
      const next = new Set(s);
      if (allPagedSelected) {
        // Unselect everything currently paged.
        for (const p of pagedRows) next.delete(p.id);
      } else {
        // Select everything currently paged.
        for (const p of pagedRows) next.add(p.id);
      }
      return next;
    });
  };
  const clearSelection = () => setSelectedIds(new Set());

  // Garbage-collect stale IDs from the selection whenever the providers list
  // changes (after reload). Keeps `selectedProviders` honest without forcing
  // the operator to re-tick boxes after a delete from another tab.
  useEffect(() => {
    if (selectedIds.size === 0) return;
    const liveIds = new Set(providers.map((p) => p.id));
    let changed = false;
    for (const id of selectedIds) {
      if (!liveIds.has(id)) { changed = true; break; }
    }
    if (changed) {
      const next = new Set();
      for (const id of selectedIds) if (liveIds.has(id)) next.add(id);
      setSelectedIds(next);
    }
    // We intentionally key off providers.length + first/last id instead of the
    // full `providers` array to avoid recomputing on unrelated edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providers.length, providers[0]?.id, providers[providers.length - 1]?.id]);

  const counts = useMemo(() => {
    const c = { total: providers.length, apiKeys: 0, oauth: 0, disabled: 0 };
    for (const p of providers) {
      if (isOAuth(p.provider_type)) c.oauth += 1; else c.apiKeys += 1;
      if (p.disabled) c.disabled += 1;
    }
    return c;
  }, [providers]);

  // Per-channel OAuth counts for the secondary stat strip — surfaces which
  // OAuth channels are actually configured without forcing the operator to
  // scan the type filter.
  const oauthByChannel = useMemo(() => {
    const m = new Map();
    for (const p of providers) {
      if (!isOAuth(p.provider_type)) continue;
      const ch = p.provider_type.replace(/^oauth:/, '');
      m.set(ch, (m.get(ch) || 0) + 1);
    }
    return m;
  }, [providers]);

  const toggleSort = (key) => {
    setSort((s) => {
      if (s?.key !== key) return { key, dir: 'asc' };
      if (s.dir === 'asc') return { key, dir: 'desc' };
      return null; // third click clears the sort
    });
  };
  const SortHeader = ({ k, children, align = 'left' }) => {
    const active = sort?.key === k;
    const arrow = active ? (sort.dir === 'asc' ? '▲' : '▼') : '↕';
    return (
      <th
        className={`th-sort${active ? ' is-active' : ''}`}
        onClick={() => toggleSort(k)}
        aria-sort={active ? (sort.dir === 'asc' ? 'ascending' : 'descending') : 'none'}
        style={align === 'right' ? { textAlign: 'right' } : undefined}
        title="Click to sort"
      >
        {children}<span className="th-sort__arrow" aria-hidden="true">{arrow}</span>
      </th>
    );
  };

  const hasFilters = !!search.trim() || !!typeFilter;

  const siblingNames = useMemo(
    () => providers.filter((p) => isOpenAI(p.provider_type)).map((p) => p.name).filter(Boolean),
    [providers],
  );

  // Existing (provider_type, file_name) pairs already represented as upstream
  // provider rows. The Import modal uses this to mark auth files that are
  // already imported (and to detect 409 conflicts on create ahead of time).
  const existingProviderKeys = useMemo(() => {
    const s = new Set();
    for (const p of providers) {
      if (p.provider_type && p.file_name) {
        s.add(`${p.provider_type}|${p.file_name}`);
      }
    }
    return s;
  }, [providers]);

  const handleDelete = async (p) => {
    try {
      await deleteUpstreamProvider(p.id);
      toast.success(`Deleted ${TYPE_LABEL[p.provider_type] || p.provider_type}`);
      setConfirmDelete(null);
      reload();
    } catch (err) {
      toast.error(err.message || 'Delete failed');
    }
  };

  // runBulk performs a single bulk action ("enable" | "disable" | "delete")
  // over the currently selected providers. Runs the per-row mutations in
  // parallel via Promise.allSettled so a single 4xx/5xx doesn't block the
  // rest. Reports a final summary toast (`X succeeded, Y failed`) and
  // collapses the bulk toolbar (clears selection) on full success. On
  // partial failure the operator keeps the selection so they can retry
  // just the failures.
  const runBulk = async (action) => {
    if (selectedCount === 0) return;
    setBulkRunning(true);
    const progressId = toast.info(
      `${action === 'delete' ? 'Deleting' : action === 'enable' ? 'Enabling' : 'Disabling'} ${selectedCount} provider${selectedCount === 1 ? '' : 's'}…`,
      { duration: 0 },
    );
    const targets = selectedProviders.slice();
    const verbs = { enable: 'enable', disable: 'disable', delete: 'delete' };
    const tasks = targets.map((p) => {
      if (action === 'delete') return deleteUpstreamProvider(p.id).then(() => p);
      // Enable / Disable: PUT with the smallest payload possible so we don't
      // clobber fields the operator didn't intend to change. The server only
      // cares about `disabled` here.
      return updateUpstreamProvider(p.id, { disabled: action === 'disable' }).then(() => p);
    });
    const settled = await Promise.allSettled(tasks);
    toast.dismiss(progressId);
    const ok = [];
    const failed = [];
    settled.forEach((r, i) => {
      if (r.status === 'fulfilled') ok.push(targets[i]);
      else failed.push({ p: targets[i], err: r.reason });
    });
    setBulkRunning(false);
    setBulkAction(null);
    if (failed.length === 0) {
      clearSelection();
      toast.success(
        `${capitalize(verbs[action])}d ${ok.length} provider${ok.length === 1 ? '' : 's'}`,
      );
      reload();
    } else if (ok.length > 0) {
      // Partial — drop the OK rows from the selection so retry targets the failures only.
      const failedIds = new Set(failed.map((f) => f.p.id));
      setSelectedIds((s) => {
        const next = new Set();
        for (const id of s) if (failedIds.has(id)) next.add(id);
        return next;
      });
      toast.error(
        `${verbs[action]}d ${ok.length}, ${failed.length} failed: ${failed[0].err?.message || 'unknown error'}` +
          (failed.length > 1 ? ` (+${failed.length - 1} more)` : ''),
        { duration: 7000 },
      );
      reload();
    } else {
      toast.error(
        `All ${failed.length} ${verbs[action]} calls failed: ${failed[0].err?.message || 'unknown error'}`,
        { duration: 7000 },
      );
    }
  };

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Upstream Providers</h1>
          <div className="main__subtitle">
            Normalized source of truth for every upstream provider credential.
            Edits re-render <code>config.yaml</code> + auth-dir artifacts and
            reload clients automatically.
          </div>
        </div>
        <div className="row gap-sm">
          <button
            className="secondary"
            onClick={() => { reload(); toast.info('Providers refreshed'); }}
            disabled={loading}
            aria-label="Refresh providers"
            title="Refresh"
          >
            ↻ Refresh
          </button>
          <button
            className="secondary"
            onClick={() => setImporting(true)}
            aria-label="Import OAuth provider"
            title="Import an existing auth file, pasted token JSON, or uploaded JSON files"
          >
            ↓ Import
          </button>
          <button className="primary" onClick={() => setEditing({})}>+ New Provider</button>
        </div>
      </div>

      <div className="row gap-sm" style={{ marginBottom: 12, flexWrap: 'wrap' }}>
        <Stat label="Total" value={counts.total} active={!hasFilters} onClick={() => { setTypeFilter(''); setSearch(''); }} />
        <Stat
          label="API Keys"
          value={counts.apiKeys}
          active={typeFilter === 'api' || (!!typeFilter && !isOAuth(typeFilter) && typeFilter !== 'oauth')}
          onClick={() => setTypeFilter(typeFilter === 'api' ? '' : 'api')}
        />
        <Stat
          label="OAuth"
          value={counts.oauth}
          active={typeFilter === 'oauth' || isOAuth(typeFilter)}
          onClick={() => setTypeFilter(typeFilter === 'oauth' ? '' : 'oauth')}
        />
        {counts.disabled > 0 && (
          <Stat label="Disabled" value={counts.disabled} dim
            onClick={() => { setSearch('disabled'); }} />
        )}
        {oauthByChannel.size > 0 && (
          <div className="row gap-sm" style={{ marginLeft: 'auto', flexWrap: 'wrap', alignItems: 'center' }}>
            <span className="dim" style={{ fontSize: 11, textTransform: 'uppercase', letterSpacing: '0.06em' }}>OAuth channels</span>
            {[...oauthByChannel.entries()].sort((a, b) => a[0].localeCompare(b[0])).map(([ch, n]) => (
              <button
                key={ch}
                className={`filter-chip${typeFilter === `oauth:${ch}` ? ' filter-chip--active' : ''}`}
                onClick={() => setTypeFilter(typeFilter === `oauth:${ch}` ? '' : `oauth:${ch}`)}
                title={`Filter to oauth:${ch}`}
                style={typeFilter === `oauth:${ch}` ? { borderColor: 'var(--accent)', color: 'var(--accent)' } : undefined}
              >
                {ch}<span className="dim">×{n}</span>
              </button>
            ))}
          </div>
        )}
      </div>

      <div className="card">
        <div className="catalog-toolbar">
          <input
            className="search-input"
            type="text"
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder="Search by name, type, email, base URL…"
            aria-label="Search upstream providers"
          />
          <select
            className="search-input"
            value={typeFilter}
            onChange={(e) => setTypeFilter(e.target.value)}
            aria-label="Filter by provider type"
            style={{ maxWidth: 220 }}
          >
            <option value="">All provider types</option>
            <optgroup label="API Key">{API_KEY_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
            <optgroup label="OAuth / File-backed">{OAUTH_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
          </select>
          {hasFilters && (
            <button
              className="ghost"
              onClick={() => { setSearch(''); setTypeFilter(''); }}
              aria-label="Clear all filters"
              title="Clear filters"
            >
              Clear filters
            </button>
          )}
          <span className="catalog-toolbar__spacer" />
          <span className="catalog-toolbar__count dim" style={{ fontSize: 11 }}>
            {totalFiltered === providers.length
              ? `${providers.length} provider${providers.length === 1 ? '' : 's'}`
              : `${totalFiltered} of ${providers.length}`}
          </span>
        </div>

        {loading ? (
          <Spinner label="Loading upstream providers…" />
        ) : error ? (
          <ErrorBanner error={error} onRetry={reload} />
        ) : filtered.length === 0 ? (
          <EmptyState
            title={hasFilters ? 'No providers match the filters' : 'No upstream providers yet'}
            hint={hasFilters
              ? 'Try clearing the search or the type filter.'
              : 'Get started by creating a new provider or importing an existing OAuth credential.'}
            actions={hasFilters ? (
              <button onClick={() => { setSearch(''); setTypeFilter(''); }}>Clear filters</button>
            ) : (
              <div className="row gap-sm">
                <button onClick={() => setImporting(true)}>↓ Import OAuth</button>
                <button className="primary" onClick={() => setEditing({})}>+ New Provider</button>
              </div>
            )}
          />
        ) : (
          <table className="table">
            <thead>
              <tr>
                <th className="col-check">
                  <input
                    type="checkbox"
                    aria-label="Select all on this page"
                    title={allPagedSelected ? 'Unselect this page' : 'Select this page'}
                    checked={allPagedSelected}
                    ref={(el) => { if (el) el.indeterminate = somePagedSelected; }}
                    onChange={toggleAllPaged}
                  />
                </th>
                <SortHeader k="provider_type">Provider</SortHeader>
                <SortHeader k="name">Identifier</SortHeader>
                <SortHeader k="priority">Priority</SortHeader>
                <SortHeader k="base_url">Base URL</SortHeader>
                <SortHeader k="disabled">Status</SortHeader>
                <SortHeader k="model_count">Models</SortHeader>
                <SortHeader k="updated_at">Updated</SortHeader>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {pagedRows.map((p) => {
                const ident = p.name || p.label || p.email || p.file_name || '—';
                const modelCount = (p.models || []).length;
                const isSel = selectedIds.has(p.id);
                return (
                  <tr key={p.id}
                    className={`clickable-row${isSel ? ' row--selected' : ''}`}
                    onClick={() => setEditing(p)}
                    style={{ cursor: 'pointer' }}
                  >
                    <td className="col-check" onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        aria-label={`Select ${ident}`}
                        checked={isSel}
                        onChange={() => toggleRowSelected(p.id)}
                      />
                    </td>
                    <td>
                      <div className="cell-stack">
                        <span className="cell-stack__main">{TYPE_LABEL[p.provider_type] || p.provider_type}</span>
                        <span className="badge badge--muted" style={{ fontSize: 10 }}>
                          {isOAuth(p.provider_type) ? 'oauth' : 'api-key'}
                        </span>
                      </div>
                    </td>
                    <td>
                      <div className="cell-stack">
                        <span className="cell-stack__main">{ident}</span>
                        {p.email && p.email !== ident && (
                          <span className="dim" style={{ fontSize: 11 }}>{p.email}</span>
                        )}
                      </div>
                    </td>
                    <td>
                      {p.priority > 0 ? (
                        <span className="badge badge--muted">{p.priority}</span>
                      ) : (
                        <span className="dim">0</span>
                      )}
                    </td>
                    <td className="truncate-cell" title={p.base_url || ''}>{p.base_url || <span className="dim">—</span>}</td>
                    <td>
                      <span className={`badge ${p.disabled ? 'badge--disabled' : 'badge--active'}`}>
                        {p.disabled ? 'disabled' : 'active'}
                      </span>
                    </td>
                    <td>{modelCount > 0 ? modelCount : <span className="dim">0</span>}</td>
                    <td className="dim" title={p.updated_at ? formatTime(p.updated_at) : ''}>
                      {p.updated_at ? formatRelativeTime(p.updated_at) : '—'}
                    </td>
                    <td>
                      <div className="row gap-sm" style={{ justifyContent: 'flex-end' }}>
                        <button className="btn-icon" title="Edit" aria-label="Edit provider"
                          onClick={(e) => { e.stopPropagation(); setEditing(p); }}>✎</button>
                        <button className="btn-icon" title="Delete" aria-label="Delete provider"
                          onClick={(e) => { e.stopPropagation(); setConfirmDelete(p); }}>✕</button>
                      </div>
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        {selectedCount > 0 && !loading && !error && totalFiltered > 0 && (
          <BulkActionBar
            count={selectedCount}
            total={totalFiltered}
            selected={selectedProviders}
            running={bulkRunning}
            onEnable={() => runBulk('enable')}
            onDisable={() => runBulk('disable')}
            onDelete={() => setConfirmBulkDelete(selectedProviders)}
            onClear={clearSelection}
          />
        )}
        {!loading && !error && totalFiltered > 0 && (
          <PaginationBar
            page={safePage}
            totalPages={totalPages}
            pageSize={pageSize}
            pageStart={pageStart}
            pageEnd={pageEnd}
            total={totalFiltered}
            onPageChange={setPage}
            onPageSizeChange={(n) => { setPageSize(n); setPage(1); }}
          />
        )}
      </div>

      {editing && (
        <UpstreamProviderEditor
          provider={editing}
          siblingNames={siblingNames}
          onClose={() => setEditing(null)}
          onSaved={() => { setEditing(null); reload(); }}
        />
      )}

      {importing && (
        <ImportOAuthProviderModal
          existingProviderKeys={existingProviderKeys}
          onClose={() => setImporting(false)}
          // Keep the modal open across successful imports so the operator can
          // keep importing multiple files in one session. Reload the parent
          // list so existingProviderKeys flows in fresh (drives Tab 1's
          // "already imported" badge), and bump a refresh signal so the modal
          // re-fetches its own auth-files list (drives Tab 1's row presence).
          onImported={() => { reload(); }}
        />
      )}

      {confirmDelete && (
        <Modal title="Delete upstream provider?" size="sm"
          onClose={() => setConfirmDelete(null)}
          footer={<>
            <button onClick={() => setConfirmDelete(null)}>Cancel</button>
            <button className="danger" onClick={() => handleDelete(confirmDelete)}>Delete</button>
          </>}
        >
          <p>
            Permanently delete{' '}
            <strong>{TYPE_LABEL[confirmDelete.provider_type] || confirmDelete.provider_type}</strong>
            {' ('}{confirmDelete.name || confirmDelete.label || confirmDelete.file_name}{')'}?
            Config.yaml and auth-dir artifacts will be re-rendered and the in-memory
            clients reloaded.
          </p>
        </Modal>
      )}

      {confirmBulkDelete && (
        <BulkDeleteConfirmModal
          targets={confirmBulkDelete}
          running={bulkRunning}
          onCancel={() => setConfirmBulkDelete(null)}
          onConfirm={async () => {
            const ids = new Set(confirmBulkDelete.map((p) => p.id));
            setConfirmBulkDelete(null);
            await runBulk('delete');
            // runBulk already cleared/updated selection + reloaded; ids is no
            // longer needed but kept here for symmetry with future flows.
            void ids;
          }}
        />
      )}

      <GlobalOAuthModelAliasCard />
    </>
  );
}

// Clickable stat tile. When onClick is provided the tile is rendered as a
// button so the operator can pivot the filter with one click (e.g. "OAuth"
// click sets the type filter to any oauth:* type). `active` highlights the
// current filter state. `dim` mutes the value for non-primary metrics like
// "Disabled" so they read as context rather than a call to action.
function Stat({ label, value, onClick, active = false, dim = false }) {
  const content = (
    <>
      <div className="stat__label">{label}</div>
      <div className="stat__value" style={dim ? { color: 'var(--text-muted)' } : undefined}>{value}</div>
    </>
  );
  if (onClick) {
    return (
      <button
        type="button"
        className="stat"
        onClick={onClick}
        aria-pressed={active}
        title={active ? `Click to clear "${label}" filter` : `Click to filter to "${label}"`}
        style={{
          textAlign: 'left',
          cursor: 'pointer',
          background: active ? 'var(--accent-dim)' : 'var(--bg-elevated)',
          border: `1px solid ${active ? 'var(--accent)' : 'var(--border)'}`,
          borderRadius: 'var(--radius-sm)',
          padding: '10px 14px',
          color: active ? 'var(--accent)' : 'inherit',
          transition: 'all 0.12s ease',
        }}
      >
        {content}
      </button>
    );
  }
  return <div className="stat">{content}</div>;
}

// PaginationBar — compact footer rendered under the table. Shows the visible
// row range, total count, current page, and a first/prev/next/last page
// navigator. Page-size selector uses common values (10/25/50/100). When the
// total fits on one page the prev/next buttons are disabled; the row-range
// text + page size still render so the operator sees the absolute total.
function PaginationBar({ page, totalPages, pageSize, pageStart, pageEnd, total, onPageChange, onPageSizeChange }) {
  const canPrev = page > 1;
  const canNext = page < totalPages;
  return (
    <div className="pagination-bar" role="navigation" aria-label="Table pagination">
      <div className="dim" style={{ fontSize: 11 }}>
        {total === 0
          ? '0 results'
          : <>Showing <strong>{pageStart}</strong>–<strong>{pageEnd}</strong> of <strong>{total}</strong></>}
      </div>
      <div className="row gap-sm" style={{ alignItems: 'center' }}>
        <label className="row gap-sm" style={{ alignItems: 'center', fontSize: 11, color: 'var(--text-muted)' }}>
          Rows
          <select
            value={pageSize}
            onChange={(e) => onPageSizeChange(Number(e.target.value))}
            aria-label="Rows per page"
            style={{ width: 'auto', padding: '4px 8px', fontSize: 12 }}
          >
            {[10, 25, 50, 100].map((n) => <option key={n} value={n}>{n}</option>)}
          </select>
        </label>
        <div className="row gap-sm" style={{ alignItems: 'center' }}>
          <button
            className="ghost"
            disabled={!canPrev}
            onClick={() => onPageChange(1)}
            aria-label="First page"
            title="First page"
          >«</button>
          <button
            className="ghost"
            disabled={!canPrev}
            onClick={() => onPageChange(page - 1)}
            aria-label="Previous page"
            title="Previous page"
          >‹ Prev</button>
          <span className="dim" style={{ fontSize: 11, minWidth: 60, textAlign: 'center' }}>
            Page <strong style={{ color: 'var(--text)' }}>{page}</strong> / {totalPages}
          </span>
          <button
            className="ghost"
            disabled={!canNext}
            onClick={() => onPageChange(page + 1)}
            aria-label="Next page"
            title="Next page"
          >Next ›</button>
          <button
            className="ghost"
            disabled={!canNext}
            onClick={() => onPageChange(totalPages)}
            aria-label="Last page"
            title="Last page"
          >»</button>
        </div>
      </div>
    </div>
  );
}

// BulkActionBar — appears under the table when one or more rows are
// selected. Shows the live selection count and three destructive / state
// actions. Enable/Disable fire immediately (mirrors single-row toggle
// semantics — no confirmation step), Delete opens a separate confirm modal
// via the parent because it cannot be undone.
function BulkActionBar({ count, total, selected, running, onEnable, onDisable, onDelete, onClear }) {
  const all = count === total;
  // Breakdown of selection by provider_type for the operator's situational
  // awareness — surfaces what category of credentials they're about to
  // affect without forcing them to scroll the table.
  const byType = useMemo(() => {
    const m = new Map();
    for (const p of selected) {
      const k = p.provider_type || 'unknown';
      m.set(k, (m.get(k) || 0) + 1);
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [selected]);
  return (
    <div className="bulk-action-bar" role="region" aria-label="Bulk actions">
      <div className="bulk-action-bar__count">
        <strong>{count}</strong> selected{all ? '' : <> of <strong>{total}</strong></>}
      </div>
      <div className="bulk-action-bar__breakdown">
        {byType.map(([k, n]) => (
          <span key={k} className="filter-chip" title={`${n} ${k}`}>
            {k}<span className="dim">×{n}</span>
          </span>
        ))}
      </div>
      <div className="bulk-action-bar__actions">
        <button onClick={onEnable} disabled={running} title="Enable selected">✓ Enable</button>
        <button onClick={onDisable} disabled={running} title="Disable selected">⊘ Disable</button>
        <button className="danger" onClick={onDelete} disabled={running} title="Delete selected (irreversible)">✕ Delete</button>
        <button className="ghost" onClick={onClear} disabled={running} title="Clear selection">Clear</button>
      </div>
    </div>
  );
}

// BulkDeleteConfirmModal — destructive-confirm pattern for the bulk delete
// path. Shows a count, breakdown by provider_type, and a list of identifiers
// (capped to 12 rows, with a "+N more" suffix) so the operator can confirm
// the exact set they're about to nuke. Config.yaml / auth-dir re-render
// warning is repeated here (mirrors the single-row confirm) because the
// bulk path doesn't go through the single-row modal first.
function BulkDeleteConfirmModal({ targets, running, onCancel, onConfirm }) {
  const maxRows = 12;
  const shown = targets.slice(0, maxRows);
  const more = targets.length - shown.length;
  return (
    <Modal
      title={`Delete ${targets.length} upstream provider${targets.length === 1 ? '' : 's'}?`}
      size="md"
      onClose={running ? () => {} : onCancel}
      footer={<>
        <button onClick={onCancel} disabled={running}>Cancel</button>
        <button className="danger" onClick={onConfirm} disabled={running}>
          {running ? 'Deleting…' : `Delete ${targets.length}`}
        </button>
      </>}
    >
      <p style={{ marginBottom: 12 }}>
        This action <strong>cannot be undone</strong>. The selected providers
        will be removed and <code>config.yaml</code> + auth-dir artifacts will
        be re-rendered. In-memory clients are reloaded automatically.
      </p>
      <ul className="bulk-confirm__list">
        {shown.map((p) => (
          <li key={p.id}>
            <code className="bulk-confirm__type">{p.provider_type}</code>
            <span className="bulk-confirm__ident">
              {p.name || p.label || p.email || p.file_name || `id:${p.id}`}
            </span>
          </li>
        ))}
        {more > 0 && <li className="dim">…and {more} more</li>}
      </ul>
    </Modal>
  );
}

// ============================================================================
// Global oauth-model-alias editor (config.yaml block)
// ============================================================================

// Channels the global oauth-model-alias config block supports. The backend
// sanitizer accepts arbitrary lowercased keys, but the runtime only honors
// these documented channels.
const OAUTH_ALIAS_CHANNELS = [
  { value: 'claude', label: 'Claude' },
  { value: 'codex', label: 'Codex' },
  { value: 'kimi', label: 'Kimi' },
  { value: 'xai', label: 'xAI' },
  { value: 'vertex', label: 'Vertex' },
  { value: 'aistudio', label: 'AI Studio' },
  { value: 'antigravity', label: 'Antigravity' },
];

// Normalizes a fetched global alias map into a stable shape the editor uses.
// API returns { "oauth-model-alias": { <channel>: [{name,alias,fork,display-name,force-mapping}] } }.
function normalizeAliasMap(raw) {
  const map = (raw && raw['oauth-model-alias']) || {};
  const out = {};
  Object.entries(map).forEach(([channel, aliases]) => {
    out[String(channel).toLowerCase()] = (Array.isArray(aliases) ? aliases : []).map((a) => ({
      'name': a.name || '',
      'alias': a.alias || '',
      'fork': !!a.fork,
      'display-name': a['display-name'] || a.displayName || '',
      'force-mapping': !!a['force-mapping'] || !!a.forceMapping,
    }));
  });
  return out;
}

// Serializes an editor channel's rows back into the API's kebab-case shape
// (dropping empty rows so the global map stays clean).
function serializeAliasRows(rows) {
  return (rows || [])
    .filter((r) => r && (r.name || r.alias))
    .map((r) => {
      const entry = {
        name: (r.name || '').trim(),
        alias: (r.alias || '').trim(),
      };
      if (r['display-name']) entry['display-name'] = (r['display-name'] || '').trim();
      if (r['fork']) entry['fork'] = true;
      if (r['force-mapping']) entry['force-mapping'] = true;
      return entry;
    });
}

function GlobalOAuthModelAliasCard() {
  const toast = useToast();
  const [channels, setChannels] = useState({});
  const [loading, setLoading] = useState(true);
  const [savingChannel, setSavingChannel] = useState('');
  const [error, setError] = useState('');
  // Tracks channels that were just added by the operator (not loaded from the
  // server). These mount expanded so the empty editor is immediately visible,
  // while all other channels default to collapsed for a scannable list.
  const newlyAddedRef = useRef(new Set());

  const reload = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const payload = await getOAuthModelAlias();
      setChannels(normalizeAliasMap(payload));
    } catch (err) {
      setError(err.message || 'Failed to load global aliases');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { reload(); }, [reload]);

  const updateRows = (channel, rows) => {
    setChannels((prev) => ({ ...prev, [channel]: rows }));
  };

  const addChannel = () => {
    // Find the first channel not yet present in the map and seed it with an
    // empty row so the editor reveals its rows immediately.
    const next = OAUTH_ALIAS_CHANNELS.find((c) => !channels[c.value]);
    if (!next) return;
    newlyAddedRef.current.add(next.value);
    setChannels((prev) => ({ ...prev, [next.value]: [{ name: '', alias: '' }] }));
  };

  const saveChannel = async (channel) => {
    setSavingChannel(channel);
    try {
      const aliases = serializeAliasRows(channels[channel] || []);
      await patchOAuthModelAlias(channel, aliases);
      toast.success(`Saved global aliases for ${channel}`);
      // Reload to pick up the server-sanitized view (empty channels are deleted).
      await reload();
    } catch (err) {
      toast.error(err.message || `Failed to save ${channel}`);
    } finally {
      setSavingChannel('');
    }
  };

  const removeChannel = async (channel) => {
    setSavingChannel(channel);
    try {
      await deleteOAuthModelAlias(channel);
      setChannels((prev) => {
        const next = { ...prev };
        delete next[channel];
        return next;
      });
      toast.success(`Removed ${channel} from global aliases`);
    } catch (err) {
      if (String(err.status) === '404') {
        // Already gone — drop locally.
        setChannels((prev) => {
          const next = { ...prev };
          delete next[channel];
          return next;
        });
      } else {
        toast.error(err.message || `Failed to remove ${channel}`);
      }
    } finally {
      setSavingChannel('');
    }
  };

  const presentChannels = Object.keys(channels).sort();
  const remainingAddable = OAUTH_ALIAS_CHANNELS.filter((c) => !channels[c.value]);
  const channelLabel = (c) => OAUTH_ALIAS_CHANNELS.find((x) => x.value === c)?.label || c;

  return (
    <div className="card" style={{ marginTop: 16 }}>
      <div className="main__header" style={{ marginBottom: 8 }}>
        <div>
          <h2 className="main__title" style={{ fontSize: 18 }}>Global OAuth Model Aliases</h2>
          <div className="main__subtitle">
            Per-channel model alias mappings written to the top-level{' '}
            <code>oauth-model-alias</code> block in <code>config.yaml</code>.
            These apply to every OAuth/file-backed account of that channel as a
            fallback; per-account aliases (edited above) override these.
          </div>
        </div>
        <div className="row gap-sm">
          <button onClick={reload} disabled={loading}>Refresh</button>
        </div>
      </div>

      {loading ? (
        <Spinner label="Loading global aliases…" />
      ) : error ? (
        <ErrorBanner error={error} onRetry={reload} />
      ) : (
        <>
          {presentChannels.length === 0 && remainingAddable.length === 0 && (
            <EmptyState title="No channels"
              hint="All defined channels are configured (or none are applicable)." />
          )}
          {presentChannels.length === 0 && remainingAddable.length > 0 && (
            <EmptyState title="No global aliases yet"
              hint="Add a channel to start mapping client aliases to OAuth upstream models." />
          )}

          {presentChannels.map((channel) => {
            const isNew = newlyAddedRef.current.has(channel);
            // Consume the "newly added" flag after first render so a later
            // reload re-collapses the channel (matching loaded-channel behavior).
            if (isNew) {
              queueMicrotask(() => { newlyAddedRef.current.delete(channel); });
            }
            return (
              <ChannelAliasEditor
                key={channel}
                channel={channel}
                label={channelLabel(channel)}
                rows={channels[channel] || []}
                onChange={(rows) => updateRows(channel, rows)}
                onSave={() => saveChannel(channel)}
                onRemove={() => removeChannel(channel)}
                saving={savingChannel === channel}
                initiallyExpanded={isNew}
              />
            );
          })}

          {remainingAddable.length > 0 && (
            <div className="row gap-sm" style={{ marginTop: 12 }}>
              <label style={{ fontSize: 12 }} className="dim">Add channel:</label>
              <select
                id="oauth_alias_add_channel"
                defaultValue=""
                onChange={(e) => {
                  const v = e.target.value;
                  if (!v) return;
                  newlyAddedRef.current.add(v);
                  setChannels((prev) => ({ ...prev, [v]: [{ name: '', alias: '' }] }));
                  e.target.value = '';
                }}
                aria-label="Add OAuth alias channel"
              >
                <option value="">Select a channel…</option>
                {remainingAddable.map((c) => (
                  <option key={c.value} value={c.value}>{c.label}</option>
                ))}
              </select>
            </div>
          )}
        </>
      )}
    </div>
  );
}

function ChannelAliasEditor({ channel, label, rows, onChange, onSave, onRemove, saving, initiallyExpanded = false }) {
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [modelOptions, setModelOptions] = useState([]);
  const [modelsLoading, setModelsLoading] = useState(false);
  const [modelsError, setModelsError] = useState('');
  // Channels default to collapsed so a long channel list stays scannable. Only
  // channels the operator just added (initiallyExpanded=true) start open so
  // the empty editor is immediately visible and ready to edit.
  const [collapsed, setCollapsed] = useState(!initiallyExpanded);

  // Fetch the static default model catalog for this OAuth channel once.
  // These IDs populate the "upstream model" dropdown alongside the free-text
  // input, so the operator can either pick a known model or type a custom one
  // (e.g. an internal/off-catalog id).
  useEffect(() => {
    let cancelled = false;
    setModelsError('');
    if (!channel) return;
    setModelsLoading(true);
    getModelDefinitions(channel)
      .then((payload) => {
        if (cancelled) return;
        const models = (payload && payload.models) || [];
        setModelOptions(models.map((m) => ({
          id: m.id || '',
          display: m.display_name || m.id || m.name || '',
        })).filter((m) => m.id));
      })
      .catch((err) => {
        if (cancelled) return;
        setModelsError(err.message || 'Failed to load default models');
      })
      .finally(() => { if (!cancelled) setModelsLoading(false); });
    return () => { cancelled = true; };
  }, [channel]);

  const update = (idx, patch) => {
    const next = rows.map((r, i) => (i === idx ? { ...r, ...patch } : r));
    onChange(next);
  };
  const addRow = () => onChange([...(rows || []), { name: '', alias: '' }]);
  const removeRow = (idx) => {
    if ((rows || []).length <= 1) {
      onChange([{ name: '', alias: '' }]);
      return;
    }
    onChange(rows.filter((_, i) => i !== idx));
  };
  const autoAddOnLastRow = (idx) => {
    if (idx === (rows || []).length - 1 && (rows[idx].name || rows[idx].alias)) {
      addRow();
    }
  };

  const aliasCount = (rows || []).filter((r) => r && (r.name || r.alias)).length;

  return (
    <div
      className="list-editor"
      style={{
        marginBottom: 16,
        paddingTop: 10,
        paddingBottom: collapsed ? 10 : 12,
        borderBottom: '1px solid var(--border)',
      }}
    >
      <div className="row gap-sm" style={{ justifyContent: 'space-between', marginBottom: collapsed ? 0 : 8, alignItems: 'center' }}>
        <button
          type="button"
          className="row gap-sm"
          onClick={() => setCollapsed((v) => !v)}
          aria-label={collapsed ? 'Expand channel' : 'Collapse channel'}
          aria-expanded={!collapsed}
          title={collapsed ? 'Expand' : 'Collapse'}
          style={{ background: 'none', border: 'none', padding: 0, cursor: 'pointer', alignItems: 'center', color: 'inherit' }}
        >
          <span style={{ display: 'inline-block', transition: 'transform 0.15s', transform: collapsed ? 'rotate(-90deg)' : 'rotate(0deg)', fontSize: 12 }} aria-hidden="true">▼</span>
          <span className="cell-stack">
            <span className="cell-stack__main" style={{ fontWeight: 600 }}>{label}</span>
            <span className="badge badge--muted" style={{ fontSize: 10 }}>{channel}</span>
          </span>
          <span className="dim" style={{ fontSize: 11 }}>
            {aliasCount === 0 ? 'no aliases' : `${aliasCount} alias${aliasCount === 1 ? '' : 'es'}`}
          </span>
        </button>
        <div className="row gap-sm">
          <button onClick={onSave} disabled={saving}>Save</button>
          {confirmRemove ? (
            <>
              <button onClick={() => setConfirmRemove(false)}>Cancel</button>
              <button className="danger" onClick={onRemove} disabled={saving}>Confirm delete</button>
            </>
          ) : (
            <button onClick={() => setConfirmRemove(true)} disabled={saving}>Remove channel</button>
          )}
        </div>
      </div>

      {!collapsed && (
        <>
          {modelsError && (
            <div className="dim" style={{ fontSize: 11, marginBottom: 6 }}>
              Default models unavailable: {modelsError}. You can still type a model id manually.
            </div>
          )}
          {modelsLoading && (
            <div className="dim" style={{ fontSize: 11, marginBottom: 6 }}>Loading default models…</div>
          )}

          {(rows || []).length === 0 && (
            <div className="list-editor__empty">No aliases. Click “Add alias”.</div>
          )}
          {(rows || []).map((row, idx) => (
            <div key={idx} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
              <div className="list-editor__row">
                {/* Mixed input: free text + dropdown of the channel's default models. */}
                <input
                  list={`oauth_alias_models_${channel}`}
                  type="text"
                  value={row.name || ''}
                  onChange={(e) => update(idx, { name: e.target.value })}
                  onBlur={() => autoAddOnLastRow(idx)}
                  placeholder="upstream model (e.g. gpt-5.3-codex-spark)"
                  spellCheck={false}
                  aria-label="Upstream model"
                  style={{ flex: 1 }}
                />
                <datalist id={`oauth_alias_models_${channel}`}>
                  {modelOptions.map((m) => (
                    <option key={m.id} value={m.id}>{m.display !== m.id ? m.display : ''}</option>
                  ))}
                </datalist>
                <input
                  type="text"
                  value={row.alias || ''}
                  onChange={(e) => update(idx, { alias: e.target.value })}
                  onBlur={() => autoAddOnLastRow(idx)}
                  placeholder="client alias (e.g. gpt-5.5)"
                  spellCheck={false}
                  aria-label="Client alias"
                  style={{ flex: 1 }}
                />
                <button
                  type="button"
                  className="list-editor__remove"
                  onClick={() => removeRow(idx)}
                  aria-label="Remove alias"
                  title="Remove"
                >
                  ×
                </button>
              </div>
              <div className="list-editor__row" style={{ paddingLeft: 0 }}>
                <input
                  type="text"
                  value={row['display-name'] || ''}
                  onChange={(e) => update(idx, { 'display-name': e.target.value })}
                  placeholder="display name (optional)"
                  spellCheck={false}
                  aria-label="Display name"
                  style={{ flex: 1 }}
                />
                <label className="toggle-row" style={{ flex: '0 0 auto', padding: '4px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
                  <span className="toggle-row__label" style={{ fontSize: 11 }}>fork</span>
                  <span className="toggle-switch">
                    <input
                      type="checkbox"
                      checked={!!row['fork']}
                      onChange={(e) => update(idx, { 'fork': e.target.checked })}
                      aria-label="Fork alias"
                    />
                    <span className="toggle-switch__slider" />
                  </span>
                </label>
                <label className="toggle-row" style={{ flex: '0 0 auto', padding: '4px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
                  <span className="toggle-row__label" style={{ fontSize: 11 }}>force-mapping</span>
                  <span className="toggle-switch">
                    <input
                      type="checkbox"
                      checked={!!row['force-mapping']}
                      onChange={(e) => update(idx, { 'force-mapping': e.target.checked })}
                      aria-label="Force mapping"
                    />
                    <span className="toggle-switch__slider" />
                  </span>
                </label>
              </div>
            </div>
          ))}
          <button type="button" className="list-editor__add" onClick={addRow}>+ Add alias</button>
        </>
      )}
    </div>
  );
}

// ============================================================================
// Schema definitions (declarative, per provider_type)
// ============================================================================

function buildSchemas() {
  const commonEndpoint = [
    { name: 'base_url', label: 'Base URL', type: 'text', placeholder: 'https://api.example.com',
      hint: 'Upstream API base URL. Leave blank to use the provider default.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
    { name: 'proxy_url', label: 'Proxy URL', type: 'text', placeholder: 'socks5://user:pass@host:1080',
      hint: 'Per-key proxy override. Use "direct"/"none" to bypass global proxy.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
    { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
      hint: 'Optional namespace prepended to every model this entry serves.' },
  ];

  const commonRouting = [
    { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
      hint: 'Higher value is preferred when multiple credentials match.' },
    { name: 'models', label: 'Models', type: 'models',
      hint: 'Map client-facing aliases to upstream model names.' },
    { name: 'excluded_models', label: 'Excluded models', type: 'chips',
      placeholder: 'model-id or wildcard', hint: 'Models that should never route through this entry. Supports * wildcards.' },
  ];

  const commonBehavior = [
    { name: 'headers', label: 'Custom headers', type: 'headers',
      hint: 'Extra HTTP headers attached to every request using this entry.' },
    { name: 'disabled', label: 'Disabled', type: 'toggle',
      hint: 'When on, this entry is excluded from routing without removing it.' },
    { name: 'disable_cooling', label: 'Disable cooldown', type: 'toggle',
      hint: 'Skip the cooldown schedule when this credential hits an error.' },
  ];

  // API-key provider schemas share most structure. The optional
  // `extraIdentity` fields are prepended to the Identity section (used to add
  // the per-type Identifier field, e.g. for Claude).
  const apiKeyBase = (extraBehavior = [], extraSections = [], extraIdentity = []) => ({
    sections: [
      { title: 'Identity', fields: [
        ...extraIdentity,
        { name: 'api_key', label: 'API key', type: 'password', placeholder: 'sk-…', required: true,
          hint: 'The credential that authenticates requests to this provider.' },
      ]},
      { title: 'Endpoint', fields: commonEndpoint },
      { title: 'Routing', fields: commonRouting, fetchModels: true },
      { title: 'Behavior', fields: [...commonBehavior, ...extraBehavior] },
      ...extraSections,
    ],
  });

  // Identifier field for API-key upstreams (all keyed types). Maps to the
  // generic `name` column, which the dashboard already lists as the
  // first-choice identifier in the provider list; the proxy lower-cases it
  // into the executor/routing provider key (see util.UpstreamProviderKey).
  // Optional — leave blank to keep the auto-derived key (empty name →
  // path-based fallback).
  const identifierField = {
    name: 'name', label: 'Identifier', type: 'text', placeholder: 'team-a-gemini',
    hint: 'Optional stable identifier for this upstream. Lower-cased to form its routing key; shown in the provider list.',
  };

  const claudeCloakSection = {
    title: 'Cloak',
    hint: 'Disguise API requests to appear as the official Claude Code CLI (Claude only).',
    fields: [
      { name: 'cloak_mode', label: 'Cloak mode', type: 'select',
        options: [
          { value: '', label: 'auto (default)' },
          { value: 'always', label: 'always' },
          { value: 'never', label: 'never' },
        ],
        hint: 'auto: cloak only for non-Claude-Code clients. always: always cloak. never: never cloak.' },
      { name: 'cloak_strict_mode', label: 'Strict mode', type: 'toggle',
        hint: 'Strip all user system messages; keep only the Claude Code prompt.' },
      { name: 'cloak_sensitive_words', label: 'Sensitive words', type: 'chips',
        placeholder: 'word to obfuscate', hint: 'Words obfuscated with zero-width characters to bypass content filters.' },
      { name: 'cloak_cache_user_id', label: 'Cache user ID', type: 'toggle',
        hint: 'Cache Claude user_id per API key. Off = fresh random per request.' },
    ],
  };

  const schemaMap = {
    'gemini-api-key': apiKeyBase([], [], [identifierField]),
    'interactions-api-key': apiKeyBase([], [], [identifierField]),
    'codex-api-key': apiKeyBase([
      { name: 'websockets', label: 'WebSockets', type: 'toggle',
        hint: 'Use the Responses API websocket transport for this entry.' },
    ], [], [identifierField]),
    'xai-api-key': apiKeyBase([
      { name: 'websockets', label: 'WebSockets', type: 'toggle',
        hint: 'Use the Responses API websocket transport for this entry.' },
    ], [], [identifierField]),
    'claude-api-key': apiKeyBase(
      [
        { name: 'rebuild_mid_system_message', label: 'Rebuild mid system message', type: 'toggle',
          hint: 'Move role=system messages into the top-level system field.' },
        { name: 'experimental_cch_signing', label: 'Experimental CCH signing', type: 'toggle',
          hint: 'Opt-in final-body cch signing for cloaked Claude /v1/messages requests.' },
      ],
      [claudeCloakSection],
      [identifierField],
    ),
    'vertex-api-key': apiKeyBase([], [], [identifierField]),
    'openai-compatibility': {
      sections: [
        { title: 'Identity', fields: [
          { name: 'name', label: 'Provider name', type: 'text', placeholder: 'openrouter', required: true,
            hint: 'Unique identifier for this OpenAI-compatible provider.' },
          { name: 'base_url', label: 'Base URL', type: 'text', placeholder: 'https://openrouter.ai/api/v1',
            required: true, hint: 'The external OpenAI-compatible API endpoint. Include /v1 if your upstream requires it; otherwise the server auto-inserts /v1 before /chat/completions and /images/...',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, or socks5://.' : '') },
        ]},
        { title: 'Endpoint', fields: [
          { name: 'proxy_url', label: 'Proxy URL', type: 'text', placeholder: 'direct',
            hint: 'Per-provider proxy override.',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
          { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
            hint: 'Optional namespace prepended to every model this provider serves.' },
        ]},
        { title: 'Routing', fields: [
          { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
            hint: 'Higher value is preferred when multiple providers match.' },
          { name: 'models', label: 'Models', type: 'openai_models',
            hint: 'Map client-facing aliases to upstream model names. Supports image + modality flags.' },
          { name: 'excluded_models', label: 'Excluded models', type: 'chips',
            placeholder: 'model-id or wildcard', hint: 'Models that should never route through this provider.' },
        ], fetchModels: true },
        { title: 'Behavior', fields: [
          { name: 'headers', label: 'Custom headers', type: 'headers',
            hint: 'Extra HTTP headers attached to every request to this provider.' },
          { name: 'disabled', label: 'Disabled', type: 'toggle',
            hint: 'When on, this provider is excluded from routing.' },
          { name: 'disable_cooling', label: 'Disable cooldown', type: 'toggle',
            hint: 'Skip the cooldown schedule when this provider hits an error.' },
          { name: 'api_key_entries', label: 'API key entries', type: 'api_key_entries',
            hint: 'Multiple keys form a round-robin pool for this provider.' },
        ]},
      ],
    },
  };

  // OAuth provider schemas — share a common identity/token/behavior shape,
  // with cloak + identifier added for oauth:claude.
  for (const oauthType of OAUTH_TYPES.map((t) => t.value)) {
    // For oauth:claude an Identifier is also surfaced — it populates the
    // generic `name` column shown in the provider list (the executor key for
    // oauth:* is fixed to the channel, so it stays "claude" regardless).
    const identityFields = oauthType === 'oauth:claude' ? [identifierField] : [];
    const sections = [
      { title: 'Identity', fields: [
        ...identityFields,
        { name: 'email', label: 'Account email', type: 'text', placeholder: 'user@example.com',
          hint: 'The OAuth account email (read from the auth file).' },
        { name: 'file_name', label: 'File name', type: 'text', placeholder: 'claude-account-1', required: !false,
          hint: 'Auth JSON file id (without .json). Used to match the auth-dir file.' },
        { name: 'label', label: 'Label', type: 'text', placeholder: 'Work account',
          hint: 'Optional human-readable label.' },
      ]},
      { title: 'OAuth token', hint: 'These fields are normally managed automatically by the token refresh pipeline. Edit only when manually seeding or correcting a token.',
        fields: [
          { name: 'token_access_token', label: 'Access token', type: 'password',
            hint: 'Current OAuth access token. Refreshed automatically.' },
          { name: 'token_refresh_token', label: 'Refresh token', type: 'password',
            hint: 'OAuth refresh token (used to obtain new access tokens).' },
          { name: 'token_expiry', label: 'Token expiry', type: 'text', placeholder: '2026-08-01T12:00:00Z',
            hint: 'RFC3339 timestamp. Leave blank if unknown.' },
          { name: 'token_expired', label: 'Token expired', type: 'toggle',
            hint: 'Mark the token as expired to force a refresh on next use.' },
        ]},
      { title: 'Routing', fields: [
        { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
          hint: 'Higher value is preferred when multiple credentials match.' },
        { name: 'models', label: 'Model aliases', type: 'models',
          hint: 'Map client-facing aliases to upstream model names. These are written to the auth file as per-account model_aliases.' },
        { name: 'excluded_models', label: 'Excluded models', type: 'chips',
          placeholder: 'model-id or wildcard', hint: 'Models never routed through this auth. Supports * wildcards.' },
      ]},
      { title: 'Behavior', fields: [
        { name: 'disabled', label: 'Disabled', type: 'toggle',
          hint: 'When on, this auth is excluded from routing.' },
        { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
          hint: 'Optional namespace prepended to every model this auth serves.' },
      ]},
    ];
    if (oauthType === 'oauth:claude') sections.push(claudeCloakSection);
    schemaMap[oauthType] = { sections };
  }

  return schemaMap;
}

// ============================================================================
// Editor modal (schema-driven)
// ============================================================================

function UpstreamProviderEditor({ provider, siblingNames = [], onClose, onSaved }) {
  const toast = useToast();
  const schemas = useMemo(() => buildSchemas(), []);
  const isEdit = !!(provider && provider.id);

  // provider_type drives the schema. On create, the operator picks it first.
  const [providerType, setProviderType] = useState(() => provider?.provider_type || '');
  const schema = schemas[providerType] || { sections: [] };

  // OAuth connect state: for oauth:* types that have a web auth-url endpoint,
  // the operator must complete the OAuth flow (generate URL → browser login →
  // paste callback URL) before the token fields are meaningful. We track
  // whether that flow has completed in this editor session. In edit mode the
  // provider is already connected, so we start connected=true (skip the
  // connect step, reveal the token section immediately).
  const oauthChannel = isOAuth(providerType) ? providerType.replace(/^oauth:/, '') : '';
  const oauthConnectable = !!oauthChannelToAuthProvider(oauthChannel);
  const [oauthConnected, setOauthConnected] = useState(isEdit && oauthConnectable);

  const [form, setForm] = useState(() => buildForm(providerType, provider));
  const [touched, setTouched] = useState({});
  const [saving, setSaving] = useState(false);
  const [serverError, setServerError] = useState('');
  const [initialSnapshot] = useState(() => JSON.stringify(form));
  const prevTypeRef = useRef(providerType);

  // When the provider_type changes (create mode), re-build the form to match
  // the new schema while preserving the provider_type itself + sensible
  // carry-overs (priority, prefix).
  useEffect(() => {
    if (prevTypeRef.current === providerType) return;
    prevTypeRef.current = providerType;
    setForm(buildForm(providerType, provider, form));
    setTouched({});
    setServerError('');
    setOauthConnected(false);
  }, [providerType]); // eslint-disable-line react-hooks/exhaustive-deps

  const errors = useMemo(
    () => validate(form, schema, providerType, siblingNames, isEdit),
    [form, schema, providerType, siblingNames, isEdit],
  );
  const hasErrors = Object.keys(errors).length > 0;
  const dirty = JSON.stringify(form) !== initialSnapshot;

  function update(name, value) {
    setForm((f) => ({ ...f, [name]: value }));
    setTouched((t) => ({ ...t, [name]: true }));
    setServerError('');
  }

  async function handleSubmit(e) {
    e?.preventDefault?.();
    if (hasErrors) {
      setTouched(Object.fromEntries(Object.keys(errors).map((k) => [k, true])));
      return;
    }
    setSaving(true);
    setServerError('');
    try {
      const payload = buildPayload(form, providerType);
      if (isEdit) {
        await updateUpstreamProvider(provider.id, payload);
        toast.success('Provider updated');
      } else {
        await createUpstreamProvider(payload);
        toast.success('Provider created');
      }
      onSaved();
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err.message || 'Save failed');
      setServerError(msg);
      toast.error(msg);
    } finally {
      setSaving(false);
    }
  }

  function attemptClose() {
    if (dirty && !saving) {
      const ok = window.confirm('Discard unsaved changes?');
      if (!ok) return;
    }
    onClose();
  }

  const title = isEdit
    ? `Edit ${TYPE_LABEL[providerType] || providerType}`
    : 'New Upstream Provider';

  // Summary banner content for the editor header. Operators editing OAuth
  // accounts care about file_name (auth-dir collision key) and the account
  // email; for API-key providers it's the api_key entries count.
  const summary = isEdit
    ? {
        identifier: provider?.file_name || provider?.name || provider?.label || '',
        secondary: provider?.email || provider?.base_url || '',
      }
    : null;

  return (
    <Modal
      title={title}
      size="xl"
      onClose={attemptClose}
      footer={<>
        <button type="button" onClick={attemptClose} disabled={saving}>Cancel</button>
        <button type="button" className="primary" onClick={handleSubmit}
          disabled={saving || (hasErrors && Object.values(touched).some(Boolean))}>
          {saving ? 'Saving…' : isEdit ? 'Save changes' : 'Create provider'}
        </button>
      </>}
    >
      <div>
        {serverError && <div className="error-banner">{serverError}</div>}
        {hasErrors && (
          <div className="error-summary">
            <strong>Please fix {Object.keys(errors).length} field{Object.keys(errors).length === 1 ? '' : 's'}:</strong>
            <ul>{Object.entries(errors).map(([k, v]) => <li key={k}>{v}</li>)}</ul>
          </div>
        )}

        {/* Editor summary banner. Surfaces the provider type + identifier
            (file_name for OAuth, name for OpenAI-compat, base_url for API-key)
            and a one-line "save will re-render" hint so the operator never
            has to remember what an upstream provider row actually controls. */}
        {providerType && (
          <div className="editor-summary" role="region" aria-label="Editor summary">
            <div className="editor-summary__head">
              <span className="badge badge--muted" style={{ fontSize: 10 }}>{providerType}</span>
              {summary?.identifier && (
                <code className="editor-summary__id">{summary.identifier}</code>
              )}
              {dirty && (
                <span className="editor-summary__dirty" title="You have unsaved changes">● unsaved changes</span>
              )}
            </div>
            <div className="editor-summary__hint">
              {isOAuth(providerType) && (
                <>Saves write a row to <code>upstream_providers</code> + a normalized JSON file to the auth-dir.</>
              )}
              {isOpenAI(providerType) && (
                <>Saves write a row to <code>upstream_providers</code>; base URL becomes the routing key.</>
              )}
              {!isOAuth(providerType) && !isOpenAI(providerType) && (
                <>Saves write a row to <code>upstream_providers</code> + re-render the matching <code>config.yaml</code> provider block.</>
              )}
              {' '}Reload is triggered automatically.
            </div>
          </div>
        )}

        {/* Provider type selector — always rendered first, disabled in edit. */}
        <div className="form-section">
          <div className="form-section__title">Provider Type</div>
          <div className="form-section__row">
            <Field label="Type" required>
              <select value={providerType} onChange={(e) => setProviderType(e.target.value)} disabled={isEdit}>
                <option value="">Select a provider type…</option>
                <optgroup label="API Key">{API_KEY_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
                <optgroup label="OAuth / File-backed">{OAUTH_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
              </select>
            </Field>
          </div>
        </div>

        {/* OAuth connect workflow — for oauth:* types that support a web
            auth-url flow, show the Connect step first (create mode only) so
            the operator can generate the authorize URL, complete the browser
            login, then paste the callback redirect URL to complete the token
            exchange. In edit mode the provider is already connected, so the
            flow is skipped entirely. After a successful callback, the
            identity/token/models fields are auto-populated from the new auth
            file before the full settings are revealed. */}
        {oauthConnectable && !isEdit && (
          <OAuthConnectSection
            providerType={providerType}
            onCompleted={(authData) => {
              setOauthConnected(true);
              // Auto-populate Identity + token + cloak + models from the auth
              // file the server just created during the OAuth flow. All
              // available fields from the auth JSON are merged so the
              // upstream_providers row persists the complete picture.
              if (authData) {
                setForm((f) => {
                  const next = {
                    ...f,
                    file_name: authData.file_name || f.file_name,
                    email: authData.email || f.email,
                    label: authData.label || f.label,
                  };
                  // Token fields.
                  if (authData.token_access_token) next.token_access_token = authData.token_access_token;
                  if (authData.token_refresh_token) next.token_refresh_token = authData.token_refresh_token;
                  if (authData.token_token_type) next.token_token_type = authData.token_token_type;
                  if (authData.token_scope) next.token_scope = authData.token_scope;
                  if (authData.token_expiry) next.token_expiry = formatRFC3339(authData.token_expiry);
                  if (authData.token_expired) next.token_expired = true;
                  // Cloak fields (Claude OAuth).
                  if (authData.cloak_mode != null) next.cloak_mode = authData.cloak_mode;
                  if (authData.cloak_strict_mode) next.cloak_strict_mode = true;
                  if (Array.isArray(authData.cloak_sensitive_words)) {
                    next.cloak_sensitive_words = authData.cloak_sensitive_words;
                  }
                  if (authData.cloak_cache_user_id === true || authData.cloak_cache_user_id === false) {
                    next.cloak_cache_user_id = authData.cloak_cache_user_id;
                  }
                  // Extra config passthrough.
                  const extra = { ...(f.extra_config || {}) };
                  if (authData.disable_cooling) extra.disable_cooling = true;
                  if (authData.request_retry != null) extra.request_retry = authData.request_retry;
                  if (authData.tool_prefix_disabled) extra.tool_prefix_disabled = true;
                  if (Object.keys(extra).length > 0) next.extra_config = extra;
                  // Prefix.
                  if (authData.prefix) next.prefix = authData.prefix;
                  // Models.
                  if (authData.models && authData.models.length > 0) {
                    next.models = authData.models;
                  }
                  return next;
                });
              }
            }}
          />
        )}

        {/* Schema-driven sections. */}
        {schema.sections.map((section) => {
          // For OAuth providers, hide the OAuth token section until the
          // connect flow is done (those fields are populated by the callback).
          if (oauthConnectable && !oauthConnected && section.title === 'OAuth token') {
            return null;
          }
          return (
          <div className="form-section" key={section.title}>
            <div className="form-section__title">{section.title}</div>
            {section.hint && <div className="form-section__hint">{section.hint}</div>}
            <div className="form-section__row">
              {section.fields.map((field) => {
                const showError = touched[field.name] && errors[field.name];
                return (
                  <Field
                    key={field.name}
                    label={field.label}
                    hint={field.hint}
                    error={showError ? errors[field.name] : ''}
                    required={field.required}
                    htmlFor={`up_${field.name.replace(/[.\s]/g, '_')}`}
                  >
                    {renderInput(field, form, update, isEdit, providerType)}
                  </Field>
                );
              })}
            </div>

            {/* Inline Fetch Models — rendered at the bottom of sections that
                declare fetchModels:true, for fetchable provider types. */}
            {section.fetchModels && FETCHABLE_TYPES.has(providerType) && (
              <FetchModelsInline
                form={form}
                provider={TYPE_SIMPLE[providerType] || providerType}
                isEdit={isEdit}
                siblingNames={siblingNames}
                onAddModels={(picked) => {
                  const existing = Array.isArray(form.models) ? form.models : [];
                  const byName = new Set(existing.map((r) => (r?.name || r?.id || '').trim()).filter(Boolean));
                  const additions = picked
                    .filter((p) => p && (p.id || p.name) && !byName.has(p.id || p.name))
                    .map((p) => {
                      const m = { name: p.id || p.name };
                      // Use camelCase keys for the upstream_providers API.
                      if (p.display_name) m.display_name = p.display_name;
                      return m;
                    });
                  if (additions.length > 0) {
                    // Merge into existing model rows (which use camelCase).
                    const merged = [...existing];
                    for (const a of additions) merged.push(a);
                    update('models', merged);
                  }
                }}
              />
            )}
          </div>
          );
        })}

        {!providerType && (
          <EmptyState title="Select a provider type" hint="Choose a type above to see its configuration fields." />
        )}
      </div>
    </Modal>
  );
}

// ============================================================================
// Input renderer (dispatches by field.type)
// ============================================================================

function renderInput(field, form, update, isEdit, providerType) {
  const id = `up_${field.name.replace(/[.\s]/g, '_')}`;
  const value = form[field.name];
  switch (field.type) {
    case 'password':
      return <PasswordInput id={id} value={value} onChange={(v) => update(field.name, v)}
        placeholder={field.placeholder} defaultShown={isEdit} />;
    case 'number':
      return <input id={id} type="number" min={field.min} value={value ?? ''}
        onChange={(e) => update(field.name, e.target.value)} placeholder={field.placeholder} />;
    case 'select':
      return <select id={id} value={value || ''} onChange={(e) => update(field.name, e.target.value)}>
        {field.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>;
    case 'toggle':
      return <ToggleRow label={field.label} hint={field.hint} checked={!!value}
        onChange={(v) => update(field.name, v)} />;
    case 'chips':
      return <ChipListEditor values={value || []} onChange={(v) => update(field.name, v)}
        placeholder={field.placeholder} emptyHint={field.emptyHint} />;
    case 'headers':
      return <KeyValueEditor rows={value || []} onChange={(v) => update(field.name, v)} />;
    case 'models':
      return <ModelListEditor rows={value || []} onChange={(v) => update(field.name, v)}
        fieldHints={{ name: 'upstream name', alias: 'client alias', displayName: 'display name' }} />;
    case 'openai_models':
      return <ModelListEditor rows={value || []} onChange={(v) => update(field.name, v)}
        fieldHints={{
          name: 'upstream model (e.g. anthropic/claude-3-5-sonnet)',
          alias: 'client alias (e.g. claude-sonnet)',
          displayName: 'display name (optional)',
        }} />;
    case 'api_key_entries':
      return <APIKeyEntriesEditor
        entries={value || []}
        onChange={(v) => update(field.name, v)}
      />;
    case 'text':
    default:
      return <input id={id} type="text" value={value || ''} onChange={(e) => update(field.name, e.target.value)}
        placeholder={field.placeholder} required={field.required} />;
  }
}

// ============================================================================
// OAuthConnectSection — the connect workflow step for oauth:* providers
// ============================================================================

// OAuthConnectSection renders the OAuth authorization workflow:
//   1. Generate authorize URL (calls /<provider>-auth-url)
//   2. Auto-open the URL in a new tab (+ copy button fallback)
//   3. Operator completes login in their browser
//   4. Operator pastes the callback redirect URL (or raw code+state)
//   5. Submit calls /oauth-callback to complete the token exchange
//
// After a successful callback, onCompleted() is called so the parent can
// reveal the full identity/token settings. This section is shown ABOVE the
// schema-driven sections for oauth:* types, so the operator follows the
// connect flow first before configuring further details.
function OAuthConnectSection({ providerType, onCompleted }) {
  const toast = useToast();
  const channel = providerType.replace(/^oauth:/, '');
  const authProvider = oauthChannelToAuthProvider(channel);

  const [stage, setStage] = useState('idle'); // idle | loading | ready | submitting | done | error
  const [error, setError] = useState('');
  const [authUrl, setAuthUrl] = useState('');
  const [oauthState, setOauthState] = useState('');
  const [flow, setFlow] = useState('web');
  const [userCode, setUserCode] = useState('');
  const [callbackUrl, setCallbackUrl] = useState('');
  const [copied, setCopied] = useState(false);

  const handleGenerate = () => {
    setStage('loading');
    setError('');
    setAuthUrl('');
    setCallbackUrl('');
    requestOAuthUrl(authProvider, { isWebUI: true })
      .then((res) => {
        setStage('ready');
        setAuthUrl(res?.url || '');
        setOauthState(res?.state || '');
        setFlow(res?.flow || 'web');
        setUserCode(res?.user_code || '');
        // Auto-open the authorize URL in a new tab so the operator can start
        // the browser login immediately.
        if (res?.url) {
          window.open(res.url, '_blank', 'noopener,noreferrer');
        }
      })
      .catch((err) => {
        setStage('error');
        setError(err.message || 'Failed to generate authorize URL');
      });
  };

  const handleSubmitCallback = () => {
    const trimmed = callbackUrl.trim();
    if (!trimmed) {
      toast.error('Paste the callback redirect URL first.');
      return;
    }
    setStage('submitting');
    setError('');
    submitOAuthCallback({ provider: authProvider, redirectUrl: trimmed })
      .then(() => {
        setStage('fetching');
        toast.info('OAuth callback accepted. Fetching credential details…');
        // The server persists the auth file asynchronously + a background
        // waiter exchanges the code for tokens. Poll listAuthFiles until the
        // new auth file for this provider channel appears, then extract the
        // identity (email/file_name/label) and discover its models.
        pollForAuthFile(0)
          .then((authData) => {
            setStage('done');
            toast.success('OAuth flow completed. Provider details populated.');
            onCompleted?.(authData);
          })
          .catch((err) => {
            // Even if polling fails/times out, mark as done so the operator
            // can proceed manually — the credential may still be settling.
            setStage('done');
            toast.info(err.message || 'Could not auto-fetch details; fill them manually.');
            onCompleted?.(null);
          });
      })
      .catch((err) => {
        setStage('error');
        setError(err.message || 'Callback submission failed');
        toast.error(err.message || 'Callback submission failed');
      });
  };

  // pollForAuthFile polls listAuthFiles up to maxAttempts (every 1.5s) until
  // it finds a non-disabled auth file whose type matches the OAuth channel.
  // Once found, it fetches the file's models via getAuthFileModels and
  // returns {file_name, email, label, models}.
  const pollForAuthFile = (attempt) => {
    const maxAttempts = 8;
    const delayMs = 1500;
    return new Promise((resolve, reject) => {
      const tryFetch = (n) => {
        listAuthFiles()
          .then((res) => {
            const files = res?.files || [];
            // Match by provider type (channel). The auth-file "type" field
            // from the server is the channel: claude, codex, xai, kimi,
            // antigravity. Skip disabled entries.
            const match = files.find((f) => {
              const ft = String(f?.type || f?.provider || '').toLowerCase();
              return ft === channel && !f?.disabled && !f?.unavailable;
            });
            if (!match) {
              if (n >= maxAttempts) {
                reject(new Error('Timed out waiting for the auth file to appear.'));
                return;
              }
              setTimeout(() => tryFetch(n + 1), delayMs);
              return;
            }
            // Auth file found — fetch the raw JSON (for tokens + all fields),
            // then fetch its models, then resolve with everything.
            const fileName = match.name || match.id || '';
            const email = match.email || '';
            const label = match.label || '';
            Promise.all([
              fetchAuthFileJSON(fileName).catch(() => null),
              getAuthFileModels(fileName).catch(() => null),
            ]).then(([rawJson, mres]) => {
              const modelList = (mres?.models || []).map((m) => {
                const model = { name: m.id || m.name || '' };
                if (m.display_name) model.display_name = m.display_name;
                return model;
              }).filter((m) => m.name);

              // Extract all available fields from the raw auth JSON so they
              // can be persisted into the upstream_providers row.
              const authData = {
                file_name: fileName,
                email: email || strVal(rawJson, 'email'),
                label,
                models: modelList,
              };
              if (rawJson) {
                // Token fields (nested "token" object OR flat top-level).
                const tokenObj = rawJson.token || {};
                authData.token_access_token =
                  strVal(rawJson, 'access_token') || strVal(tokenObj, 'access_token');
                authData.token_refresh_token =
                  strVal(rawJson, 'refresh_token') || strVal(tokenObj, 'refresh_token');
                authData.token_token_type =
                  strVal(rawJson, 'token_type') || strVal(tokenObj, 'token_type');
                authData.token_scope =
                  strVal(rawJson, 'scope') || strVal(tokenObj, 'scope');
                authData.token_expiry =
                  strVal(rawJson, 'expires_at') || strVal(rawJson, 'expire') ||
                  strVal(rawJson, 'expired') || strVal(tokenObj, 'expiry');
                if (rawJson.expired === true) authData.token_expired = true;
                // Cloak fields (Claude OAuth).
                authData.cloak_mode = strVal(rawJson, 'cloak_mode');
                if (rawJson.cloak_strict_mode === true) authData.cloak_strict_mode = true;
                if (Array.isArray(rawJson.cloak_sensitive_words)) {
                  authData.cloak_sensitive_words = rawJson.cloak_sensitive_words;
                }
                if (rawJson.cloak_cache_user_id === true || rawJson.cloak_cache_user_id === false) {
                  authData.cloak_cache_user_id = rawJson.cloak_cache_user_id;
                }
                // Pass-through extras.
                if (rawJson.disable_cooling === true) authData.disable_cooling = true;
                if (rawJson.request_retry != null) authData.request_retry = rawJson.request_retry;
                if (rawJson.tool_prefix_disabled === true) authData.tool_prefix_disabled = true;
                if (rawJson.prefix) authData.prefix = rawJson.prefix;
              }
              resolve(authData);
            });
          })
          .catch(() => {
            if (n >= maxAttempts) {
              reject(new Error('Failed to fetch auth files.'));
              return;
            }
            setTimeout(() => tryFetch(n + 1), delayMs);
          });
      };
      tryFetch(attempt);
    });
  };

  const handleCopy = () => {
    if (!authUrl) return;
    navigator.clipboard?.writeText(authUrl).then(() => {
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    });
  };

  return (
    <div className="form-section">
      <div className="form-section__title">Connect {TYPE_LABEL[providerType] || providerType}</div>
      <div className="form-section__hint">
        Start the OAuth login flow in your browser, then paste the callback URL
        you land on after authorizing. This exchanges the code for tokens so the
        provider is ready to route requests.
      </div>

      {stage === 'idle' && (
        <div className="form__row">
          <button type="button" className="primary" onClick={handleGenerate}>
            Generate authorize URL
          </button>
        </div>
      )}

      {stage === 'loading' && <Spinner label="Requesting authorize URL…" />}

      {stage === 'fetching' && <Spinner label="Fetching credential details from server…" />}

      {stage === 'error' && (
        <>
          <div className="error-banner">{error}</div>
          <button type="button" onClick={handleGenerate}>Try again</button>
        </>
      )}

      {(stage === 'ready' || stage === 'submitting' || stage === 'done') && (
        <>
          {stage === 'done' ? (
            <div className="success-banner">
              ✓ OAuth flow completed. Save the provider to persist the credential.
            </div>
          ) : (
            <div className="form__row">
              <label className="form__label">Authorize URL</label>
              <div className="copyable" style={{ wordBreak: 'break-all', fontSize: 12 }}>{authUrl}</div>
              <div className="row gap-sm" style={{ marginTop: 6 }}>
                <button type="button" onClick={() => window.open(authUrl, '_blank', 'noopener,noreferrer')}>
                  Re-open in new tab
                </button>
                <button type="button" onClick={handleCopy}>{copied ? '✓ Copied' : 'Copy URL'}</button>
              </div>
              {flow === 'device' && userCode && (
                <div className="form__hint" style={{ marginTop: 8 }}>
                  Device code: <strong>{userCode}</strong>
                </div>
              )}
              <div className="form__hint" style={{ marginTop: 8 }}>
                Complete the login in the browser tab. After authorizing, you will
                be redirected to a callback URL — copy it from the browser address
                bar and paste it below.
              </div>
            </div>
          )}

          {stage !== 'done' && (
            <div className="form__row">
              <label className="form__label">Callback URL</label>
              <input
                type="text"
                value={callbackUrl}
                onChange={(e) => setCallbackUrl(e.target.value)}
                placeholder="https://your-server/anthropic/callback?code=…&state=…"
                spellCheck={false}
              />
              <div className="form__hint">
                Paste the full redirect URL from the browser address bar after
                completing the login. The server extracts the code and state
                automatically.
              </div>
              <div className="row gap-sm" style={{ marginTop: 8 }}>
                <button
                  type="button"
                  className="primary"
                  onClick={handleSubmitCallback}
                  disabled={stage === 'submitting' || !callbackUrl.trim()}
                >
                  {stage === 'submitting' ? 'Submitting…' : 'Complete OAuth'}
                </button>
              </div>
            </div>
          )}
        </>
      )}
    </div>
  );
}

// ============================================================================
// ImportOAuthProviderModal — create an upstream provider from existing/pasted
// OAuth material (auth files already on disk, pasted token JSON, or uploaded
// JSON files). Complements OAuthConnectSection (which performs a live browser
// login). All three tabs share extractAuthFields() + buildOAuthCreatePayload()
// below so token/cloak/model extraction is consistent with the connect flow.
// ============================================================================

// Map an auth-file's `type`/`provider` string (lowercased) to the upstream
// provider_type prefix `oauth:<channel>`. Returns '' when no match — the
// caller surfaces a "use New Provider instead" message.
const AUTH_TYPE_TO_OAUTH_CHANNEL = {
  claude: 'claude',
  anthropic: 'claude',
  codex: 'codex',
  'openai-codex': 'codex',
  kimi: 'kimi',
  moonshot: 'kimi',
  xai: 'xai',
  grok: 'xai',
  vertex: 'vertex',
  aistudio: 'aistudio',
  'google-aistudio': 'aistudio',
  'ai-studio': 'aistudio',
  antigravity: 'antigravity',
};

// Returns true when the parsed JSON looks like a Google service-account file
// (which the existing /vertex/import endpoint handles — not this modal).
function isServiceAccountJson(obj) {
  return obj && (
    obj.type === 'service_account' ||
    typeof obj.private_key === 'string' ||
    typeof obj.private_key_id === 'string'
  );
}

// extractAuthFields maps a parsed auth-dir JSON object to the field set the
// upstream_providers POST endpoint expects for an `oauth:<channel>` row.
// Mirrors the merging logic in OAuthConnectSection.pollForAuthFile
// (lines ~1308-1343) and the onCompleted merger (~1017-1055) so a row created
// via Import carries the same token/cloak/model metadata as one created via
// the live connect flow.
//
// Returns { provider_type, file_name, email, label, token_*, cloak_*, prefix,
// extra_config, models } or { error } when the channel cannot be detected or
// the JSON is a service-account file.
function extractAuthFields(json, opts = {}) {
  if (!json || typeof json !== 'object') {
    return { error: 'JSON is empty or not an object.' };
  }
  if (isServiceAccountJson(json)) {
    return {
      error: 'This looks like a Vertex service-account JSON. Use the Vertex import page instead.',
    };
  }
  const rawType = String(
    json.type || json.provider || json.channel || (json.token && json.token.type) || '',
  ).toLowerCase();
  const channel = AUTH_TYPE_TO_OAUTH_CHANNEL[rawType];
  if (!channel) {
    return {
      error: `Can't detect OAuth channel from JSON type "${rawType || '(empty)'}". Use "+ New Provider" instead.`,
    };
  }
  const providerType = `oauth:${channel}`;
  const tokenObj = json.token || {};
  const fileName = (opts.fileName || json.file_name || json.name || '').trim();

  const fields = {
    provider_type: providerType,
    file_name: fileName,
    email: strVal(json, 'email') || strVal(json, 'account'),
    label: strVal(json, 'label'),
    token_access_token: strVal(json, 'access_token') || strVal(tokenObj, 'access_token'),
    token_refresh_token: strVal(json, 'refresh_token') || strVal(tokenObj, 'refresh_token'),
    token_token_type: strVal(json, 'token_type') || strVal(tokenObj, 'token_type'),
    token_scope: strVal(json, 'scope') || strVal(tokenObj, 'scope'),
    token_expiry: strVal(json, 'expires_at') || strVal(json, 'expire') ||
      strVal(json, 'expired') || strVal(tokenObj, 'expiry'),
    token_expired: json.expired === true ? true : false,
    cloak_mode: strVal(json, 'cloak_mode'),
    cloak_strict_mode: json.cloak_strict_mode === true ? true : false,
    cloak_sensitive_words: Array.isArray(json.cloak_sensitive_words)
      ? json.cloak_sensitive_words : [],
    prefix: strVal(json, 'prefix'),
    extra_config: {},
    models: [],
  };
  if (json.cloak_cache_user_id === true || json.cloak_cache_user_id === false) {
    fields.cloak_cache_user_id = json.cloak_cache_user_id;
  }
  // Pass-through extras that upstream_providers persists into extra_config.
  const extra = {};
  if (json.disable_cooling === true) extra.disable_cooling = true;
  if (json.request_retry != null) extra.request_retry = json.request_retry;
  if (json.tool_prefix_disabled === true) extra.tool_prefix_disabled = true;
  fields.extra_config = extra;
  return fields;
}

// buildOAuthCreatePayload converts extracted fields into the JSON body shape
// expected by POST /upstream-providers (mirrors UpstreamProviderEditor's
// buildPayload for the `oauth` + `claude` branches).
function buildOAuthCreatePayload(fields) {
  const payload = {
    provider_type: fields.provider_type,
    priority: 0,
    disabled: false,
    prefix: (fields.prefix || '').trim(),
    base_url: '',
    proxy_url: 'none',
    headers: {},
    models: (fields.models || []).map((m) => ({
      name: m.name || m.id || '',
      alias: m.alias || '',
      display_name: m.display_name || m.displayName || '',
      force_mapping: !!m.force_mapping,
      fork: !!m.fork,
    })).filter((m) => m.name),
    excluded_models: [],
    extra_config: { ...(fields.extra_config || {}) },
  };
  payload.email = (fields.email || '').trim();
  payload.file_name = (fields.file_name || '').trim();
  payload.label = (fields.label || '').trim();
  payload.token_access_token = fields.token_access_token || '';
  payload.token_refresh_token = fields.token_refresh_token || '';
  payload.token_expired = !!fields.token_expired;
  if (fields.token_expiry) {
    const t = new Date(fields.token_expiry);
    if (!Number.isNaN(t.getTime())) {
      payload.token_expiry = t.toISOString();
    }
  }
  if (fields.provider_type === 'oauth:claude') {
    payload.cloak_mode = fields.cloak_mode || '';
    payload.cloak_strict_mode = !!fields.cloak_strict_mode;
    payload.cloak_sensitive_words = fields.cloak_sensitive_words || [];
    if (fields.cloak_cache_user_id === true || fields.cloak_cache_user_id === false) {
      payload.cloak_cache_user_id = fields.cloak_cache_user_id;
    }
  }
  return payload;
}

// deriveFileName synthesizes an auth-file base name from the extracted
// channel + email (or a timestamp) when the source JSON had none.
function deriveFileName(fields) {
  if (fields.file_name) return fields.file_name.replace(/\.json$/i, '');
  const ch = (fields.provider_type || '').replace(/^oauth:/, '') || 'oauth';
  const email = (fields.email || '').trim();
  if (email) {
    const slug = email.toLowerCase().replace(/[^a-z0-9._-]+/g, '-').replace(/^-+|-+$/g, '');
    if (slug) return `${ch}-${slug}`;
  }
  return `${ch}-${new Date().toISOString().replace(/[:.]/g, '-').slice(0, 19)}`;
}

// PreviewCard — compact, label/value summary for a parsed OAuth payload. Used
// by all three Import tabs so the operator always sees the same projection of
// what the upstream provider row will look like once created.
function PreviewCard({ fields, fileNameOverride, alreadyImported = false }) {
  if (!fields) return null;
  const fileName = (fileNameOverride || fields.file_name || '(derived)').replace(/\.json$/i, '');
  const channel = (fields.provider_type || '').replace(/^oauth:/, '') || '?';
  const expiry = fields.token_expiry
    ? (() => { try { return new Date(fields.token_expiry).toLocaleString(); } catch { return fields.token_expiry; } })()
    : '—';
  return (
    <div className="preview-card" role="region" aria-label="Import preview">
      <div className="preview-card__title">
        {alreadyImported
          ? <>Already imported: <code style={{ color: 'var(--accent)' }}>oauth:{channel}</code> → <code>{fileName}.json</code></>
          : <>Will create <code style={{ color: 'var(--accent)' }}>oauth:{channel}</code> → <code>{fileName}.json</code></>}
      </div>
      {alreadyImported && (
        <div className="preview-card__notice" role="status">
          An upstream provider row with this <code>(provider_type, file_name)</code> already exists.
          Edit it from the table instead of re-importing.
        </div>
      )}
      <dl style={{ margin: 0 }}>
        <div className="preview-card__row"><dt>Provider type</dt><dd>{fields.provider_type || '—'}</dd></div>
        <div className="preview-card__row"><dt>Account email</dt><dd>{fields.email || '—'}</dd></div>
        <div className="preview-card__row"><dt>Token expiry</dt><dd>{expiry}</dd></div>
        <div className="preview-card__row"><dt>Access token</dt><dd>{fields.token_access_token ? '✓ present' : '✗ missing'}</dd></div>
        <div className="preview-card__row"><dt>Refresh token</dt><dd>{fields.token_refresh_token ? '✓ present' : '✗ missing'}</dd></div>
        <div className="preview-card__row"><dt>Models</dt><dd>{(fields.models || []).length} mapped</dd></div>
        {channel === 'claude' && (
          <div className="preview-card__row"><dt>Cloak mode</dt><dd>{fields.cloak_mode || 'auto (default)'}</dd></div>
        )}
      </dl>
    </div>
  );
}

function ImportOAuthProviderModal({ existingProviderKeys = new Set(), onClose, onImported }) {
  const toast = useToast();
  const [tab, setTab] = useState('files'); // files | paste | upload
  const [creating, setCreating] = useState(false);

  // Tab 1: from existing auth files.
  const [authFiles, setAuthFiles] = useState([]);
  const [authFilesLoading, setAuthFilesLoading] = useState(false);
  const [authFilesError, setAuthFilesError] = useState('');
  const [selectedFile, setSelectedFile] = useState(null); // { name, type, email, ... } — drives the single-row preview
  const [preview, setPreview] = useState(null); // extractAuthFields output
  const [previewError, setPreviewError] = useState('');
  // Bulk-import selection (Tab 1). Independent of `selectedFile` so the
  // operator can both preview one row and queue several others. Keyed by
  // file name (not id) because auth-files aren't stored in our DB — the
  // server identifies them by their on-disk name.
  const [bulkSelectedNames, setBulkSelectedNames] = useState(() => new Set());
  const [bulkRunning, setBulkRunning] = useState(false);
  // Tab 1 filters — search by keyword + filter by OAuth channel. Both are
  // applied to `authFiles` before the eligible / bulk-selected derivations,
  // so the bulk-action bar only ever reflects what the operator can see.
  const [importSearch, setImportSearch] = useState('');
  const [importChannelFilter, setImportChannelFilter] = useState('');

  // Tab 2: paste JSON.
  const [rawJson, setRawJson] = useState('');
  const [fileNameInput, setFileNameInput] = useState('');
  const [pastePreview, setPastePreview] = useState(null);
  const [pasteError, setPasteError] = useState('');

  // Tab 3: upload files.
  const [uploadResult, setUploadResult] = useState(null); // per-file summary
  const [dropHover, setDropHover] = useState(false);
  const fileInputRef = useRef(null);

  const existingByFile = useMemo(() => {
    // existingProviderKeys holds "<provider_type>|<file_name>" strings. Surface
    // both a file-name-only set (for the table badge) and a paired set (for
    // the Create-button guard — a row imported as oauth:claude should not
    // block a different oauth:codex row with the same file_name).
    const names = new Set();
    const pairs = new Set();
    for (const k of existingProviderKeys) {
      const idx = k.indexOf('|');
      if (idx <= 0) continue;
      const pt = k.slice(0, idx);
      const fn = k.slice(idx + 1);
      names.add(fn);
      pairs.add(`${pt}|${fn}`);
    }
    return { names, pairs };
  }, [existingProviderKeys]);

  // True when the previewed auth file already has a matching upstream
  // provider row — drives both the Tab 1 badge state and the Create button's
  // disabled state so re-clicking Create can't 409.
  const previewAlreadyImported = !!(preview && preview.provider_type && preview.file_name &&
    existingByFile.pairs.has(`${preview.provider_type}|${preview.file_name}`));

  // Auth-files filtered by Tab 1's search + channel controls. All downstream
  // derivations (eligible sets, bulk-checkbox counts, preview/footer) read
  // from `filteredAuthFiles` instead of the raw `authFiles` so the toolbar
  // and table stay in lockstep — the operator never sees counts that
  // include rows they've already filtered out.
  const filteredAuthFiles = useMemo(() => {
    const q = importSearch.trim().toLowerCase();
    return authFiles.filter((f) => {
      const ch = String(f?.type || f?.provider || '').toLowerCase();
      if (importChannelFilter && ch !== importChannelFilter) return false;
      if (!q) return true;
      const name = (f.name || f.id || '').toLowerCase();
      const email = (f.email || '').toLowerCase();
      return name.includes(q) || email.includes(q);
    });
  }, [authFiles, importSearch, importChannelFilter]);
  // Channels actually present in the unfiltered list — used to populate the
  // filter dropdown. Showing the full OAUTH_TYPES catalog would include
  // channels with zero files; derived-from-data keeps the menu short.
  const availableChannels = useMemo(() => {
    const seen = new Map();
    for (const f of authFiles) {
      const ch = String(f?.type || f?.provider || '').toLowerCase();
      if (!ch) continue;
      seen.set(ch, (seen.get(ch) || 0) + 1);
    }
    return [...seen.entries()].sort((a, b) => a[0].localeCompare(b[0]));
  }, [authFiles]);
  const hasImportFilters = !!importSearch.trim() || !!importChannelFilter;
  const clearImportFilters = () => { setImportSearch(''); setImportChannelFilter(''); };

  // Bulk-selection derived state. Eligible rows are auth-files that are NOT
  // already imported (the badge says "already imported") — selecting those
  // would always 409, so we hide them from the bulk bar entirely. Consumes
  // `filteredAuthFiles` so the bulk-checkbox counts only include rows the
  // operator can currently see.
  const eligibleAuthFiles = useMemo(() => filteredAuthFiles.filter((f) => {
    const name = f.name || f.id || '';
    return !existingByFile.names.has(name);
  }), [filteredAuthFiles, existingByFile]);
  const eligibleNames = useMemo(
    () => new Set(eligibleAuthFiles.map((f) => f.name || f.id || '')),
    [eligibleAuthFiles],
  );
  // Garbage-collect any bulk-selected names that have become ineligible
  // (e.g. an upstream_provider row was created for them while the modal was
  // open, or the auth-file list was refreshed). Without this the operator
  // could try to bulk-create a name that no longer maps to an eligible row.
  useEffect(() => {
    if (bulkSelectedNames.size === 0) return;
    let changed = false;
    const next = new Set();
    for (const name of bulkSelectedNames) {
      if (eligibleNames.has(name)) next.add(name);
      else changed = true;
    }
    if (changed) setBulkSelectedNames(next);
  }, [eligibleNames]); // eslint-disable-line react-hooks/exhaustive-deps
  const eligibleSelectedCount = useMemo(() => {
    let n = 0;
    for (const name of bulkSelectedNames) if (eligibleNames.has(name)) n += 1;
    return n;
  }, [bulkSelectedNames, eligibleNames]);
  const toggleBulkRow = (name) => {
    setBulkSelectedNames((s) => {
      const next = new Set(s);
      if (next.has(name)) next.delete(name); else next.add(name);
      return next;
    });
  };
  const toggleAllEligible = () => {
    setBulkSelectedNames((s) => {
      const next = new Set(s);
      const allChecked = eligibleNames.size > 0 &&
        [...eligibleNames].every((n) => next.has(n));
      for (const name of eligibleNames) {
        if (allChecked) next.delete(name); else next.add(name);
      }
      return next;
    });
  };
  const clearBulkSelection = () => setBulkSelectedNames(new Set());

  const loadAuthFiles = useCallback(async () => {
    setAuthFilesLoading(true);
    setAuthFilesError('');
    try {
      const res = await listAuthFiles();
      const files = (res?.files || []).filter((f) => {
        const t = String(f?.type || f?.provider || '').toLowerCase();
        return !!AUTH_TYPE_TO_OAUTH_CHANNEL[t];
      });
      setAuthFiles(files);
      if (files.length === 0) {
        setTab('paste');
      }
    } catch (err) {
      setAuthFilesError(err.message || 'Failed to load auth files.');
    } finally {
      setAuthFilesLoading(false);
    }
  }, []);

  useEffect(() => { loadAuthFiles(); }, [loadAuthFiles]);

  // notifyImported — single chokepoint called by every successful import
  // path (Tab 1 createFromFields, Tab 2 createFromPaste, Tab 3 processFileText).
  // It triggers a parent reload (so existingProviderKeys flows in fresh for
  // Tab 1's "already imported" badge) and also re-fetches the modal's own
  // auth-files list (so a Tab 3 upload that created a brand-new auth file
  // appears in Tab 1 without a close/reopen).
  const notifyImported = useCallback(() => {
    onImported?.();
    loadAuthFiles();
  }, [onImported, loadAuthFiles]);

  // ---- Tab 1: select auth file → fetch JSON + models → preview.
  const selectFile = async (file) => {
    setSelectedFile(file);
    setPreview(null);
    setPreviewError('');
    const fileName = file.name || file.id || '';
    if (!fileName) return;
    try {
      const [rawJson, mres] = await Promise.all([
        fetchAuthFileJSON(fileName).catch(() => null),
        getAuthFileModels(fileName).catch(() => null),
      ]);
      if (!rawJson) {
        setPreviewError('Could not read the auth file JSON.');
        return;
      }
      const extracted = extractAuthFields(rawJson, { fileName: fileName.replace(/\.json$/i, '') });
      if (extracted.error) {
        setPreviewError(extracted.error);
        return;
      }
      const modelList = (mres?.models || []).map((m) => ({
        name: m.id || m.name || '',
        display_name: m.display_name || m.displayName || '',
      })).filter((m) => m.name);
      extracted.models = modelList;
      setPreview(extracted);
    } catch (err) {
      setPreviewError(err.message || 'Failed to preview auth file.');
    }
  };

  // ---- Create from previewed (Tab 1 or Tab 2).
  const createFromFields = async (fields, { uploadFirst = false, rawJsonString = '' } = {}) => {
    setCreating(true);
    try {
      let uploadErr = '';
      if (uploadFirst && rawJsonString) {
        try {
          await uploadAuthFileRaw(fields.file_name, rawJsonString);
          toast.info(`Wrote auth file ${fields.file_name}.json to auth dir.`);
        } catch (err) {
          // 409 = file already exists — acceptable; carry on with create.
          if (String(err.status) !== '409') {
            throw err;
          }
          uploadErr = ` (auth file already exists; reusing)`;
        }
      }
      const payload = buildOAuthCreatePayload(fields);
      try {
        await createUpstreamProvider(payload);
        toast.success(`Imported ${fields.provider_type}${uploadErr}`);
        notifyImported();
      } catch (err) {
        if (String(err.status) === '409') {
          toast.info(`${fields.provider_type} with file_name "${fields.file_name}" is already an upstream provider.`);
          // Even on 409, the parent reload might be stale — kick off the
          // refresh so the badge updates if a sibling Tab just imported it.
          notifyImported();
        } else {
          throw err;
        }
      }
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err.message || 'Import failed');
      toast.error(msg);
    } finally {
      setCreating(false);
    }
  };

  // ---- Shared: parse text → extract → upload → create. Returns a per-file
  // result object the calling tab renders. Reused by paste + upload tabs.
  //
  // After upload, fetches the auth-file's model catalog via getAuthFileModels
  // so the create payload carries the same model_aliases that Tab 1
  // (selectFile) does — without this the imported row's Models column reads
  // 0 in the table even though the server-side registry knows the models.
  const processFileText = async (fileLabel, text, sourceName = '') => {
    const result = { file: fileLabel, status: 'pending', message: '' };
    let parsed;
    try { parsed = JSON.parse(text); }
    catch (err) { result.status = 'error'; result.message = `Invalid JSON: ${err.message}`; return result; }

    const baseName = sourceName.replace(/\.json$/i, '') || '';
    const extracted = extractAuthFields(parsed, { fileName: baseName });
    if (extracted.error) { result.status = 'error'; result.message = extracted.error; return result; }

    const fileName = deriveFileName(extracted);
    extracted.file_name = fileName;
    result.file_name = fileName;
    let note = '';
    try {
      await uploadAuthFileRaw(fileName, text);
    } catch (err) {
      if (String(err.status) !== '409') { result.status = 'error'; result.message = err.message || 'Upload failed'; return result; }
      note = ' (auth file already exists; reusing)';
    }
    // Pull the file's known models from the server-side registry so the
    // upstream_providers row persists the same model_aliases as the auth file.
    // Failure here is non-fatal — the auth file may not be registered yet (the
    // upload handler registers it synchronously on success, but in some paths
    // it can take a moment). The create still goes through with no models.
    try {
      const mres = await getAuthFileModels(fileName);
      const modelList = (mres?.models || []).map((m) => ({
        name: m.id || m.name || '',
        display_name: m.display_name || m.displayName || '',
      })).filter((m) => m.name);
      extracted.models = modelList;
    } catch { /* non-fatal */ }
    const payload = buildOAuthCreatePayload(extracted);
    try {
      await createUpstreamProvider(payload);
      result.status = 'ok';
      result.message = `Imported oauth:${(extracted.provider_type || '').replace(/^oauth:/, '')}${note}`;
    } catch (err) {
      if (String(err.status) === '409') {
        result.status = 'ok';
        result.message = `Already an upstream provider (${extracted.provider_type}).`;
      } else {
        result.status = 'error';
        result.message = err.message || 'Create failed';
      }
    }
    return result;
  };

  // ---- Tab 1 bulk import: import every selected (non-already-imported)
  // auth-file as an upstream provider row. For each name we:
  //   1. fetch the raw auth JSON (the source of truth on disk),
  //   2. extract fields via extractAuthFields,
  //   3. fetch the server-side registry's known models for the file,
  //   4. POST /upstream-providers with the assembled payload.
  // Step 3 is the same fetch done by selectFile() for the single-row flow,
  // so the bulk path produces identical rows to clicking through Tab 1
  // one-by-one. No uploadAuthFileRaw call — the file is already on disk.
  // Per-row work is parallelized via Promise.allSettled so one failure
  // doesn't block the rest. Mirrors the main-table BulkActionBar pattern:
  // progress toast, then a final summary (success / partial / all-failed),
  // then notifyImported() to refresh existingProviderKeys + authFiles list.
  const importBulkFromFiles = async () => {
    if (eligibleSelectedCount === 0) return;
    const names = [...bulkSelectedNames].filter((n) => eligibleNames.has(n));
    if (names.length === 0) return;
    setBulkRunning(true);
    const progressId = toast.info(
      `Importing ${names.length} provider${names.length === 1 ? '' : 's'}…`,
      { duration: 0 },
    );
    const tasks = names.map(async (name) => {
      const result = { name, status: 'pending', message: '' };
      try {
        const rawJson = await fetchAuthFileJSON(name).catch(() => null);
        if (!rawJson) {
          result.status = 'error';
          result.message = 'Could not read the auth file JSON.';
          return result;
        }
        const extracted = extractAuthFields(rawJson, { fileName: name.replace(/\.json$/i, '') });
        if (extracted.error) {
          result.status = 'error';
          result.message = extracted.error;
          return result;
        }
        try {
          const mres = await getAuthFileModels(name);
          const modelList = (mres?.models || []).map((m) => ({
            name: m.id || m.name || '',
            display_name: m.display_name || m.displayName || '',
          })).filter((m) => m.name);
          extracted.models = modelList;
        } catch { /* non-fatal */ }
        try {
          await createUpstreamProvider(buildOAuthCreatePayload(extracted));
          const ch = (extracted.provider_type || '').replace(/^oauth:/, '');
          result.status = 'ok';
          result.message = `Imported oauth:${ch}`;
          return result;
        } catch (err) {
          if (String(err.status) === '409') {
            // Race with a sibling import — treat as already done.
            result.status = 'ok';
            result.message = `Already an upstream provider (${extracted.provider_type}).`;
          } else {
            result.status = 'error';
            result.message = err.message || 'Create failed';
          }
          return result;
        }
      } catch (err) {
        result.status = 'error';
        result.message = err.message || 'Unexpected error';
        return result;
      }
    });
    const settled = await Promise.allSettled(tasks);
    toast.dismiss(progressId);
    const ok = [];
    const failed = [];
    for (let i = 0; i < settled.length; i += 1) {
      const r = settled[i];
      if (r.status === 'fulfilled') {
        if (r.value.status === 'ok') ok.push(r.value);
        else failed.push(r.value);
      } else {
        failed.push({ name: names[i], status: 'error', message: r.reason?.message || 'Unknown error' });
      }
    }
    setBulkRunning(false);
    if (failed.length === 0) {
      clearBulkSelection();
      setSelectedFile(null);
      setPreview(null);
      toast.success(
        `Imported ${ok.length} provider${ok.length === 1 ? '' : 's'} from auth files`,
      );
      notifyImported();
    } else if (ok.length > 0) {
      // Partial: keep the failed names selected so the operator can retry.
      const failedNames = new Set(failed.map((f) => f.name));
      setBulkSelectedNames(failedNames);
      // Drop the preview if it pointed at a successfully-imported row.
      if (selectedFile && (selectedFile.name || selectedFile.id) &&
        failedNames.has(selectedFile.name || selectedFile.id) === false) {
        setSelectedFile(null);
        setPreview(null);
      }
      toast.error(
        `Imported ${ok.length}, ${failed.length} failed: ${failed[0].message}` +
          (failed.length > 1 ? ` (+${failed.length - 1} more)` : ''),
        { duration: 7000 },
      );
      notifyImported();
    } else {
      toast.error(
        `All ${failed.length} imports failed: ${failed[0].message}`,
        { duration: 7000 },
      );
    }
  };

  // ---- Tab 2: parse pasted JSON → show preview (no upload yet).
  const parsePastedJson = () => {
    setPastePreview(null);
    setPasteError('');
    const trimmed = rawJson.trim();
    if (!trimmed) {
      setPasteError('Paste a token JSON first.');
      return;
    }
    let parsed;
    try { parsed = JSON.parse(trimmed); }
    catch (err) { setPasteError(`Invalid JSON: ${err.message}`); return; }
    const extracted = extractAuthFields(parsed);
    if (extracted.error) { setPasteError(extracted.error); return; }
    const finalName = (fileNameInput.trim() || deriveFileName(extracted)).replace(/\.json$/i, '');
    if (!fileNameInput.trim()) setFileNameInput(finalName);
    extracted.file_name = finalName;
    setPastePreview(extracted);
  };

  const createFromPaste = async () => {
    if (!pastePreview) return;
    const fileName = fileNameInput.trim().replace(/\.json$/i, '');
    const fields = { ...pastePreview, file_name: fileName };
    setCreating(true);
    try {
      let note = '';
      try {
        await uploadAuthFileRaw(fileName, rawJson.trim());
      } catch (err) {
        if (String(err.status) !== '409') throw err;
        note = ' (auth file already exists; reusing)';
      }
      // Same model-fetch step as Tab 3 so the create payload carries the
      // server-side registry's known models for this auth file.
      try {
        const mres = await getAuthFileModels(fileName);
        const modelList = (mres?.models || []).map((m) => ({
          name: m.id || m.name || '',
          display_name: m.display_name || m.displayName || '',
        })).filter((m) => m.name);
        fields.models = modelList;
      } catch { /* non-fatal */ }
      try {
        await createUpstreamProvider(buildOAuthCreatePayload(fields));
        toast.success(`Imported ${fields.provider_type}${note}`);
        notifyImported();
      } catch (err) {
        if (String(err.status) === '409') {
          toast.info(`${fields.provider_type} with file_name "${fields.file_name}" is already an upstream provider.`);
          notifyImported();
        } else { throw err; }
      }
    } catch (err) {
      const msg = err instanceof ApiError ? err.message : (err.message || 'Import failed');
      toast.error(msg);
    } finally {
      setCreating(false);
    }
  };

  // ---- Tab 3: upload files (or dropped/pasted files) → process each.
  const handleFiles = async (fileList) => {
    const files = Array.from(fileList || []);
    if (files.length === 0) return;
    setUploadResult(null);
    const results = [];
    for (const file of files) {
      const text = await file.text();
      results.push(await processFileText(file.name, text, file.name));
    }
    setUploadResult(results);
    const okCount = results.filter((r) => r.status === 'ok').length;
    if (okCount > 0) toast.success(`Imported ${okCount}/${results.length} file(s).`);
    if (okCount > 0) notifyImported();
  };

  const onFileInput = (e) => {
    handleFiles(e.target.files);
    e.target.value = '';
  };

  // Drag-and-drop for the dropzone. Listens to dragenter/leave to toggle the
  // hover state (not just dragover) so the highlight only shows while the
  // cursor is inside the zone, matching native file-picker expectations.
  const onDrop = (e) => {
    e.preventDefault();
    setDropHover(false);
    handleFiles(e.dataTransfer?.files);
  };

  const footer = (
    <>
      <button type="button" onClick={onClose} disabled={creating}>Close</button>
      {tab === 'files' && preview && !preview.error && (
        previewAlreadyImported ? (
          <button type="button" disabled title="Already imported as an upstream provider">
            Already imported
          </button>
        ) : (
          <button
            type="button"
            className="primary"
            onClick={() => createFromFields(preview)}
            disabled={creating}
          >
            {creating ? 'Importing…' : `Create ${preview.provider_type}`}
          </button>
        )
      )}
      {tab === 'paste' && pastePreview && !pastePreview.error && (
        <button
          type="button"
          className="primary"
          onClick={createFromPaste}
          disabled={creating}
        >
          {creating ? 'Importing…' : `Create ${pastePreview.provider_type}`}
        </button>
      )}
    </>
  );

  const tabBtn = (k, label) => (
    <button
      type="button"
      className={`tab-bar__btn${tab === k ? ' is-active' : ''}`}
      onClick={() => setTab(k)}
      aria-pressed={tab === k}
    >{label}</button>
  );

  return (
    <Modal title="Import OAuth Provider" size="xl" onClose={onClose} footer={footer}>
      <div className="form-section">
        <div className="form-section__hint" style={{ marginBottom: 12 }}>
          Create an upstream provider from OAuth material that already exists — a
          token file already saved on the server, a JSON you paste here, or one or
          more JSON files you upload. To run the live browser login flow instead,
          use <strong>+ New Provider</strong>.
        </div>

        <div className="tab-bar" role="tablist" style={{ marginBottom: 16 }}>
          {tabBtn('files', `From auth files (${authFiles.length})`)}
          {tabBtn('paste', 'Paste JSON')}
          {tabBtn('upload', 'Upload files')}
        </div>

        {tab === 'files' && (
          <>
            {authFilesLoading ? <Spinner label="Loading auth files…" /> : null}
            {authFilesError ? <ErrorBanner error={{ message: authFilesError }} onRetry={loadAuthFiles} /> : null}
            {!authFilesLoading && !authFilesError && authFiles.length === 0 ? (
              <EmptyState title="No OAuth auth files on disk"
                hint="No OAuth-type auth files found on the server. Switch to “Paste JSON” or “Upload files” to import credentials from elsewhere."
                actions={
                  <div className="row gap-sm">
                    <button onClick={() => setTab('paste')}>Paste JSON</button>
                    <button className="primary" onClick={() => setTab('upload')}>Upload files</button>
                  </div>
                }
              />
            ) : null}
            {authFiles.length > 0 && (
              <div className="catalog-toolbar" style={{ marginBottom: 10 }}>
                <input
                  className="search-input"
                  type="text"
                  value={importSearch}
                  onChange={(e) => setImportSearch(e.target.value)}
                  placeholder="Search by file name or email…"
                  aria-label="Search auth files"
                />
                <select
                  className="search-input"
                  value={importChannelFilter}
                  onChange={(e) => setImportChannelFilter(e.target.value)}
                  aria-label="Filter by OAuth channel"
                  style={{ maxWidth: 200 }}
                >
                  <option value="">All channels</option>
                  {availableChannels.map(([ch, n]) => (
                    <option key={ch} value={ch}>oauth:{ch} ({n})</option>
                  ))}
                </select>
                {hasImportFilters && (
                  <button
                    className="ghost"
                    onClick={clearImportFilters}
                    aria-label="Clear filters"
                    title="Clear filters"
                  >Clear filters</button>
                )}
                <span className="catalog-toolbar__spacer" />
                <span className="catalog-toolbar__count dim" style={{ fontSize: 11 }}>
                  {filteredAuthFiles.length === authFiles.length
                    ? `${authFiles.length} file${authFiles.length === 1 ? '' : 's'}`
                    : `${filteredAuthFiles.length} of ${authFiles.length}`}
                </span>
              </div>
            )}
            {authFiles.length > 0 && filteredAuthFiles.length === 0 ? (
              <EmptyState
                title="No auth files match the filters"
                hint="Adjust the search or channel filter to see more files."
                actions={
                  <button onClick={clearImportFilters}>Clear filters</button>
                }
              />
            ) : null}
            {authFiles.length > 0 && filteredAuthFiles.length > 0 && (() => {
              // Header tri-state for the bulk checkbox. Computed here so the
              // checkbox + its ref + its indeterminate flag all stay in sync.
              const eligibleOnPage = eligibleAuthFiles.length;
              const selectedEligible = [...bulkSelectedNames].filter((n) => eligibleNames.has(n)).length;
              const allChecked = eligibleOnPage > 0 && selectedEligible === eligibleOnPage;
              const someChecked = selectedEligible > 0 && !allChecked;
              return (
              <table className="table">
                <thead>
                  <tr>
                    <th className="col-check">
                      <input
                        type="checkbox"
                        aria-label="Select all eligible auth files"
                        title={eligibleOnPage === 0
                          ? 'No eligible files to select'
                          : allChecked
                            ? `Unselect all (${selectedEligible})`
                            : `Select all ${eligibleOnPage} eligible`}
                        disabled={eligibleOnPage === 0}
                        checked={allChecked}
                        ref={(el) => { if (el) el.indeterminate = someChecked; }}
                        onChange={toggleAllEligible}
                      />
                    </th>
                    <th>File</th><th>Channel</th><th>Email</th><th>State</th>
                  </tr>
                </thead>
                <tbody>
                  {filteredAuthFiles.map((f) => {
                    const name = f.name || f.id || '';
                    const ch = String(f?.type || f?.provider || '').toLowerCase();
                    const already = existingByFile.names.has(name);
                    const isSel = selectedFile && (selectedFile.name || selectedFile.id) === name;
                    const isBulk = !already && bulkSelectedNames.has(name);
                    return (
                      <tr key={name}
                        className={`clickable-row${isBulk ? ' row--selected' : ''}`}
                        style={{ cursor: 'pointer', background: !isBulk && isSel ? 'var(--bg-elevated)' : undefined }}
                        onClick={() => selectFile(f)}
                      >
                        <td className="col-check" onClick={(e) => e.stopPropagation()}>
                          <input
                            type="checkbox"
                            aria-label={`Select ${name} for bulk import`}
                            disabled={already}
                            checked={isBulk}
                            onChange={() => toggleBulkRow(name)}
                            title={already ? 'Already imported' : isBulk ? 'Unselect' : 'Select for bulk import'}
                          />
                        </td>
                        <td><code>{name}</code></td>
                        <td>
                          <span className="badge badge--muted" style={{ fontSize: 10 }}>oauth:{ch}</span>
                        </td>
                        <td>{f.email || <span className="dim">—</span>}</td>
                        <td>{already
                          ? <span className="badge badge--muted">already imported</span>
                          : <span className="badge badge--active">available</span>}</td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
              );
            })()}
            {eligibleSelectedCount > 0 && (
              <div className="bulk-action-bar bulk-action-bar--inline" role="region" aria-label="Bulk import">
                <div className="bulk-action-bar__count">
                  <strong>{eligibleSelectedCount}</strong> auth file{eligibleSelectedCount === 1 ? '' : 's'} selected for import
                </div>
                <div className="bulk-action-bar__breakdown">
                  {(() => {
                    // Count selected by channel for situational awareness.
                    // Iterate `filteredAuthFiles` so the chips only show
                    // channels that are currently visible (operator would
                    // otherwise see channels they've already filtered out).
                    const m = new Map();
                    for (const f of filteredAuthFiles) {
                      const name = f.name || f.id || '';
                      if (!bulkSelectedNames.has(name)) continue;
                      const ch = String(f?.type || f?.provider || '').toLowerCase();
                      m.set(ch, (m.get(ch) || 0) + 1);
                    }
                    return [...m.entries()].sort((a, b) => b[1] - a[1]).map(([ch, n]) => (
                      <span key={ch} className="filter-chip" title={`${n} ${ch}`}>
                        oauth:{ch}<span className="dim">×{n}</span>
                      </span>
                    ));
                  })()}
                </div>
                <div className="bulk-action-bar__actions">
                  <button
                    className="primary"
                    onClick={importBulkFromFiles}
                    disabled={bulkRunning}
                    title="Create upstream provider rows for every selected file"
                  >
                    {bulkRunning ? 'Importing…' : `Import ${eligibleSelectedCount} selected`}
                  </button>
                  <button
                    className="ghost"
                    onClick={clearBulkSelection}
                    disabled={bulkRunning}
                    title="Clear selection"
                  >Clear</button>
                </div>
              </div>
            )}
            {previewError && <div className="error-banner" style={{ marginTop: 8 }}>{previewError}</div>}
            <PreviewCard fields={preview} fileNameOverride={selectedFile && (selectedFile.name || selectedFile.id)} alreadyImported={previewAlreadyImported} />
          </>
        )}

        {tab === 'paste' && (
          <div className="form-section">
            <div className="form-section__row">
              <textarea
                rows={10}
                value={rawJson}
                onChange={(e) => setRawJson(e.target.value)}
                placeholder={'{\n  "type": "claude",\n  "access_token": "…",\n  "refresh_token": "…",\n  "expires_at": "2026-08-01T12:00:00Z",\n  "email": "user@example.com"\n}'}
                spellCheck={false}
                style={{ width: '100%', fontFamily: 'var(--mono, monospace)', fontSize: 12 }}
                aria-label="Raw auth-file JSON"
              />
            </div>
            <div className="form-section__row" style={{ display: 'grid', gridTemplateColumns: '1fr auto', gap: 8, alignItems: 'end' }}>
              <Field label="File name (without .json)" hint="Auto-derived from the JSON if left blank.">
                <input type="text" value={fileNameInput}
                  onChange={(e) => setFileNameInput(e.target.value)}
                  placeholder="auto-derived" />
              </Field>
              <button type="button" onClick={parsePastedJson} disabled={!rawJson.trim()}>Parse</button>
            </div>
            {pasteError && <div className="error-banner">{pasteError}</div>}
            <PreviewCard fields={pastePreview} fileNameOverride={fileNameInput} />
          </div>
        )}

        {tab === 'upload' && (
          <div className="form-section">
            <div
              className={`dropzone${dropHover ? ' dropzone--hover' : ''}`}
              onDragOver={(e) => { e.preventDefault(); setDropHover(true); }}
              onDragEnter={(e) => { e.preventDefault(); setDropHover(true); }}
              onDragLeave={() => setDropHover(false)}
              onDrop={onDrop}
              role="button"
              tabIndex={0}
              onClick={() => fileInputRef.current?.click()}
              onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') fileInputRef.current?.click(); }}
              aria-label="Choose or drop JSON files"
            >
              <div style={{ fontSize: 13 }}>Drop auth-file JSON files here, or click to choose</div>
              <div className="dropzone__hint">Multiple files OK. Vertex service-account JSON is skipped — use the Vertex import page.</div>
              <input
                ref={fileInputRef}
                type="file"
                accept=".json,application/json"
                multiple
                onChange={onFileInput}
                style={{ display: 'none' }}
                aria-hidden="true"
                tabIndex={-1}
              />
            </div>
            {uploadResult && (
              <table className="table" style={{ marginTop: 12 }}>
                <thead><tr><th>File</th><th>Status</th><th>Message</th></tr></thead>
                <tbody>
                  {uploadResult.map((r, i) => (
                    <tr key={i}>
                      <td><code>{r.file}</code></td>
                      <td>
                        <span className={`badge ${r.status === 'ok' ? 'badge--active' : 'badge--disabled'}`}>
                          {r.status}
                        </span>
                      </td>
                      <td className="dim" style={{ fontSize: 12 }}>{r.message}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
            {!uploadResult && (
              <EmptyState title="No files processed yet"
                hint="Drop or pick one or more .json files above to import them as upstream providers." />
            )}
          </div>
        )}
      </div>
    </Modal>
  );
}

// APIKeyEntriesEditor — multi-row editor for openai-compatibility
// api-key-entries. Each row carries an optional normalised identity (also
// known as the entry provider key), a masked API key, and an optional proxy
// URL. The persisted child-row id is round-tripped so the backend can update
// rows in place rather than deleting and reinserting them.
function APIKeyEntriesEditor({ entries, onChange, error = '' }) {
  const safe = Array.isArray(entries) ? entries : [];
  function update(idx, patch) {
    onChange(safe.map((e, i) => (i === idx ? { ...e, ...patch } : e)));
  }
  function add() {
    onChange([...safe, { api_key: '', proxy_url: '', name: '', id: 0 }]);
  }
  function remove(idx) { onChange(safe.filter((_, i) => i !== idx)); }

  // Entry identity rules mirror the backend's normalizeUpstreamProviderEntryName.
  // Errors are surfaced inline, never include the secret, and the raw API key
  // is never used as a label or placeholder.
  const errors = useMemo(() => validateAPIKeyEntries(safe), [safe]);

  function rowKey(e, idx) {
    const id = Number(e && e.id) || 0;
    return id > 0 ? `entry-${id}` : `entry-new-${idx}`;
  }

  return (
    <div className="list-editor">
      {safe.length === 0 && <div className="list-editor__empty">No API key entries. Click "+ Add key".</div>}
      {error && (
        <div className="error-banner" role="alert" style={{ marginTop: 4 }}>{error}</div>
      )}
      {errors.__global && (
        <div className="error-banner" role="alert" style={{ marginTop: 4 }}>{errors.__global}</div>
      )}
      {safe.map((e, idx) => {
        const id = Number(e && e.id) || 0;
        const rowErr = errors[idx] || {};
        const hint = id > 0
          ? `Persisted as entry #${id}. ${idHintForIdentity(e)}`
          : 'Blank identity will become key-<id> after save.';
        return (
          <div className="list-editor__rowgroup" key={rowKey(e, idx)}>
            <div className="list-editor__row">
              <input
                type="text"
                value={e.name || ''}
                onChange={(ev) => update(idx, { name: ev.target.value })}
                placeholder="identity (optional, e.g. team-a)"
                spellCheck={false}
                aria-label="API key entry identity"
                aria-invalid={!!rowErr.name}
                data-testid={`api-key-entry-name-${idx}`}
              />
              <PasswordInput
                value={e.api_key}
                onChange={(v) => update(idx, { api_key: v })}
                placeholder="api key"
              />
              <input
                type="text"
                value={e.proxy_url || ''}
                onChange={(ev) => update(idx, { proxy_url: ev.target.value })}
                placeholder="proxy url (optional)"
                spellCheck={false}
                aria-label="API key entry proxy URL"
              />
              <button
                type="button"
                className="list-editor__remove"
                onClick={() => remove(idx)}
                aria-label="Remove entry"
                title="Remove"
              >×</button>
            </div>
            <div className="list-editor__rowhint muted" style={{ fontSize: 11 }}>
              {hint}
            </div>
            {rowErr.name && (
              <div className="form__error" role="alert">{rowErr.name}</div>
            )}
          </div>
        );
      })}
      <button type="button" className="list-editor__add" onClick={add}>+ Add key</button>
    </div>
  );
}

// validateAPIKeyEntries enforces the same rules the backend's
// normalizeUpstreamProviderEntryName applies, plus the duplicate-name check
// across the provider's entries. Returns a map keyed by entry index plus a
// __global bucket for cross-entry messages.
function validateAPIKeyEntries(entries) {
  const out = {};
  const seenNames = new Map(); // normalised name → first occurrence idx
  for (let i = 0; i < entries.length; i += 1) {
    const e = entries[i] || {};
    const errs = {};
    const raw = typeof e.name === 'string' ? e.name : '';
    const trimmed = raw.trim();
    const normalised = trimmed.toLowerCase();
    if (trimmed !== '') {
      if (!/^[a-z0-9][a-z0-9_-]*$/i.test(trimmed)) {
        errs.name = 'Identity may only contain letters, digits, "_" and "-". It must start with a letter or digit.';
      } else if (/^key-[0-9]+$/.test(normalised)) {
        errs.name = `"${trimmed}" is reserved for auto-generated identities. Use a different value or leave blank.`;
      } else if (seenNames.has(normalised)) {
        const firstIdx = seenNames.get(normalised);
        errs.name = firstIdx === i
          ? 'Duplicate identity detected.'
          : `Duplicate identity. Entry ${firstIdx + 1} already uses "${trimmed}".`;
      }
    }
    if (Object.keys(errs).length > 0) out[i] = errs;
    if (trimmed !== '' && !errs.name) {
      seenNames.set(normalised, i);
    }
  }
  return out;
}

// idHintForIdentity renders the row's eventual route identity for the hint
// line. Existing rows use the server-returned id; fresh rows omit the hint.
function idHintForIdentity(e) {
  const id = Number(e && e.id) || 0;
  const normalised = typeof e.name === 'string' ? e.name.trim().toLowerCase() : '';
  const valid = normalised && /^[a-z0-9][a-z0-9_-]*$/.test(normalised) && !/^key-[0-9]+$/.test(normalised);
  if (valid) return `Route identity: ${normalised}.`;
  if (id > 0) return `Route identity: key-${id}.`;
  return 'Blank identity will become key-<id> after save.';
}

// ============================================================================
// Form construction & payload building
// ============================================================================

function buildForm(providerType, initial, carryOver) {
  const src = initial || {};
  const carry = carryOver || {};
  const base = {
    provider_type: providerType || src.provider_type || '',
    name: carry.name ?? src.name ?? '',
    label: carry.label ?? src.label ?? '',
    api_key: carry.api_key ?? src.api_key ?? '',
    base_url: carry.base_url ?? src.base_url ?? '',
    proxy_url: carry.proxy_url ?? src.proxy_url ?? '',
    prefix: carry.prefix ?? src.prefix ?? '',
    email: src.email ?? '',
    file_name: src.file_name ?? '',
    priority: carry.priority ?? src.priority ?? 0,
    disabled: src.disabled ?? false,
    websockets: src.websockets ?? false,
    rebuild_mid_system_message: src.rebuild_mid_system_message ?? false,
    experimental_cch_signing: src.experimental_cch_signing ?? false,
    disable_cooling: (src.extra_config && src.extra_config.disable_cooling) ?? false,
    headers: [],
    models: [],
    excluded_models: src.excluded_models ?? [],
    api_key_entries: [],
    // Cloak
    cloak_mode: src.cloak_mode ?? '',
    cloak_strict_mode: src.cloak_strict_mode ?? false,
    cloak_sensitive_words: src.cloak_sensitive_words ?? [],
    cloak_cache_user_id: src.cloak_cache_user_id ?? false,
    // OAuth tokens
    token_access_token: src.token_access_token ?? '',
    token_refresh_token: src.token_refresh_token ?? '',
    token_expiry: src.token_expiry ? formatRFC3339(src.token_expiry) : '',
    token_expired: src.token_expired ?? false,
  };

  // Hydrate models (camelCase shape from the API).
  if (Array.isArray(src.models) && src.models.length > 0) {
    base.models = src.models.map((m) => ({
      name: m.name || '',
      alias: m.alias || '',
      'display-name': m.display_name || m.displayName || '',
      'force-mapping': !!m.force_mapping,
      'fork': !!m.fork,
      image: !!m.image,
      'input-modalities': m.input_modalities || m.inputModalities || [],
      'output-modalities': m.output_modalities || m.outputModalities || [],
    }));
  }

  // Hydrate headers map → KeyValueEditor rows.
  if (src.headers && typeof src.headers === 'object') {
    base.headers = Object.entries(src.headers).map(([key, value]) => ({ key, value: String(value) }));
  }

  // Hydrate openai-compat api_key_entries. Round-trip the persisted child-row
  // id and the server-normalised name so subsequent saves update in place
  // rather than deleting and reinserting the row.
  if (Array.isArray(src.api_key_entries)) {
    base.api_key_entries = src.api_key_entries.map((e) => ({
      id: Number(e.id) || 0,
      name: e.name || '',
      api_key: e.api_key || '',
      proxy_url: e.proxy_url || '',
    }));
  }

  return base;
}

function buildPayload(form, providerType) {
  const oauth = isOAuth(providerType);
  const openai = isOpenAI(providerType);
  const claude = isClaude(providerType);

  const headersMap = {};
  (form.headers || []).forEach(({ key, value }) => {
    const k = (key || '').trim();
    if (k) headersMap[k] = value;
  });

  const cleanedModels = (form.models || [])
    .filter((r) => r && (r.name || r.alias))
    .map((r) => {
      const m = {
        name: (r.name || '').trim(),
        alias: (r.alias || '').trim(),
        display_name: (r['display-name'] || '').trim(),
        force_mapping: !!r['force-mapping'],
        fork: !!r['fork'],
      };
      if (openai) {
        if (r.image) m.image = true;
        const im = r['input-modalities'];
        if (Array.isArray(im) && im.length) m.input_modalities = im;
        const om = r['output-modalities'];
        if (Array.isArray(om) && om.length) m.output_modalities = om;
      }
      return m;
    });

  // Build extra_config from the form's toggle fields + any pre-existing
  // extra_config keys (e.g. request_retry, tool_prefix_disabled populated
  // during the OAuth connect flow).
  const extra = { ...(typeof form.extra_config === 'object' ? form.extra_config : {}) };
  if (form.disable_cooling) extra.disable_cooling = true;
  // Don't send an empty object — extra_config defaults to '{}' server-side.
  const extraConfig = Object.keys(extra).length > 0 ? extra : {};

  const payload = {
    provider_type: providerType,
    priority: Number(form.priority) || 0,
    disabled: !!form.disabled,
    prefix: (form.prefix || '').trim(),
    base_url: (form.base_url || '').trim(),
    proxy_url: (form.proxy_url || '').trim() || 'none',
    headers: headersMap,
    models: cleanedModels,
    excluded_models: form.excluded_models || [],
    extra_config: extraConfig,
  };

  if (oauth) {
    payload.email = (form.email || '').trim();
    payload.file_name = (form.file_name || '').trim();
    payload.label = (form.label || '').trim();
    payload.token_access_token = form.token_access_token || '';
    payload.token_refresh_token = form.token_refresh_token || '';
    payload.token_expired = !!form.token_expired;
    if (form.token_expiry) {
      const t = new Date(form.token_expiry);
      if (!Number.isNaN(t.getTime())) {
        payload.token_expiry = t.toISOString();
      }
    }
  } else if (openai) {
    payload.name = (form.name || '').trim();
    // api_key_entries round-trip the persisted child-row id and the optional
    // normalised identity. The id is only sent when it is a positive integer;
    // new rows omit it so the backend treats them as inserts.
    payload.api_key_entries = (form.api_key_entries || [])
      .filter((e) => e.api_key && e.api_key.trim())
      .map((e) => {
        const entry = {
          api_key: e.api_key.trim(),
          proxy_url: (e.proxy_url || '').trim(),
        };
        const id = Number(e.id) || 0;
        if (id > 0) entry.id = id;
        const name = typeof e.name === 'string' ? e.name.trim().toLowerCase() : '';
        if (name) entry.name = name;
        return entry;
      });
  } else {
    // API-key providers. The Identifier field maps to the generic `name`
    // column (lower-cased by the proxy into the routing provider key).
    payload.name = (form.name || '').trim();
    payload.api_key = (form.api_key || '').trim();
  }

  if (claude) {
    // The Identifier field is mapped to the generic `name` column.
    payload.name = (form.name || '').trim();
    payload.rebuild_mid_system_message = !!form.rebuild_mid_system_message;
    payload.experimental_cch_signing = !!form.experimental_cch_signing;
    payload.cloak_mode = form.cloak_mode || '';
    payload.cloak_strict_mode = !!form.cloak_strict_mode;
    payload.cloak_sensitive_words = form.cloak_sensitive_words || [];
    payload.cloak_cache_user_id = !!form.cloak_cache_user_id;
  }

  // Codex/xAI websockets.
  if (providerType === 'codex-api-key' || providerType === 'xai-api-key') {
    payload.websockets = !!form.websockets;
  }

  return payload;
}

// ============================================================================
// Validation
// ============================================================================

function validate(form, schema, providerType, siblingNames, isEdit) {
  const errors = {};
  for (const section of schema.sections) {
    for (const field of section.fields) {
      const v = form[field.name];
      if (field.required && !field._skipRequired && (v === '' || v === null || v === undefined ||
          (Array.isArray(v) && v.length === 0 && field.type !== 'chips'))) {
        // For arrays/toggles, empty isn't necessarily invalid. Only flag
        // text/password/number/select required-as-non-empty.
        if (['text', 'password', 'number'].includes(field.type)) {
          if (!v || (typeof v === 'string' && !v.trim())) {
            errors[field.name] = `${field.label} is required.`;
            continue;
          }
        }
      }
      if (field.validate && v) {
        const msg = field.validate(v);
        if (msg) errors[field.name] = msg;
      }
    }
  }
  // OpenAI-compat name uniqueness.
  if (isOpenAI(providerType)) {
    const name = (form.name || '').trim();
    if (name && siblingNames.includes(name)) {
      // In edit mode, the current row's own name is in siblingNames too;
      // we can't distinguish without the editing row's id in the list, so
      // only flag duplicates when there are 2+ occurrences.
      const occurrences = siblingNames.filter((n) => n === name).length;
      if (!isEdit || occurrences > 1) {
        errors.name = `Another provider already uses the name "${name}".`;
      }
    }
    // API-key entry identity validation mirrors the backend's
    // normalizeUpstreamProviderEntryName so the Save button cannot submit
    // a payload the server would reject.
    const entryErrors = validateAPIKeyEntries(form.api_key_entries || []);
    const dupCount = Object.keys(entryErrors).length;
    if (dupCount > 0) {
      errors.api_key_entries = `${dupCount} API key entr${dupCount === 1 ? 'y has' : 'ies have'} an identity problem. See inline messages below.`;
    }
  }
  return errors;
}

// ============================================================================
// Helpers
// ============================================================================

// capitalize — first-letter uppercase for verbs in bulk-summary toasts. We
// don't need full title-case because the strings are short ("Enabled" /
// "Disabled" / "Deleted").
function capitalize(s) {
  if (!s) return '';
  return s.charAt(0).toUpperCase() + s.slice(1);
}

function formatRFC3339(t) {
  if (!t) return '';
  try {
    const d = new Date(t);
    if (Number.isNaN(d.getTime())) return '';
    return d.toISOString().replace(/\.\d{3}Z$/, 'Z');
  } catch { return ''; }
}

// strVal safely extracts a trimmed string from an object by key. Returns ''
// for missing/null/non-string values.
function strVal(obj, key) {
  if (!obj || typeof obj !== 'object') return '';
  const v = obj[key];
  if (typeof v === 'string') return v.trim();
  if (typeof v === 'number') return String(v);
  return '';
}

function formatTime(iso) {
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '—';
    return d.toLocaleString();
  } catch { return '—'; }
}

// formatRelativeTime returns a compact "5m ago / 2h ago / 3d ago" string for
// recent timestamps and falls back to a short date for older ones. Operators
// scanning the table care about "is this fresh" more than the exact time, so
// the relative form reads more clearly than "1/26/2026, 9:31:42 AM".
function formatRelativeTime(iso) {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '—';
  const diffMs = Date.now() - d.getTime();
  const sec = Math.round(diffMs / 1000);
  if (sec < 60) return 'just now';
  const min = Math.round(sec / 60);
  if (min < 60) return `${min}m ago`;
  const hr = Math.round(min / 60);
  if (hr < 24) return `${hr}h ago`;
  const day = Math.round(hr / 24);
  if (day < 30) return `${day}d ago`;
  return d.toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' });
}
