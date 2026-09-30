// Model picker for the playground. Lists models per upstream provider so an
// operator can see what each provider exposes and test using the alias (the
// id a client sends). Catalog-only models remain available in a fallback
// group. Props: { value, onChange, cooldownProviders }.

import React, { useEffect, useMemo, useRef, useState } from 'react';
import { listModelsCatalog, listUpstreamProviders, listUpstreamProviderLiveStatus } from '../api/client.js';
import { coerceLiveStatusResponse } from '../api/liveStatus.js';
import { unwrapList } from './modelPickerFilters.js';
import {
  buildProviderGroups,
  buildCatalogGroups,
  collectSendIds,
  filterGroups,
} from './providerModels.js';

export default function ModelPicker({ value, onChange, cooldownProviders = null }) {
  const [catalog, setCatalog] = useState([]);
  const [providers, setProviders] = useState([]);
  const [liveStatus, setLiveStatus] = useState({});
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState('');
  const listRef = useRef(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [c, p, l] = await Promise.all([
          listModelsCatalog({ pageSize: 500, scope: 'all' }).catch(() => []),
          listUpstreamProviders({ providerType: '' }).catch(() => []),
          listUpstreamProviderLiveStatus().catch(() => null),
        ]);
        if (cancelled) return;
        setCatalog(unwrapList(c));
        setProviders(unwrapList(p));
        setLiveStatus(coerceLiveStatusResponse(l));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  const groups = useMemo(() => {
    const upstream = buildProviderGroups(providers, { cooldownProviders, liveStatus });
    const catalogExtra = buildCatalogGroups(catalog, collectSendIds(upstream));
    return [...upstream, ...catalogExtra];
  }, [providers, catalog, cooldownProviders, liveStatus]);

  const filtered = useMemo(() => filterGroups(groups, query), [groups, query]);
  const totalModels = useMemo(
    () => filtered.reduce((n, g) => n + g.models.length, 0),
    [filtered],
  );

  // Arrow keys move focus between model rows; Enter/Space are handled by the
  // native button. Home/End jump to the ends.
  function onListKeyDown(e) {
    const keys = ['ArrowDown', 'ArrowUp', 'Home', 'End'];
    if (!keys.includes(e.key)) return;
    const options = Array.from(listRef.current?.querySelectorAll('[role="option"]') || []);
    if (options.length === 0) return;
    e.preventDefault();
    const current = options.indexOf(document.activeElement);
    let next = current;
    if (e.key === 'ArrowDown') next = current < 0 ? 0 : Math.min(current + 1, options.length - 1);
    if (e.key === 'ArrowUp') next = current < 0 ? 0 : Math.max(current - 1, 0);
    if (e.key === 'Home') next = 0;
    if (e.key === 'End') next = options.length - 1;
    options[next]?.focus();
  }

  return (
    <div className="playground-picker">
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Search providers, models, aliases…"
        aria-label="Filter models"
        className="playground-picker__input"
      />
      {loading ? (
        <div className="playground-picker__message">Loading models…</div>
      ) : totalModels === 0 ? (
        <div className="playground-picker__message">{query ? 'No models match' : 'No models available'}</div>
      ) : (
        <div
          role="listbox"
          aria-label="Models"
          className="playground-picker__list"
          ref={listRef}
          onKeyDown={onListKeyDown}
        >
          {filtered.map((group) => (
            <div key={group.id} className="playground-picker__group">
              <div className="playground-picker__group-header">
                <span className="playground-picker__group-title" title={group.providerKey || group.title}>
                  {group.title}
                </span>
                {group.type && group.type !== 'catalog' && (
                  <span className="playground-picker__group-type">{group.type}</span>
                )}
                {group.cooldown ? (
                  <span className="playground-picker-row__badge is-cooldown">COOLDOWN</span>
                ) : group.live ? (
                  <span className="playground-picker-row__badge is-live">LIVE</span>
                ) : null}
              </div>
              {group.models.map((row) => {
                const isSelected = value && row.sendId === value;
                const mapping = row.isAlias ? `→ ${row.name}` : '';
                return (
                  <button
                    key={`${group.id}:${row.sendId}`}
                    type="button"
                    role="option"
                    aria-selected={isSelected}
                    className={`playground-picker-row${isSelected ? ' is-selected' : ''}`}
                    onClick={() => onChange(row.sendId)}
                    title={row.isAlias ? `${row.sendId} → ${row.name}` : row.sendId}
                  >
                    <span className="playground-picker-row__model">
                      {row.sendId}
                      {mapping && <span className="playground-picker-row__mapping">{mapping}</span>}
                    </span>
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
