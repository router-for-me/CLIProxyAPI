import React, { useState, useEffect, useRef, useMemo } from 'react';
import { listModelsCatalog, ApiError } from '../api/client.js';

// ModelIdCombobox — single-select searchable combobox for picking a model id
// from the live/global catalog. Used by the ModelRouteEntryModal "Add Model"
// screen so operators can find a model id by typing instead of guessing it.
//
// Mirrors the lazily-loaded + searchable UX of ModelMultiSelect but resolves a
// single string value into the parent's `onChange(modelId)` rather than a chip
// array:
//   - lazily loads available models from /v0/management/models-catalog
//     (distinctIds dedupes to one row per model id),
//   - searches model id / provider / display name while typing,
//   - clicking a row (or Enter on an exact match) selects that id,
//   - Enter on a query that matches nothing adds it as a custom entry so
//     operators can still grant ids the live catalog doesn't enumerate yet,
//   - degrades gracefully if the catalog endpoint is unreachable.
//
// Rows already on the group list (`existingModels`) are shown muted and
// cannot be re-selected.

const MAX_MODELS_LOAD = 1000; // safety cap to avoid unbounded paging
const PAGE_SIZE = 200;

export default function ModelIdCombobox({
  value,
  onChange,
  existingModels = [],
  placeholder = 'search models…',
  autoFocus = false,
}) {
  const [models, setModels] = useState([]);     // [{id, provider, officialProvider, displayName}]
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(null);
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);
  // Position/layout of the fixed-position dropdown. `openUp` flips the menu
  // above the input when there isn't enough room below (e.g. near a modal's
  // bottom edge); null while closed.
  const [anchor, setAnchor] = useState(null);

  const rootRef = useRef(null);
  const inputRef = useRef(null);
  const dropdownRef = useRef(null);

  const existingSet = useMemo(() => new Set(existingModels), [existingModels]);

  // Fetch available models lazily on mount. Page through the catalog.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      setLoadError(null);
      try {
        const acc = [];
        let page = 1;
        while (acc.length < MAX_MODELS_LOAD) {
          const res = await listModelsCatalog({ page, pageSize: PAGE_SIZE, availableOnly: true, distinctIds: true });
          const rows = Array.isArray(res?.models) ? res.models : [];
          for (const r of rows) {
            const id = r?.id || r?.name;
            if (!id) continue;
            acc.push({
              id,
              provider: r?.provider || '',
              officialProvider: r?.official_provider || r?.officialProvider || '',
              displayName: r?.display_name || r?.displayName || '',
            });
          }
          const totalPages = Number(res?.total_pages) || 1;
          if (page >= totalPages || rows.length === 0) break;
          page += 1;
        }
        if (!cancelled) setModels(acc);
      } catch (err) {
        if (!cancelled) setLoadError(err instanceof ApiError ? err.message : 'Could not load available models.');
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  // Close the dropdown on outside click / Escape.
  useEffect(() => {
    if (!open) return undefined;
    function onDocClick(e) {
      if (rootRef.current && !rootRef.current.contains(e.target)) setOpen(false);
    }
    function onKey(e) {
      if (e.key === 'Escape') setOpen(false);
    }
    function onScroll(e) {
      // Reposition after any scroll (modal body scrolls, window resize) so the
      // dropdown stays glued to the input.
      if (rootRef.current && e.target.closest?.('.model-id-combobox')) return;
      positionDropdown();
    }
    document.addEventListener('mousedown', onDocClick);
    document.addEventListener('keydown', onKey);
    document.addEventListener('scroll', onScroll, true);
    return () => {
      document.removeEventListener('mousedown', onDocClick);
      document.removeEventListener('keydown', onKey);
      document.removeEventListener('scroll', onScroll, true);
    };
  }, [open]);

  // Measure the input against the viewport every time the dropdown renders so
  // it opens with the right size and direction (up vs down).
  useEffect(() => {
    positionDropdown();
  });

  function positionDropdown() {
    const input = inputRef.current;
    if (!input || !open) { setAnchor(null); return; }
    const r = input.getBoundingClientRect();
    const GAP = 6;
    const PAD = 8;
    const spaceBelow = window.innerHeight - r.bottom - PAD;
    const spaceAbove = r.top - PAD;
    // Open upward when there's clearly more room above, otherwise downward.
    const openUp = spaceAbove > spaceBelow && spaceBelow < 260;
    const maxHeight = Math.max(120, (openUp ? spaceAbove : spaceBelow) - GAP);
    const next = {
      left: r.left,
      top: openUp ? r.top - GAP : r.bottom + GAP,
      width: r.width,
      maxHeight,
    };
    // Bail if nothing changed so we don't re-render in an infinite loop.
    setAnchor((prev) => (
      prev
      && Math.abs(prev.left - next.left) < 0.5
      && Math.abs(prev.top - next.top) < 0.5
      && Math.abs(prev.width - next.width) < 0.5
      && Math.abs(prev.maxHeight - next.maxHeight) < 0.5
        ? prev
        : next
    ));
  }

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return models.slice(0, 50);
    return models
      .filter((m) =>
        m.id.toLowerCase().includes(q) ||
        (m.officialProvider || '').toLowerCase().includes(q) ||
        m.provider.toLowerCase().includes(q) ||
        (m.displayName || '').toLowerCase().includes(q))
      .slice(0, 50);
  }, [models, query]);

  useEffect(() => { setHighlight(0); }, [query, open]);

  // Select a concrete model id (or custom token) and hand it to the parent.
  function select(token) {
    const t = (token || '').trim();
    if (!t) return;
    onChange(t);
    setQuery('');
    setOpen(false);
  }

  function handleKeyDown(e) {
    if (e.key === 'Enter') {
      e.preventDefault();
      const q = query.trim();
      if (!q) return;
      const exact = models.find((m) => m.id === q && !existingSet.has(m.id));
      if (exact) select(exact.id);
      else select(q); // custom entry not enumerated by the live catalog
      return;
    }
    if (e.key === 'ArrowDown' && open) {
      e.preventDefault();
      setHighlight((h) => Math.min(h + 1, Math.max(filtered.length - 1, 0)));
      return;
    }
    if (e.key === 'ArrowUp' && open) {
      e.preventDefault();
      setHighlight((h) => Math.max(h - 1, 0));
      return;
    }
  }

  return (
    <div
      className={`model-multiselect model-id-combobox ${open ? 'is-open' : ''}`}
      ref={rootRef}
      onClick={() => inputRef.current?.focus()}
    >
      <div className="model-multiselect__chips">
        <input
          ref={inputRef}
          className="model-multiselect__input"
          value={query}
          placeholder={value ? value : placeholder}
          autoFocus={autoFocus}
          onChange={(e) => { setQuery(e.target.value); setOpen(true); }}
          onFocus={() => setOpen(true)}
          onKeyDown={handleKeyDown}
        />
      </div>

      {open && anchor && (
        <div
          ref={dropdownRef}
          className="model-multiselect__dropdown model-id-combobox__dropdown"
          style={{
            position: 'fixed',
            left: anchor.left,
            top: anchor.top,
            width: anchor.width,
            maxHeight: anchor.maxHeight,
          }}
        >
          {loading && <div className="model-multiselect__status model-multiselect__status--loading">Loading available models…</div>}
          {!loading && loadError && (
            <div className="model-multiselect__status model-multiselect__status--error">
              {loadError} — you can still type a custom model id and press Enter.
            </div>
          )}
          {!loading && !loadError && filtered.length === 0 && (
            <div className="model-multiselect__status">
              No models match “{query}”. Press Enter to add it as a custom entry.
            </div>
          )}
          {!loading && filtered.length > 0 && (
            <ul className="model-multiselect__list">
              {filtered.map((m, i) => {
                const isOnList = existingSet.has(m.id);
                return (
                  <li
                    key={`${m.provider}:${m.id}`}
                    className={`model-multiselect__option ${isOnList ? 'is-selected' : ''} ${i === highlight ? 'is-highlight' : ''}`}
                    onMouseDown={(e) => {
                      e.preventDefault();
                      if (isOnList) return;
                      select(m.id);
                    }}
                    onMouseEnter={() => setHighlight(i)}
                  >
                    <span className="model-multiselect__option-id mono">{m.id}</span>
                    {m.officialProvider && <span className="model-multiselect__option-provider" title="Official provider">{m.officialProvider}</span>}
                    <span className="model-multiselect__option-check">
                      {isOnList ? 'on list' : '\u00a0'}
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
    </div>
  );
}
