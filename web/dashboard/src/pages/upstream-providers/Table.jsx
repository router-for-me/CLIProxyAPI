// Table.jsx — self-contained upstream-providers catalog view.
//
// Owns: toolbar (search, type select, clear filters, row count, refresh
// health button), the HealthSummary chip strip (wired to a local
// `healthFilters` Set via the `onNavigate` callback), the providers table
// (with the new Health column), the bulk-action bar + bulk-delete confirm
// modal, and pagination. Local sort + filter + selection state live here so
// the parent only has to feed the raw `providers` array + `liveStatus`
// map and react to the few callbacks it cares about.
//
// Does NOT own: the page header (title + global action buttons), the
// ImportOAuthProviderModal, or the GlobalOAuthModelAliasCard /
// ChannelAliasEditor. Those stay in UpstreamProvidersPage.jsx for now;
// Task 17 wires the parent to use this component.

import React, { useState, useMemo, useEffect } from 'react';
import { useNavigate } from 'react-router-dom';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../../components/Primitives.jsx';
import { useToast } from '../../components/Toast.jsx';
import { StatusDot } from './components/StatusDot.jsx';
import { HealthSummary } from './HealthSummary.jsx';
import { applyFilters, applySort } from './filters.js';
import { statusFromHealth, cooldownReason, getHealthSummary } from './health.js';
import {
  API_KEY_TYPES,
  OAUTH_TYPES,
  TYPE_LABEL,
  isOAuth,
} from '../upstream-provider-editor/schemas.js';

// ============================================================================
// Component
// ============================================================================

