// ============================================================================
// Upstream provider editor — Overview tab
// ============================================================================
//
// OverviewTab renders the schema-driven form sections lifted verbatim from
// the previous monolithic ProviderEditorForm (./CreateMode.jsx, pre-PR-2
// index.jsx). It is one of six tabs the new tabbed editor (PR 2) mounts
// under /upstream-providers/:id/:tab; the route shell (./index.jsx) supplies
// the EditorStateProvider context, the sticky header (type/identifier/dirty
// marker/hint — formerly inline in the form body), and the TabBar.
//
// State source: useEditorState() (./useEditorState.jsx). The context exposes
// `state` (the form object), `setField(name, value)`, `setEntries(...)`,
// `touched`, `errors`, `providerType`, `isEdit`, `schema`, `oauthConnected`.
// `siblingNames` is consumed internally by the context for `validate()`;
// `FetchModelsInline` is passed an empty array here because the tab doesn't
// own the page-level sibling fetch (the context already gates the validation
// side). `proxyPools` is fetched locally for the proxy_pool_id + api_key_entries
// pickers, matching the pattern CreateMode used before the context lift.
//
// Surgical substitutions vs the old ProviderEditorForm body:
//   local form state              -> useEditorState().state
//   local update(name, value)     -> useEditorState().setField(name, value)
//   handleAddEntry/handleRemoveEntry (none in Overview; entries editor uses
//                                     setField/setEntries via its own callbacks)
//
// Explicitly excluded from this tab:
//   - Editor summary banner (now lives in EditorShell's sticky header)
//   - OAuth connect sub-section (create-mode-only, handled by CreateMode)
//   - Type-picker step (create-mode-only, handled by CreateMode)
//
// The `modelsTab` flag (from the design doc) was false during PR 2, keeping
// the inline `models` field on Overview. PR 3 flipped it to true: the Models
// tab (./ModelsTab.jsx) now owns the field and the Overview no longer
// renders it. Still a module-local constant, colocated with the lift site.

import React, { useEffect, useState } from 'react';
import {
  Field,
  PasswordInput,
  ToggleRow,
  ChipListEditor,
  KeyValueEditor,
  ModelListEditor,
} from '../manage-cpa/FormPrimitives.jsx';
import FetchModelsInline from '../manage-cpa/FetchModelsInline.jsx';
import APIKeyEntriesEditor from './EntriesEditor.jsx';
import {
  listProxyPools,
} from '../../api/client.js';
import {
  API_KEY_TYPES,
  OAUTH_TYPES,
  TYPE_SIMPLE,
} from './schemas.js';
import { useEditorState } from './useEditorState.jsx';

// PR 3 has shipped: ModelsTab owns the `models` field, so it no longer
// renders inline in Overview. Held as a module-local constant so the
// conditional is colocated with the lift site.
const MODELS_TAB = true;

// Types where the FetchModelsInline probe is meaningful (has a base_url +
// api_key the operator can probe, or an auth-file the registry tracks).
// Mirrors the constant in CreateMode.jsx; duplicated here because OverviewTab
// is the only consumer after the create-flow split.
const FETCHABLE_TYPES = new Set([
  'gemini-api-key', 'interactions-api-key', 'codex-api-key', 'xai-api-key',
  'claude-api-key', 'vertex-api-key', 'openai-compatibility',
]);

