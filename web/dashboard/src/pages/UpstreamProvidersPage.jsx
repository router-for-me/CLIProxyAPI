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

import React, { useState, useMemo, useEffect, useCallback } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  listUpstreamProviders,
  createUpstreamProvider,
  updateUpstreamProvider,
  deleteUpstreamProvider,
  listAuthFiles,
  getAuthFileModels,
  fetchAuthFileJSON,
  uploadAuthFileRaw,
  listUpstreamProviderLiveStatus,
} from '../api/client.js';
import { ApiError } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, EmptyState, Modal } from '../components/Primitives.jsx';
import { useToast } from '../components/Toast.jsx';
import { Field } from './manage-cpa/FormPrimitives.jsx';
import {
  TYPE_LABEL,
} from './upstream-provider-editor/schemas.js';
import { coerceLiveStatusResponse } from '../api/liveStatus.js';
import Table from './upstream-providers/Table.jsx';
import AliasesCard from './upstream-providers/AliasesCard.jsx';

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
// ImportOAuthProviderModal — create an upstream provider from existing/pasted
// OAuth material (auth files already on disk, pasted token JSON, or uploaded
// JSON files). Complements the editor page's OAuth connect flow (which
// performs a live browser login). All three tabs share extractAuthFields() +
// buildOAuthCreatePayload() below so token/cloak/model extraction is
// consistent with the connect flow.
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
// Mirrors the merging logic in OAuthConnectSection.pollForAuthFile and the
// editor page's onCompleted merger (upstream-provider-editor/) so a row
// created via Import carries the same token/cloak/model metadata as one
// created via the live connect flow.
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
// expected by POST /upstream-providers (mirrors the editor page's buildPayload
// in upstream-provider-editor/form.js for the `oauth` + `claude` branches).
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
  const fileInputRef = React.useRef(null);

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

// strVal safely extracts a trimmed string from an object by key. Returns ''
// for missing/null/non-string values.
function strVal(obj, key) {
  if (!obj || typeof obj !== 'object') return '';
  const v = obj[key];
  if (typeof v === 'string') return v.trim();
  if (typeof v === 'number') return String(v);
  return '';
}
