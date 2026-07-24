// Reusable form primitives for the Manage-CPA provider-key editor.
//
// These components are intentionally tiny and self-contained so the modal
// stays declarative. They emit no API calls of their own — the parent
// collects the final form state via the onChange callback and decides
// what to PUT/PATCH.

import React, { useState } from 'react';

// --- Field ----------------------------------------------------------------

// Field wraps a labeled <input> with a required-asterisk, optional help
// text, and inline error display. The error message is rendered below the
// input with the .has-error modifier on the field itself.
export function Field({
  label,
  hint,
  error,
  required,
  children,
  htmlFor,
}) {
  return (
    <div className="form__row">
      <label
        className={`form__label${required ? ' form__label--required' : ''}`}
        htmlFor={htmlFor}
      >
        {label}
      </label>
      {children}
      {hint && !error && <div className="form__hint">{hint}</div>}
      {error && <div className="form__error">{error}</div>}
    </div>
  );
}

// PasswordInput — text input with a small "show" toggle. Keeps the value
// the same; only flips type="text" / type="password".
export function PasswordInput({ id, value, onChange, placeholder, autoComplete = 'off', defaultShown = false }) {
  const [shown, setShown] = useState(!!defaultShown);
  return (
    <div className="password-input">
      <input
        id={id}
        type={shown ? 'text' : 'password'}
        value={value || ''}
        onChange={(e) => onChange(e.target.value)}
        placeholder={placeholder}
        autoComplete={autoComplete}
        spellCheck={false}
      />
      <button
        type="button"
        className="password-toggle"
        onClick={() => setShown((s) => !s)}
        tabIndex={-1}
      >
        {shown ? 'Hide' : 'Show'}
      </button>
    </div>
  );
}

// --- Toggle row -----------------------------------------------------------

// ToggleRow — label + help text on the left, switch on the right. Used
// for boolean fields (disabled, websockets, force-mapping, ...).
export function ToggleRow({ label, hint, checked, onChange, disabled }) {
  return (
    <label className={`toggle-row${disabled ? ' is-disabled' : ''}`}>
      <div>
        <div className="toggle-row__label">{label}</div>
        {hint && <div className="toggle-row__hint">{hint}</div>}
      </div>
      <span className="toggle-switch">
        <input
          type="checkbox"
          checked={!!checked}
          onChange={(e) => onChange(e.target.checked)}
          disabled={disabled}
        />
        <span className="toggle-switch__slider" />
      </span>
    </label>
  );
}

// --- List editors ---------------------------------------------------------

// ChipListEditor — small inline editor for arrays of strings (excluded
// models, sensitive words). Type into the input + Enter (or comma) to add
// a chip; click the X to remove. Renders nothing fancier than a row of
// chips + one text input below.
export function ChipListEditor({ values, onChange, placeholder, emptyHint }) {
  const [draft, setDraft] = useState('');

  function commit() {
    const next = draft.split(/[,\n]/).map((s) => s.trim()).filter(Boolean);
    if (next.length === 0) return;
    const merged = Array.from(new Set([...(values || []), ...next]));
    onChange(merged);
    setDraft('');
  }

  function remove(idx) {
    const next = (values || []).filter((_, i) => i !== idx);
    onChange(next);
  }

  function onKey(e) {
    if (e.key === 'Enter' || e.key === ',') {
      e.preventDefault();
      commit();
    } else if (e.key === 'Backspace' && !draft && (values || []).length > 0) {
      remove(values.length - 1);
    }
  }

  return (
    <div className="list-editor">
      {(values || []).length === 0 && (
        <div className="list-editor__empty">
          {emptyHint || 'Type a value, press Enter to add.'}
        </div>
      )}
      <div className="list-editor__row--chip">
        {(values || []).map((v, i) => (
          <span key={`${v}-${i}`} className="entry-chip">
            {v}
            <button
              type="button"
              className="entry-chip__remove"
              onClick={() => remove(i)}
              aria-label={`Remove ${v}`}
            >
              ×
            </button>
          </span>
        ))}
        <input
          className="chip-input"
          type="text"
          value={draft}
          onChange={(e) => setDraft(e.target.value)}
          onKeyDown={onKey}
          onBlur={commit}
          placeholder={placeholder}
        />
      </div>
    </div>
  );
}