export default function UpstreamProvidersTable({
  providers,
  liveStatus = {},
  onRefreshHealth,
  onRefresh,
  onCreate,
  onBulkAction,   // parent may pass a notification callback (toast etc.); bulk work happens here
  refreshing = false,
  loading = false,
  error = null,
  onRetry,
}) {
  const toast = useToast();
  const navigate = useNavigate();
  const [search, setSearch] = useState('');
  const [typeFilter, setTypeFilter] = useState('');
  // Multi-select health filter — empty set = no filter (show all). The
  // HealthSummary's onNavigate(key|null) toggles this set; null clears it.
  const [healthFilters, setHealthFilters] = useState(() => new Set());
  // Table sort: { key, dir } — null = leave server order. Keys map 1:1 to
  // row fields so the sort happens purely client-side over the filtered list.
  const [sort, setSort] = useState({ key: 'updated_at', dir: 'desc' });
  // Client-side pagination. Rows/page is operator-selectable; the current
  // page resets whenever filters/search/sort change so the operator never
  // lands on an out-of-range page after narrowing the list.
  const [page, setPage] = useState(1);
  const [pageSize, setPageSize] = useState(25);
  // Bulk-action selection. Stored as a Set of upstream_provider.id values so
  // it survives filter/sort/page changes (operators can narrow, act, then
  // re-broaden without losing the selection). `null` when no bulk action is
  // in flight; otherwise the action identifier ("delete" | "enable" | "disable").
  const [selectedIds, setSelectedIds] = useState(() => new Set());
  const [bulkRunning, setBulkRunning] = useState(false);
  // Confirm-modal for destructive bulk delete (non-destructive Enable/Disable
  // confirm inline via a single toast, matching how single-row toggles work).
  const [confirmBulkDelete, setConfirmBulkDelete] = useState(null);
  // Local single-row delete confirm (parent owns the API mutation via
  // onDelete; Table just renders the modal and invokes onDelete on confirm).
  const [confirmDelete, setConfirmDelete] = useState(null);

  // Health summary for the chip strip. Computed from the FULL providers
  // list (not the filtered view) so operators can see the absolute counts
  // even while a filter is active.
  const healthSummary = useMemo(
    () => getHealthSummary(providers, liveStatus),
    [providers, liveStatus],
  );

  // typeFilter accepts three shapes:
  //   ''                 → no filter
  //   'api' / 'oauth'    → category filter (from the stat tiles)
  //   '<provider_type>'  → exact match (from the dropdown)
  // The current spec drops the legacy 'api' / 'oauth' shortcuts; the
  // dropdown is exact-match only. We mirror filters.js's contract: only the
  // exact provider_type passes the type filter, no implicit category
  // shortcut. Operators wanting the broader view should use the dropdown's
  // optgroup headings.
  function matchesTypeFilter(providerType) {
    if (!typeFilter) return true;
    return providerType === typeFilter;
  }

  const filtered = useMemo(() => {
    // Defer to the shared pure helper so behavior matches other surfaces
    // (HealthPage, future search filters, etc.). We still keep `typeFilter`
    // / `healthFilters` / `search` local so the UI can stay synchronized
    // with the rendered table.
    const out = applyFilters(providers, {
      search,
      typeFilter,
      healthFilters,
      liveStatus,
    });
    // Sort on top of the filtered set. The shared helper handles the
    // 'health' key specially; we pass through the rest as-is.
    const sorted = sort && sort.key
      ? applySort(out, sort.key, sort.dir, liveStatus)
      : out;
    return sorted;
  }, [providers, search, typeFilter, healthFilters, sort, liveStatus]);

  // Reset to page 1 whenever the result set's shape changes (filter,
  // search, sort, or the underlying provider list). Without this the
  // operator can narrow the list and land on an empty page.
  useEffect(() => { setPage(1); }, [search, typeFilter, healthFilters, sort, providers.length]);

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
  // following a delete from another session). Recomputed on every render
  // so the bulk toolbar always reflects current truth.
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
        for (const p of pagedRows) next.delete(p.id);
      } else {
        for (const p of pagedRows) next.add(p.id);
      }
      return next;
    });
  };
  const clearSelection = () => setSelectedIds(new Set());

  // Garbage-collect stale IDs from the selection whenever the providers
  // list changes (after reload). Keeps `selectedProviders` honest without
  // forcing the operator to re-tick boxes after a delete from another tab.
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
    // We intentionally key off providers.length + first/last id instead of
    // the full `providers` array to avoid recomputing on unrelated edits.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [providers.length, providers[0]?.id, providers[providers.length - 1]?.id]);

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

  const hasFilters = !!search.trim() || !!typeFilter || (healthFilters && healthFilters.size > 0);

  // runBulk performs a single bulk action ("enable" | "disable" | "delete")
  // over the currently selected providers. Runs the per-row mutations in
  // parallel via Promise.allSettled so a single 4xx/5xx doesn't block the
  // rest. If the parent supplied `onBulkAction`, we forward a batch
  // payload (action + targets) and let the parent own the API call so the
  // existing refresh / toast wiring stays in one place. Falls back to a
  // no-op with a toast when no parent handler is registered.
  const runBulk = async (action) => {
    if (selectedCount === 0) return;
    if (typeof onBulkAction !== 'function') {
      toast.error(`No bulk handler registered for "${action}"`);
      return;
    }
    setBulkRunning(true);
    try {
      // The parent decides how to talk to the server (single calls in
      // parallel, batched PUTs, etc.). Table owns selection state only.
      await onBulkAction({ action, targets: selectedProviders });
      // Optimistic: clear selection on full success. Partial-failure
      // narrowing is the parent's job (it returns / throws appropriately).
      // We mirror the original parent logic by clearing selection here and
      // letting the parent call reload() as needed.
      clearSelection();
    } catch (err) {
      toast.error(err?.message || `Bulk ${action} failed`);
    } finally {
      setBulkRunning(false);
      setConfirmBulkDelete(null);
    }
  };

  const onHealthNavigate = (key) => {
    setHealthFilters((prev) => {
      const next = new Set(prev);
      if (key === null) return next;          // null = no-op for multi-select
      if (next.has(key)) next.delete(key);    // toggle off when clicked again
      else next.add(key);                    // toggle on
      return next;
    });
  };

  // Build the per-row health breakdown for the bulk-action bar so the
  // operator sees what mix of states they're about to mutate. Mirrors the
  // type-breakdown pattern already in BulkActionBar but uses the new
  // HEALTH_STATES buckets instead of provider_type.
  const selectedHealthBreakdown = useMemo(() => {
    if (selectedCount === 0) return [];
    const m = new Map();
    for (const p of selectedProviders) {
      const k = statusFromHealth(p, liveStatus[String(p.id)]);
      m.set(k, (m.get(k) || 0) + 1);
    }
    return [...m.entries()].sort((a, b) => b[1] - a[1]);
  }, [selectedProviders, selectedCount, liveStatus]);

  return (
    <>
      <HealthSummary
        summary={healthSummary}
        onNavigate={onHealthNavigate}
        activeKey={null}
      />

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
              onClick={() => {
                setSearch('');
                setTypeFilter('');
                setHealthFilters(new Set());
              }}
              aria-label="Clear all filters"
              title="Clear filters"
            >
              Clear filters
            </button>
          )}
          {healthFilters.size > 0 && (
            <button
              className="ghost"
              onClick={() => setHealthFilters(new Set())}
              aria-label="Clear health filters"
              title="Clear health filters"
            >
              Clear health
            </button>
          )}
          <span className="catalog-toolbar__spacer" />
          <span className="catalog-toolbar__count dim" style={{ fontSize: 11 }}>
            {totalFiltered === providers.length
              ? `${providers.length} provider${providers.length === 1 ? '' : 's'}`
              : `${totalFiltered} of ${providers.length}`}
          </span>
          <button
            className="secondary"
            onClick={() => {
              if (onRefreshHealth) onRefreshHealth();
              else toast.info('Health refresh requested');
            }}
            disabled={refreshing}
            aria-label="Refresh provider health"
            title="Re-poll upstream health for every row"
          >
            {refreshing ? '↻ Refreshing health…' : '↻ Refresh health'}
          </button>
          {typeof onRefresh === 'function' && (
            <button
              className="secondary"
              onClick={() => { onRefresh(); }}
              disabled={loading}
              aria-label="Refresh providers"
              title="Refresh"
            >
              ↻ Refresh
            </button>
          )}
          {typeof onCreate === 'function' && (
            <button
              className="primary"
              onClick={() => onCreate()}
              aria-label="Create a new upstream provider"
              title="Create a new upstream provider"
            >
              + New Provider
            </button>
          )}
        </div>

        {loading ? (
          <Spinner label="Loading upstream providers…" />
        ) : error ? (
          <ErrorBanner error={error} onRetry={onRetry} />
        ) : filtered.length === 0 ? (
          <EmptyState
            title={hasFilters ? 'No providers match the filters' : 'No upstream providers yet'}
            hint={hasFilters
              ? 'Try clearing the search, type, or health filters.'
              : 'Get started by creating a new provider or importing an existing OAuth credential.'}
            actions={hasFilters ? (
              <button onClick={() => { setSearch(''); setTypeFilter(''); setHealthFilters(new Set()); }}>
                Clear filters
              </button>
            ) : (
              <div className="row gap-sm">
                {typeof onCreate === 'function' && (
                  <button className="primary" onClick={() => onCreate()}>+ New Provider</button>
                )}
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
                <SortHeader k="health">Health</SortHeader>
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
                const liveEntry = liveStatus[String(p.id)];
                const healthStatus = statusFromHealth(p, liveEntry);
                const healthReason = cooldownReason(liveEntry?.cooldown_until) || liveEntry?.last_error || null;
                return (
                  <tr key={p.id}
                    className={`clickable-row${isSel ? ' row--selected' : ''}`}
                    onClick={() => navigate(`/upstream-providers/${encodeURIComponent(p.id)}`)}
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
                    <td>
                      <StatusDot status={healthStatus} reason={healthReason} size="sm" />
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
                          onClick={(e) => { e.stopPropagation(); onEdit ? onEdit(p) : navigate(`/upstream-providers/${encodeURIComponent(p.id)}`); }}>✎</button>
                        <button className="btn-icon" title="Delete" aria-label="Delete provider"
                          onClick={(e) => { e.stopPropagation(); onDelete ? onDelete(p) : setConfirmDelete(p); }}>✕</button>
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
            healthBreakdown={selectedHealthBreakdown}
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

      {confirmDelete && (
        <Modal title="Delete upstream provider?" size="sm"
          onClose={() => setConfirmDelete(null)}
          footer={<>
            <button onClick={() => setConfirmDelete(null)}>Cancel</button>
            <button className="danger" onClick={async () => {
              try {
                if (onDelete) {
                  await onDelete(confirmDelete);
                } else {
                  toast.error('No delete handler registered');
                }
              } catch (err) {
                toast.error(err?.message || 'Delete failed');
              } finally {
                setConfirmDelete(null);
              }
            }}>Delete</button>
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
            const targets = confirmBulkDelete;
            setConfirmBulkDelete(null);
            await runBulk('delete');
            void targets;
          }}
        />
      )}
    </>
  );
}

// ============================================================================
// Subcomponents (kept local; mirrors the layout in UpstreamProvidersPage.jsx)
// ============================================================================

// BulkActionBar — appears under the table when one or more rows are
// selected. Shows the live selection count, two breakdowns (provider_type
// for the existing behavior, health buckets for the new HealthSummary
// integration), and three destructive / state actions. Enable/Disable fire
// immediately (mirrors single-row toggle semantics — no confirmation
// step), Delete opens a separate confirm modal via the parent because it
// cannot be undone.
function BulkActionBar({ count, total, selected, running, healthBreakdown, onEnable, onDisable, onDelete, onClear }) {
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
        {Array.isArray(healthBreakdown) && healthBreakdown.map(([k, n]) => (
          <span key={`h-${k}`} className="filter-chip" title={`${n} ${k}`}>
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

// PaginationBar — compact footer rendered under the table. Shows the
// visible row range, total count, current page, and a first/prev/next/last
// page navigator. Page-size selector uses common values (10/25/50/100).
// When the total fits on one page the prev/next buttons are disabled; the
// row-range text + page size still render so the operator sees the
// absolute total.
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

// BulkDeleteConfirmModal — destructive-confirm pattern for the bulk
// delete path. Shows a count, breakdown by provider_type, and a list of
// identifiers (capped to 12 rows, with a "+N more" suffix) so the operator
// can confirm the exact set they're about to nuke. Config.yaml / auth-dir
// re-render warning is repeated here (mirrors the single-row confirm)
// because the bulk path doesn't go through the single-row modal first.
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
// Local helpers (mirroring the originals in UpstreamProvidersPage.jsx)
// ============================================================================

function formatTime(iso) {
  try {
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return '—';
    return d.toLocaleString();
  } catch { return '—'; }
}

// formatRelativeTime returns a compact "5m ago / 2h ago / 3d ago" string
// for recent timestamps and falls back to a short date for older ones.
// Operators scanning the table care about "is this fresh" more than the
// exact time, so the relative form reads more clearly than a full locale
// timestamp. Mirrors the behavior in UpstreamProvidersPage.jsx.
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
