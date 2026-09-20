// ============================================================================
// Upstream provider editor — TabBar
// ============================================================================
//
// TabBar renders the horizontal section strip for the tabbed editor (PR 2).
// Tabs are filtered by provider type: hidden tabs (Entries, Quota, Test)
// appear only when the loaded row's type warrants them. The active tab is
// driven by the `:tab` URL segment (the route is nested under
// `/upstream-providers/:id/:tab`); the Overview tab is the default when
// the parent route resolves to no segment.
//
// Each NavLink targets an ABSOLUTE path built from the matched :id, not a
// relative one. `to="../models" relative="path"` stripped the id segment and
// emitted /upstream-providers/models: "path" relativity resolves against the
// ROUTE that matched — here `/upstream-providers/:id` — so ".." consumed :id
// instead of the tab segment. The absolute form keeps the link correct no
// matter which tab is active.

import React from 'react';
import { NavLink, useParams } from 'react-router-dom';
import { TESTABLE_TYPES } from './schemas.js';

const ALL_TABS = [
  { key: 'overview', label: 'Overview', alwaysShown: true },
  { key: 'models', label: 'Models', alwaysShown: true },
  { key: 'entries', label: 'Entries', condition: 'isEntryBearing' },
  { key: 'quota', label: 'Quota', condition: 'isOpencodeGo' },
  { key: 'test', label: 'Test', condition: 'isTestable' },
  { key: 'logs', label: 'Logs', alwaysShown: true },
];

export function TabBar({ providerType, isEntryBearing }) {
  const { id } = useParams();
  const isOpencodeGo = providerType === 'opencode-go';
  // OAuth providers are excluded by definition: the server's
  // /upstream-providers/:id/test endpoint expects a real entry credential,
  // and oauth:* rows only have an auth file the registry tracks.
  const isTestable = !providerType?.startsWith('oauth:') && TESTABLE_TYPES.includes(providerType);

  const visibleTabs = ALL_TABS.filter((t) => {
    if (t.alwaysShown) return true;
    if (t.condition === 'isEntryBearing') return isEntryBearing;
    if (t.condition === 'isOpencodeGo') return isOpencodeGo;
    if (t.condition === 'isTestable') return isTestable;
    return true;
  });

  return (
    <nav aria-label="Provider sections" className="upstream-editor__tabs">
      {visibleTabs.map((t) => (
        <NavLink
          key={t.key}
          to={`/upstream-providers/${id}/${t.key}`}
          className={({ isActive }) => `upstream-editor__tab ${isActive ? 'upstream-editor__tab--active' : ''}`}
        >
          {t.label}
        </NavLink>
      ))}
    </nav>
  );
}

export default TabBar;