// KeyValueEditor — small two-column table for {key: string, value: string}.
// Used for HTTP headers. Add row appends a blank row at the bottom; the
// last row's input onBlur auto-commits a new row so typing flows naturally.
export function KeyValueEditor({ rows, onChange, keyPlaceholder, valuePlaceholder }) {
  const safe = Array.isArray(rows) ? rows : [];
  function update(idx, patch) {
    const next = safe.map((r, i) => (i === idx ? { ...r, ...patch } : r));
    onChange(next);
  }
  function add() {
    onChange([...safe, { key: '', value: '' }]);
  }
  function remove(idx) {
    onChange(safe.filter((_, i) => i !== idx));
  }
  function autoAddOnLastRow(idx) {
    if (idx === safe.length - 1 && (safe[idx].key || safe[idx].value)) {
      add();
    }
  }
  return (
    <div className="list-editor">
      {safe.length === 0 && (
        <div className="list-editor__empty">No headers. Click Add header.</div>
      )}
      {safe.map((row, idx) => (
        <div className="list-editor__row" key={idx}>
          <input
            type="text"
            value={row.key || ''}
            onChange={(e) => update(idx, { key: e.target.value })}
            onBlur={() => autoAddOnLastRow(idx)}
            placeholder={keyPlaceholder || 'Header name'}
            spellCheck={false}
          />
          <input
            type="text"
            value={row.value || ''}
            onChange={(e) => update(idx, { value: e.target.value })}
            onBlur={() => autoAddOnLastRow(idx)}
            placeholder={valuePlaceholder || 'Value'}
            spellCheck={false}
          />
          <button
            type="button"
            className="list-editor__remove"
            onClick={() => remove(idx)}
            aria-label="Remove row"
            title="Remove"
          >
            ×
          </button>
        </div>
      ))}
      <button type="button" className="list-editor__add" onClick={add}>
        + Add header
      </button>
    </div>
  );
}

// ModelListEditor — list of { name, alias, display-name?, force-mapping? }
// records, one row per model. The last row auto-appends a new blank row
// when the user starts typing so they can keep adding without clicking +.
export function ModelListEditor({ rows, onChange, fieldHints = {} }) {
  const safe = Array.isArray(rows) ? rows : [];
  function update(idx, patch) {
    const next = safe.map((r, i) => (i === idx ? { ...r, ...patch } : r));
    onChange(next);
  }
  function add() {
    onChange([...safe, { name: '', alias: '' }]);
  }
  function remove(idx) {
    if (safe.length === 1) {
      // Don't leave an empty list of models — provider would reject it.
      onChange([{ name: '', alias: '' }]);
      return;
    }
    onChange(safe.filter((_, i) => i !== idx));
  }
  function autoAddOnLastRow(idx) {
    if (idx === safe.length - 1 && (safe[idx].name || safe[idx].alias)) {
      add();
    }
  }
  return (
    <div className="list-editor">
      {safe.length === 0 && (
        <div className="list-editor__empty">No models. Click Add model.</div>
      )}
      {safe.map((row, idx) => (
        <div key={idx} style={{ display: 'flex', flexDirection: 'column', gap: 4 }}>
          <div className="list-editor__row">
            <input
              type="text"
              value={row.name || ''}
              onChange={(e) => update(idx, { name: e.target.value })}
              onBlur={() => autoAddOnLastRow(idx)}
              placeholder={fieldHints.name || 'upstream name (e.g. gpt-4o)'}
              spellCheck={false}
              aria-label="Model name"
            />
            <input
              type="text"
              value={row.alias || ''}
              onChange={(e) => update(idx, { alias: e.target.value })}
              onBlur={() => autoAddOnLastRow(idx)}
              placeholder={fieldHints.alias || 'client alias (optional)'}
              spellCheck={false}
              aria-label="Model alias"
            />
            <button
              type="button"
              className="list-editor__remove"
              onClick={() => remove(idx)}
              aria-label="Remove model"
              title="Remove"
            >
              ×
            </button>
          </div>
          <div className="list-editor__row" style={{ paddingLeft: 0 }}>
            <input
              type="text"
              value={row['display-name'] || ''}
              onChange={(e) => update(idx, { 'display-name': e.target.value })}
              placeholder={fieldHints.displayName || 'display name (optional)'}
              spellCheck={false}
              aria-label="Display name"
              style={{ flex: 1 }}
            />
            <label className="toggle-row" style={{ flex: '0 0 auto', padding: '4px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
              <span className="toggle-row__label" style={{ fontSize: 11 }}>fork</span>
              <span className="toggle-switch">
                <input
                  type="checkbox"
                  checked={!!row['fork']}
                  onChange={(e) => update(idx, { 'fork': e.target.checked })}
                  aria-label="Fork alias"
                  title="Keep the original model id available in addition to the alias"
                />
                <span className="toggle-switch__slider" />
              </span>
            </label>
            <label className="toggle-row" style={{ flex: '0 0 auto', padding: '4px 8px', border: '1px solid var(--border)', borderRadius: 'var(--radius-sm)' }}>
              <span className="toggle-row__label" style={{ fontSize: 11 }}>force-mapping</span>
              <span className="toggle-switch">
                <input
                  type="checkbox"
                  checked={!!row['force-mapping']}
                  onChange={(e) => update(idx, { 'force-mapping': e.target.checked })}
                  aria-label="Force mapping"
                />
                <span className="toggle-switch__slider" />
              </span>
            </label>
          </div>
        </div>
      ))}
      <button type="button" className="list-editor__add" onClick={add}>
        + Add model
      </button>
    </div>
  );
}
