// ============================================================================
// Upstream provider editor — route shell
// ============================================================================
//
// UpstreamProviderEditorPage is the route element mounted by App.jsx for
// /upstream-providers/:id/:tab. The page owns:
//   - The async load of the provider row (edit mode)
//   - The EditorStateProvider context (form state + handlers lifted from
//     the previous ProviderEditorForm, see ./useEditorState.jsx)
//   - The sticky header (Back / title / dirty badge / Save button)
//   - The TabBar (filtered by provider type)
//   - An <Outlet /> where the active tab component mounts
//
// Tab components (OverviewTab / ModelsTab / EntriesTab / QuotaTab /
// TestTab / LogsTab) are added in Tasks 3-7; until then the <Outlet />
// simply renders nothing, which matches the plan's "build fails only on
// missing tab imports" contract (the parent route ships first, the child
// routes ship incrementally).

import React from 'react';
import { Navigate, Outlet, useNavigate, useParams } from 'react-router-dom';
import { useAsync } from '../../hooks/useAsync.js';
import { getUpstreamProvider } from '../../api/client.js';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';
import { EditorStateProvider, useEditorState } from './useEditorState.jsx';
import { TabBar } from './TabBar.jsx';

// resolveEditorMode classifies the :id route param. 'new' means create;
// anything else is a numeric PG row id. Exported as a pure helper so the
// mode contract is testable without a DOM, and so the create-mode page
// shell (UpstreamProviderEditorPage) can decide whether to mount the type
// picker or the form body.
//
// Kept here as a backwards-compatible export for the index.test.js suite
// shipped with PR 1. The new shell uses React Router's matched :id param
// directly, so the helper is no longer invoked at runtime — but the test
// contract (the exact "new" / "0" / undefined branches) is still load-
// bearing as documentation of the URL contract.
export function resolveEditorMode(id) {
  const isCreate = id === 'new' || id === undefined;
  return { isCreate, providerId: isCreate ? null : id };
}

// detailRedirectTarget builds the /overview target for the legacy detail
// URL. A function (not a static string) because React Router 6 does NOT
// interpolate route params inside <Navigate to="..."> — a static
// "/upstream-providers/:id/overview" sends the browser to a literal ":id"
// path, which the tabbed route then matches as id=":id" and the API
// rejects with "id must be a positive integer".
export function detailRedirectTarget(id) {
  return `/upstream-providers/${id}/overview`;
}

// UpstreamDetailRedirect is the route element for /upstream-providers/:id
// (no tab segment): it forwards old bookmarks to the Overview tab while
// preserving the real provider id.
export function UpstreamDetailRedirect() {
  const { id } = useParams();
  return <Navigate to={detailRedirectTarget(id)} replace />;
}

export function UpstreamProviderEditorPage() {
  const { id } = useParams();
  const navigate = useNavigate();
  const providerAsync = useAsync(() => getUpstreamProvider(id), [id]);

  if (providerAsync.loading) {
    return (
      <div className="main">
        <Spinner label="Loading provider…" />
      </div>
    );
  }
  if (providerAsync.error) {
    return (
      <div className="main">
        <ErrorBanner error={providerAsync.error} onRetry={providerAsync.reload} />
        <div className="row gap-sm" style={{ marginTop: 12 }}>
          <button type="button" onClick={() => navigate('/upstream-providers')}>
            ← Back to providers
          </button>
        </div>
      </div>
    );
  }

  return (
    <EditorStateProvider initial={providerAsync.data}>
      <EditorShell />
    </EditorStateProvider>
  );
}

function EditorShell() {
  const navigate = useNavigate();
  const {
    state,
    providerType,
    isEntryBearing,
    dirty,
    saving,
    savingError,
    save,
  } = useEditorState();

  const title = state?.name || state?.label || state?.email || state?.file_name || 'Untitled';

  return (
    <div className="main">
      <header className="upstream-editor__header">
        <button
          type="button"
          onClick={() => navigate('/upstream-providers')}
          className="upstream-editor__back"
        >
          ← Back
        </button>
        <div className="upstream-editor__header-main">
          <h1 className="upstream-editor__title">{title}</h1>
          <span className="badge">{providerType}</span>
          {dirty && (
            <span className="upstream-editor__dirty" aria-live="polite">
              ● unsaved changes
            </span>
          )}
          {savingError && (
            <span className="error-pill" role="alert">
              {savingError}
            </span>
          )}
        </div>
        <button
          type="button"
          onClick={save}
          disabled={saving}
          className="btn btn-primary"
        >
          {saving ? 'Saving…' : 'Save'}
        </button>
      </header>
      <TabBar providerType={providerType} isEntryBearing={isEntryBearing} />
      <Outlet />
    </div>
  );
}

export default UpstreamProviderEditorPage;