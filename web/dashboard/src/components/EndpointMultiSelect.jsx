import React, { useState, useEffect, useRef, useMemo } from 'react';
import { buildEndpointGroups } from '../api/managementEndpoints.js';

// EndpointMultiSelect — combobox for picking management REST endpoint
// signatures (e.g. "GET /usage-stats/*") for a token's allowed/blocked
// endpoint policy lists.
//
// Selected values are plain strings ("<METHOD> <path>"). The parent owns the
// array; we call onChange with a fresh array on every add/remove.
//
// The option set is a static, grouped catalog of every registered
// /v0/management route (see managementEndpoints.js) — no network fetch. The
// combobox also accepts free-text custom entries (Enter) so an operator can
// type a glob the catalog doesn't enumerate (e.g. a future route).
//
// Mirrors the styling/UX of ModelMultiSelect but with a grouped dropdown.
export default function EndpointMultiSelect({
  value = [],
  onChange,
  label,
  placeholder = 'search endpoints…',
  hint,
}) {
  const groups = useMemo(() => buildEndpointGroups(), []);
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState(false);
  const [highlight, setHighlight] = useState(0);

  const rootRef = useRef(null);
  const inputRef = useRef(null);

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

  // Filtered, flattened set of options matching the query. Each item carries
  // its group label so the dropdown can render group headers.
  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    const out = [];
    for (const g of groups) {
      for (const e of g.entries) {
        if (!q || e.value.toLowerCase().includes(q)) {
          out.push({ ...e, group: g.label });
        }
      }
    }
    return out.slice(0, 80); // cap for render perf
  }, [groups, query]);

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
      const exact = filtered.find((m) => m.value === q);
      if (exact) add(exact.value);
      else add(q); // custom signature
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

  // Group filtered items by their group label for rendering headers.
  const groupedFiltered = useMemo(() => {
    const map = new Map();
    for (const item of filtered) {
      if (!map.has(item.group)) map.set(item.group, []);
      map.get(item.group).push(item);
    }
    return Array.from(map.entries());
  }, [filtered]);

  return (
    <div className="form__row">
      {label && <label className="form__label">{label}</label>}
      <div
        className={`endpoint-multiselect ${open ? 'is-open' : ''}`}
        ref={rootRef}
        onClick={() => inputRef.current?.focus()}
      >
        <div className="endpoint-multiselect__chips">
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
            className="endpoint-multiselect__input"
            value={query}
            placeholder={value.length === 0 ? placeholder : ''}
            onChange={(e) => { setQuery(e.target.value); setOpen(true); }}
            onFocus={() => setOpen(true)}
            onKeyDown={handleKeyDown}
          />
        </div>

        {open && (
          <div className="endpoint-multiselect__dropdown">
            {filtered.length === 0 && (
              <div className="endpoint-multiselect__status">
                No endpoints match “{query}”. Press Enter to add it as a custom entry.
              </div>
            )}
            {groupedFiltered.map(([groupLabel, items]) => (
              <div key={groupLabel} className="endpoint-multiselect__group">
                <div className="endpoint-multiselect__group-label">{groupLabel}</div>
                <ul className="endpoint-multiselect__list">
                  {items.map((item, i) => {
                    const flatIndex = filtered.indexOf(item);
                    const isSelected = selectedSet.has(item.value);
                    return (
                      <li
                        key={item.value}
                        className={`endpoint-multiselect__option ${isSelected ? 'is-selected' : ''} ${flatIndex === highlight ? 'is-highlight' : ''}`}
                        onMouseDown={(e) => {
                          e.preventDefault();
                          if (!isSelected) add(item.value);
                          setQuery('');
                          inputRef.current?.focus();
                        }}
                        onMouseEnter={() => setHighlight(flatIndex)}
                      >
                        <span className={`endpoint-multiselect__method endpoint-multiselect__method--${item.method.toLowerCase()}`}>{item.method}</span>
                        <span className="endpoint-multiselect__option-path mono">{item.path}</span>
                        {item.glob && <span className="endpoint-multiselect__option-glob" title="subtree glob">★</span>}
                        {isSelected && <span className="endpoint-multiselect__option-check">✓</span>}
                      </li>
                    );
                  })}
                </ul>
              </div>
            ))}
          </div>
        )}
      </div>
      {hint && <div className="form__hint">{hint}</div>}
    </div>
  );
}
