// ============================================================================
// Upstream provider editor — Test tab
// ============================================================================
//
// Verbatim lift of the API-key test panel (TestPanel.jsx) into its own tab.
// The panel used to live inline at the bottom of the previous monolithic
// ProviderEditorForm (./index.jsx, and is still rendered there in create
// mode + Overview until PR 3 lifts the create-mode body too). For PR 2 the
// panel gains a parallel route `/upstream-providers/:id/test` so operators
// can reach it from the tab bar without scrolling through the form.
//
// This wrapper just wires the route param (`providerId` from useParams) and
// the editor state (`form` from useEditorState) into the panel's original
// prop contract — no behavior changes to the panel itself.
//
// Live status: TestPanel never had a live-status display, so we surface one
// here for the row itself (the liveStatus map is keyed by String(provider
// id); see ../api/liveStatus.js + useEditorState.jsx). Per-entry live
// evidence would require mapping each entry to its compound provider_key,
// which the editor does not expose today — same pragmatic fallback
// EntriesTab uses. Falls back to "—" when no entry is present in the map.

import React from 'react';
import { useParams } from 'react-router-dom';
import TestPanel from './TestPanel.jsx';
import { StatusDot, statusFromRow } from '../upstream-providers/components/StatusDot.jsx';
import { useEditorState } from './useEditorState.jsx';

export function TestTab() {
  const { id: providerId } = useParams();
  const { state, providerType, liveStatus } = useEditorState();

  // Provider object the panel expects: only `id` and `provider_type` are
  // read. A stub built from the route param + context is sufficient and
  // keeps the panel's prop contract verbatim.
  const provider = { id: providerId, provider_type: providerType };

  const liveEntry = providerId ? liveStatus[String(providerId)] : null;
  const status = liveEntry
    ? statusFromRow({
        isLive: !!liveEntry.is_live,
        cooldownUntil: liveEntry.cooldown_until,
        breakerOpen: !!liveEntry.breaker_open,
      })
    : null;

  return (
    <>
      {providerId && (
        <div
          className="form-section"
          data-testid="test-tab-live-status"
          style={{ display: 'flex', alignItems: 'center', gap: 8 }}
        >
          <div className="form-section__title" style={{ margin: 0, marginRight: 4 }}>Live status</div>
          {status ? (
            <StatusDot status={status} reason={liveEntry?.reason} />
          ) : (
            <span className="dim" title="No live evidence yet">—</span>
          )}
        </div>
      )}
      <TestPanel provider={provider} form={state} />
    </>
  );
}

export default TestTab;
