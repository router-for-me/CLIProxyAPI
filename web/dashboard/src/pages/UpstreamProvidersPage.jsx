// UpstreamProvidersPage — thin shell that loads the providers list +
// live status, owns the page-level CRUD callbacks (single-row delete, bulk
// actions, import modal), and delegates rendering to:
//   - Table.jsx              (catalog: toolbar, HealthSummary, table, bulk
//                             action bar, pagination, delete/bulk-delete
//                             confirm modals). Reads ?health= for deep
//                             links from HealthPage.
//   - AliasesCard.jsx        (Global OAuth Model Aliases editor at the
//                             bottom of the page).
//
// Task 17 (PR1 upstream-health) replaces the previous monolithic inline
// rendering. All state local to the table view (search, type filter,
// health filter, sort, page, selection) now lives inside Table.jsx; the
// parent only owns the cross-cutting bulk-action wiring (Promise.allSettled
// over selected rows + summary toast + selection narrowing + reload), per
// Table.jsx's onBulkAction prop contract.

import React, { useState, useMemo, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  listUpstreamProviders,
  updateUpstreamProvider,
  deleteUpstreamProvider,
  listUpstreamProviderLiveStatus,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useToast } from '../components/Toast.jsx';
import {
  TYPE_LABEL,
} from './upstream-provider-editor/schemas.js';
import { coerceLiveStatusResponse } from '../api/liveStatus.js';
import Table from './upstream-providers/Table.jsx';
import AliasesCard from './upstream-providers/AliasesCard.jsx';
import ImportOAuthProviderModal from './upstream-providers/ImportModal.jsx';

// ============================================================================
// Page
// ============================================================================

export default function UpstreamProvidersPage() {
  const toast = useToast();
  const navigate = useNavigate();
  const { data, error, loading, reload } = useAsync(() => listUpstreamProviders(), []);
  const [liveStatus, setLiveStatus] = useState({});
  const [refreshing, setRefreshing] = useState(false);
  // Import modal. Kept local to the page since it spans multiple providers
  // (Tab 1: existing auth files, Tab 2: pasted JSON, Tab 3: file upload) and
  // re-uses the createUpstreamProvider API directly. Lives outside the table
  // because it's a one-shot creation flow, not a row operation.
  const [importing, setImporting] = useState(false);

  // Refresh the per-row live-status map (breaker / cooldown / is_live). Shown
  // by Table.jsx's Health column. On any error we degrade to {} so the table
  // renders every row as "Stale" rather than blowing up the toolbar.
  const refreshHealth = useCallback(async () => {
    setRefreshing(true);
    try {
      const json = await listUpstreamProviderLiveStatus();
      setLiveStatus(coerceLiveStatusResponse(json));
    } catch {
      setLiveStatus({});
    } finally {
      setRefreshing(false);
    }
  }, []);

  const providers = data?.providers || [];

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

  // Single-row delete. Invoked by Table.jsx's row Edit/Delete actions +
  // confirm modal. Mirrors the original handleDelete's behavior: success
  // toast + reload; error toast.
  const handleDelete = useCallback(async (p) => {
    try {
      await deleteUpstreamProvider(p.id);
      toast.success(`Deleted ${TYPE_LABEL[p.provider_type] || p.provider_type}`);
      reload();
    } catch (err) {
      toast.error(err.message || 'Delete failed');
    }
  }, [toast, reload]);

  // runBulk performs a single bulk action ("enable" | "disable" | "delete")
  // over the targets Table.jsx hands us via onBulkAction. Runs the per-row
  // mutations in parallel via Promise.allSettled so a single 4xx/5xx
  // doesn't block the rest. Reports a final summary toast
  // (`X succeeded, Y failed`). Table.jsx owns the progress toast +
  // bulkRunning flag + the selectedIds Set; we just mutate the API.
  //
  // The selection-narrowing (drop succeeded IDs on partial failure) and the
  // post-action reload() are owned here per Task 12's onBulkAction contract.
  // We can't narrow Table's selectedIds directly — Table exposes no setter
  // — so the parent relies on the reload above to garbage-collect stale IDs
  // (Table already drops unknown IDs from its selection Set on providers
  // list changes).
  const handleBulkAction = useCallback(async ({ action, targets }) => {
    if (!Array.isArray(targets) || targets.length === 0) return;
    const verbs = { enable: 'enable', disable: 'disable', delete: 'delete' };
    const tasks = targets.map((p) => {
      if (action === 'delete') return deleteUpstreamProvider(p.id).then(() => p);
      // Enable / Disable: PUT with the smallest payload possible so we don't
      // clobber fields the operator didn't intend to change. The server only
      // cares about `disabled` here.
      return updateUpstreamProvider(p.id, { disabled: action === 'disable' }).then(() => p);
    });
    const settled = await Promise.allSettled(tasks);
    const ok = [];
    const failed = [];
    settled.forEach((r, i) => {
      if (r.status === 'fulfilled') ok.push(targets[i]);
      else failed.push({ p: targets[i], err: r.reason });
    });
    if (failed.length === 0) {
      toast.success(
        `${capitalize(verbs[action])}d ${ok.length} provider${ok.length === 1 ? '' : 's'}`,
      );
    } else if (ok.length > 0) {
      toast.error(
        `${verbs[action]}d ${ok.length}, ${failed.length} failed: ${failed[0].err?.message || 'unknown error'}` +
          (failed.length > 1 ? ` (+${failed.length - 1} more)` : ''),
        { duration: 7000 },
      );
    } else {
      toast.error(
        `All ${failed.length} ${verbs[action]} calls failed: ${failed[0].err?.message || 'unknown error'}`,
        { duration: 7000 },
      );
      // Bubble up so Table.jsx can re-enable the toolbar (runBulk's
      // `setBulkRunning(false)` runs in `finally` regardless).
      throw failed[0].err;
    }
    // Reload after the action settles so the table reflects server truth
    // (the garbage-collect effect inside Table drops stale selection IDs).
    reload();
  }, [toast, reload]);

  return (
    <div className="main">
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
          <button className="primary" onClick={() => navigate('/upstream-providers/new')}>+ New Provider</button>
        </div>
      </div>

      <Table
        providers={providers}
        liveStatus={liveStatus}
        refreshing={refreshing}
        loading={loading}
        error={error}
        onRetry={reload}
        onRefreshHealth={refreshHealth}
        onRefresh={reload}
        onCreate={() => navigate('/upstream-providers/new')}
        onEdit={(p) => navigate(`/upstream-providers/${encodeURIComponent(p.id)}`)}
        onDelete={handleDelete}
        onBulkAction={handleBulkAction}
      />

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

      <AliasesCard />
    </div>
  );
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
