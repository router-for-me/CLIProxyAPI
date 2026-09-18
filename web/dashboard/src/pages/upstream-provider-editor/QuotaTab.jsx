// ============================================================================
// Upstream provider editor — Quota tab
// ============================================================================
//
// Verbatim lift of the opencode-go quota/seed/refresh panel (OpenCodeGoPanel /
// OpenCodeGoActions) into its own tab. The panel used to live inline at the
// bottom of the previous monolithic ProviderEditorForm (and is still rendered
// there in create mode and — temporarily — in OverviewTab until PR 3's
// MODELS_TAB flag flip). For PR 2 the panel gains a parallel route
// `/upstream-providers/:id/quota` so operators can reach it from the tab bar.
//
// This wrapper just wires the route param (`providerId` from useParams) and
// the editor state (`form` from useEditorState) into the panel's original
// prop contract — no behavior changes to the panel itself.

import React, { useCallback } from 'react';
import { useParams } from 'react-router-dom';
import OpenCodeGoActions from './OpenCodeGoPanel.jsx';
import { useEditorState } from './useEditorState.jsx';
import { getUpstreamProvider } from '../../api/client.js';
import { buildForm } from './form.js';

export function QuotaTab() {
  const { id: providerId } = useParams();
  const { state, setState, providerType } = useEditorState();

  // Provider object the panel expects: only `id` is read, so a stub with the
  // route param is sufficient. Keeps the panel's prop contract verbatim.
  const provider = { id: providerId, provider_type: providerType };

  // onCatalogChanged mirrors CreateMode's existing callback: refetch the
  // row and rebuild the form so the inline model list reflects whatever
  // the server just seeded/refreshed.
  const onCatalogChanged = useCallback(() => {
    if (!providerId) return;
    getUpstreamProvider(providerId)
      .then((row) => {
        setState(buildForm(providerType, row));
      })
      .catch(() => { /* keep the stale form; save still works */ });
  }, [providerId, providerType, setState]);

  return (
    <OpenCodeGoActions
      provider={provider}
      form={state}
      onCatalogChanged={onCatalogChanged}
    />
  );
}

export default QuotaTab;