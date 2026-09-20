// ============================================================================
// Upstream provider editor — route shell
// ============================================================================
//
// UpstreamProviderEditorPage is the route element mounted by App.jsx for
// /upstream-providers/:id (tab segments are child routes). The page owns:
//   - The async load of the provider row (edit mode)
//   - The EditorStateProvider context (form state + handlers lifted from
//     the previous ProviderEditorForm, see ./useEditorState.jsx)
//   - The sticky header (Back / title / dirty badge / Save button)
//   - The TabBar (filtered by provider type)
//   - An <Outlet /> where the active tab component mounts
//
// Tab components (OverviewTab / ModelsTab / EntriesTab / QuotaTab /
// TestTab / LogsTab) mount as child routes of /upstream-providers/:id via
// <Outlet />.

import React from 'react';
import { Outlet, useNavigate, useParams } from 'react-router-dom';
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
    <div className="main upstream-editor">
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
          className="primary"
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