export default function OverviewTab() {
  const {
    state,
    setField,
    providerType,
    setProviderType,
    isEdit,
    schema,
    errors,
    touched,
    oauthConnected,
    oauthConnectable,
  } = useEditorState();

  // Active proxy pools feed the row-level + per-entry pickers. The list is
  // fetched once on mount; a stale binding (pool deleted elsewhere) still
  // shows its raw id so the operator can clear it. Matches the pattern
  // CreateMode used before the context lift — the page shell doesn't own
  // this list yet, so the tab fetches it directly.
  const [proxyPools, setProxyPools] = useState([]);
  useEffect(() => {
    let cancelled = false;
    listProxyPools()
      .then((res) => { if (!cancelled) setProxyPools(Array.isArray(res?.pools) ? res.pools : []); })
      .catch(() => { /* picker degrades to manual URL only */ });
    return () => { cancelled = true; };
  }, []);

  return (
    <>
      {/* Provider type selector — still rendered first so the type stays
          switchable in create mode; disabled in edit (the type is the row's
          identity). The section is intentionally NOT part of schema.sections
          (the schema is keyed on provider_type, so a section keyed by type
          would self-reference). */}
      <div className="form-section">
        <div className="form-section__title">Provider Type</div>
        <div className="form-section__row">
          <Field label="Type" required>
            <select value={providerType} onChange={(e) => setProviderType(e.target.value)} disabled={isEdit}>
              <option value="">Select a provider type…</option>
              <optgroup label="API Key">{API_KEY_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
              <optgroup label="OAuth / File-backed">{OAUTH_TYPES.map((t) => <option key={t.value} value={t.value}>{t.label}</option>)}</optgroup>
            </select>
          </Field>
        </div>
      </div>

      {/* Schema-driven sections. Each provider type's buildSchemas() entry
          defines the ordered Identity / Endpoint / Routing / Behavior
          sections (plus optional Cloak for Claude). The loop body is the
          exact lift from ProviderEditorForm; the form state + update handler
          come from context now. */}
      {schema.sections.map((section) => {
        // For OAuth providers, hide the OAuth token section until the
        // connect flow is done (those fields are populated by the callback).
        // The connect flow itself is owned by CreateMode, so this branch
        // only triggers in create mode and falls through harmlessly in edit.
        if (oauthConnectable && !oauthConnected && section.title === 'OAuth token') {
          return null;
        }
        // With the Models tab shipped (PR 3), the inline `models` field has
        // moved out of the Routing section. Filter the field out of the loop
        // output so Overview doesn't duplicate the field while ModelsTab
        // owns it.
        return (
          <div className="form-section" key={section.title}>
            <div className="form-section__title">{section.title}</div>
            {section.hint && <div className="form-section__hint">{section.hint}</div>}
            <div className="form-section__row">
              {section.fields.map((field) => {
                if (MODELS_TAB && field.name === 'models') return null;
                const showError = touched[field.name] && errors[field.name];
                return (
                  <Field
                    key={field.name}
                    label={field.label}
                    hint={field.hint}
                    error={showError ? errors[field.name] : ''}
                    required={field.required}
                    htmlFor={`up_${field.name.replace(/[.\s]/g, '_')}`}
                  >
                    {renderInput(field, state, setField, isEdit, providerType, proxyPools)}
                  </Field>
                );
              })}
            </div>

            {/* Inline Fetch Models — rendered at the bottom of sections that
                declare fetchModels:true, for fetchable provider types. */}
            {section.fetchModels && FETCHABLE_TYPES.has(providerType) && (
              <FetchModelsInline
                form={state}
                provider={TYPE_SIMPLE[providerType] || providerType}
                isEdit={isEdit}
                siblingNames={[]}
                onAddModels={(picked) => {
                  const existing = Array.isArray(state.models) ? state.models : [];
                  const byName = new Set(existing.map((r) => (r?.name || r?.id || '').trim()).filter(Boolean));
                  const additions = picked
                    .filter((p) => p && (p.id || p.name) && !byName.has(p.id || p.name))
                    .map((p) => {
                      const m = { name: p.id || p.name };
                      // Use camelCase keys for the upstream_providers API.
                      if (p.display_name) m.display_name = p.display_name;
                      return m;
                    });
                  if (additions.length > 0) {
                    // Merge into existing model rows (which use camelCase).
                    const merged = [...existing];
                    for (const a of additions) merged.push(a);
                    setField('models', merged);
                  }
                }}
              />
            )}
          </div>
        );
      })}
    </>
  );
}

// renderInput dispatches by field.type. Verbatim lift from ProviderEditorForm;
// the only substitution is that the update() callback is now supplied by the
// caller (useEditorState().setField) instead of the local form's setter.
function renderInput(field, form, update, isEdit, providerType, proxyPools = []) {
  const id = `up_${field.name.replace(/[.\s]/g, '_')}`;
  const value = form[field.name];
  switch (field.type) {
    case 'password':
      return <PasswordInput id={id} value={value} onChange={(v) => update(field.name, v)}
        placeholder={field.placeholder} defaultShown={isEdit} />;
    case 'number':
      return <input id={id} type="number" min={field.min} value={value ?? ''}
        onChange={(e) => update(field.name, e.target.value)} placeholder={field.placeholder} />;
    case 'select':
      return <select id={id} value={value || ''} onChange={(e) => update(field.name, e.target.value)}>
        {field.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
      </select>;
    case 'toggle':
      return <ToggleRow label={field.label} hint={field.hint} checked={!!value}
        onChange={(v) => update(field.name, v)} />;
    case 'chips':
      return <ChipListEditor values={value || []} onChange={(v) => update(field.name, v)}
        placeholder={field.placeholder} emptyHint={field.emptyHint} />;
    case 'headers':
      return <KeyValueEditor rows={value || []} onChange={(v) => update(field.name, v)} />;
    case 'models':
      return <ModelListEditor rows={value || []} onChange={(v) => update(field.name, v)}
        fieldHints={{ name: 'upstream name', alias: 'client alias', displayName: 'display name' }} />;
    case 'openai_models':
      return <ModelListEditor rows={value || []} onChange={(v) => update(field.name, v)}
        fieldHints={{
          name: 'upstream model (e.g. anthropic/claude-3-5-sonnet)',
          alias: 'client alias (e.g. claude-sonnet)',
          displayName: 'display name (optional)',
        }} />;
    case 'opencode_models':
      return <OpenCodeGoModelListEditor rows={value || []} onChange={(v) => update(field.name, v)} />;
    case 'proxy_pool_id': {
      // Row-level pool binding: inherit (empty) / active pools / direct.
      // Selecting a pool clears the manual proxy_url field (the renderer
      // makes them exclusive; keep the form consistent with that).
      const numeric = Number(value);
      const selected = Number.isFinite(numeric) && numeric > 0 ? String(numeric) : '';
      return (
        <select
          id={id}
          value={selected}
          onChange={(e) => {
            const v = e.target.value;
            if (v === '') {
              update(field.name, '');
            } else {
              update(field.name, Number(v));
              update('proxy_url', '');
            }
          }}
          data-testid="up_proxy_pool_id"
        >
          <option value="">inherit: manual proxy URL / global</option>
          {proxyPools.filter((p) => p.is_active).map((p) => (
            <option key={p.id} value={String(p.id)}>{p.name}</option>
          ))}
          <option value="0">none (no pool)</option>
        </select>
      );
    }
    case 'api_key_entries':
      return <APIKeyEntriesEditor
        entries={value || []}
        onChange={(v) => update(field.name, v)}
        proxyPools={proxyPools}
      />;
    case 'text':
    default:
      return <input id={id} type="text" value={value || ''} onChange={(e) => update(field.name, e.target.value)}
        placeholder={field.placeholder} required={field.required} />;
  }
}

// OpenCodeGoModelListEditor extends the shared ModelListEditor with a
// per-model wire-format select (openai default / anthropic). Wire format is
// stored on the row under the kebab-case 'wire-format' key, matching the
// form hydration/serialization convention.
function OpenCodeGoModelListEditor({ rows, onChange }) {
  const safe = Array.isArray(rows) ? rows : [];
  function updateWire(idx, v) {
    const next = safe.map((r, i) => (i === idx ? { ...r, 'wire-format': v } : r));
    onChange(next);
  }
  return (
    <div>
      <ModelListEditor rows={safe} onChange={onChange}
        fieldHints={{
          name: 'upstream model (e.g. glm-5.2, minimax-m3)',
          alias: 'client alias (optional)',
          displayName: 'display name (optional)',
        }} />
      {safe.length > 0 && (
        <div style={{ marginTop: 4, fontSize: 11 }} className="dim">
          Wire format per model (rows align top-to-bottom with the list above):
        </div>
      )}
      {safe.map((row, idx) => (
        <div key={idx} style={{ display: 'flex', alignItems: 'center', gap: 8, marginTop: 2 }}>
          <span className="dim" style={{ minWidth: 110, overflow: 'hidden', textOverflow: 'ellipsis' }}>
            {row?.name || '(unnamed)'}
          </span>
          <select
            value={row?.['wire-format'] || ''}
            onChange={(e) => updateWire(idx, e.target.value)}
            aria-label={`Wire format for ${row?.name || 'model'}`}
            data-testid="opengo-wire-format"
          >
            <option value="">openai (default)</option>
            <option value="openai">openai</option>
            <option value="anthropic">anthropic</option>
          </select>
        </div>
      ))}
    </div>
  );
}