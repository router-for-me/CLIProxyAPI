import React, { useState, useEffect, useRef, useMemo } from 'react';
import { listModelsCatalog, ApiError } from '../api/client.js';

// ModelMultiSelect — combobox for picking model IDs from the live catalog.
//
// Selected values are plain strings (model IDs or custom tokens, e.g.
// wildcards like `gpt-4*`). The parent owns the array; we call onChange
// with a fresh array on every add/remove.
//
// Why a custom component: the dashboard has no UI library, only plain
// <select>. A native <select multiple> is awkward for large model lists,
// so we build a small combobox that:
//   - lazily loads available models from /v0/management/models-catalog,
//   - lets the operator search/filter,
//   - permits free-text custom entries (for wildcards the catalog doesn't
//     enumerate) via Enter,
//   - degrades gracefully: if the catalog endpoint is unreachable we still
//     let the operator type custom tokens.
//
// Intentionally framework-free; mirrors the styling tokens used elsewhere
// in the dashboard (variables from global.css).

const MAX_MODELS_LOAD = 1000; // safety cap to avoid unbounded paging
const PAGE_SIZE = 200;

export default function ModelMultiSelect({
  value = [],
  onChange,
  label,
  placeholder = 'search models…',
  hint,
}) {
  const [models, setModels] = useState([]);     // [{id, provider, officialProvider, displayName}]
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(null);
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);

  const rootRef = useRef(null);
  const inputRef = useRef(null);

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
    document.addEventListener('mousedown', onDocClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  const selectedSet = useMemo(() => new Set(value), [value]);

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

  function add(token) {
    const t = (token || '').trim();
    if (!t) return;
    if (selectedSet.has(t)) return;
    onChange([...value, t]);
  }
  function remove(token) {
    onChange(value.filter((v) => v !== token));
  }

  function handleKeyDown(e) {
    if (e.key === 'Enter') {
      e.preventDefault();
      const q = query.trim();
      if (!q) return;
      const exact = models.find((m) => m.id === q);
      if (exact) add(exact.id);
      else add(q); // custom token (e.g. wildcard)
      setQuery('');
      return;
    }
    if (e.key === 'Backspace' && query === '' && value.length > 0) {
      remove(value[value.length - 1]);
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
    <div className="form__row">
      {label && <label className="form__label">{label}</label>}
      <div
        className={`model-multiselect ${open ? 'is-open' : ''}`}
        ref={rootRef}
        onClick={() => inputRef.current?.focus()}
      >
        <div className="model-multiselect__chips">
          {value.map((v) => (
            <span className="chip mono" key={v}>
              {v}
              <button
                type="button"
                className="chip__remove"
                onClick={(e) => { e.stopPropagation(); remove(v); }}
                aria-label={`remove ${v}`}
              >
                ×
              </button>
            </span>
          ))}
          <input
            ref={inputRef}
            className="model-multiselect__input"
            value={query}
            placeholder={value.length === 0 ? placeholder : ''}
            onChange={(e) => { setQuery(e.target.value); setOpen(true); }}
            onFocus={() => setOpen(true)}
            onKeyDown={handleKeyDown}
          />
        </div>

        {open && (
          <div className="model-multiselect__dropdown">
            {loading && <div className="model-multiselect__status model-multiselect__status--loading">Loading available models…</div>}
            {!loading && loadError && (
              <div className="model-multiselect__status model-multiselect__status--error">
                {loadError} — you can still type a custom model name and press Enter.
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
                  const isSelected = selectedSet.has(m.id);
                  return (
                    <li
                      key={`${m.provider}:${m.id}`}
                      className={`model-multiselect__option ${isSelected ? 'is-selected' : ''} ${i === highlight ? 'is-highlight' : ''}`}
                      onMouseDown={(e) => {
                        e.preventDefault();
                        if (!isSelected) add(m.id);
                        setQuery('');
                        inputRef.current?.focus();
                      }}
                      onMouseEnter={() => setHighlight(i)}
                    >
                      <span className="model-multiselect__option-id mono">{m.id}</span>
                      {m.officialProvider && <span className="model-multiselect__option-provider" title="Official provider">{m.officialProvider}</span>}
                      {isSelected && <span className="model-multiselect__option-check">✓</span>}
                    </li>
                  );
                })}
              </ul>
            )}
          </div>
        )}
      </div>
      {hint && <div className="form__hint">{hint}</div>}
    </div>
  );
}
