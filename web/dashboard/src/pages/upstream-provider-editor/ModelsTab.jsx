// ============================================================================
// Upstream provider editor — Models tab
// ============================================================================
//
// ModelsTab renders the `models` field + the FetchModelsInline sub-section
// lifted verbatim from the previous monolithic ProviderEditorForm (and now
// OverviewTab). It is one of six tabs the new tabbed editor (PR 2) mounts
// under /upstream-providers/:id/:tab; the route shell (./index.jsx) supplies
// the EditorStateProvider context, the sticky header (type/identifier/dirty
// marker/hint), and the TabBar.
//
// State source: useEditorState() (./useEditorState.jsx). The context exposes
// `state`, `setModels`, `providerType`, `isEdit` — ModelsTab only needs
// `state.models`, `setModels`, `providerType`, and `isEdit`.
//
// Surgical substitutions vs the previous inline rendering:
//   local form.models               -> useEditorState().state.models
//   local update('models', v)       -> useEditorState().setModels(v)
//
// PR 3 will overhaul this tab (model catalog search, per-row wire-format
// editing without opencode-go branching, etc.). For PR 2 the tab is a
// verbatim lift — the operator can still add/remove model rows and probe
// the upstream endpoint via FetchModelsInline.

import React from 'react';
import { ModelListEditor } from '../manage-cpa/FormPrimitives.jsx';
import FetchModelsInline from '../manage-cpa/FetchModelsInline.jsx';
import { useEditorState } from './useEditorState.jsx';
import { TYPE_SIMPLE } from './schemas.js';

// Types where the FetchModelsInline probe is meaningful (has a base_url +
// api_key the operator can probe, or an auth-file the registry tracks).
// Mirrors the constant in OverviewTab.jsx / CreateMode.jsx; duplicated here
// because ModelsTab owns this section now.
const FETCHABLE_TYPES = new Set([
  'gemini-api-key', 'interactions-api-key', 'codex-api-key', 'xai-api-key',
  'claude-api-key', 'vertex-api-key', 'openai-compatibility',
]);

// OpenCodeGoModelListEditor extends the shared ModelListEditor with a
// per-model wire-format select (openai default / anthropic). Wire format is
// stored on the row under the kebab-case 'wire-format' key, matching the
// form hydration/serialization convention. Verbatim lift from OverviewTab.
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

export function ModelsTab() {
  const { state, setModels, providerType, isEdit } = useEditorState();
  const rows = state.models || [];

  // openai-compatibility rows use `type: 'openai_models'` in the schema,
  // which carried different placeholder hints upstream (verbing out a
  // model name like `anthropic/claude-3-5-sonnet`). PR 2 verbatim-lift:
  // match the per-type hints the old renderInput branch produced so the
  // operator's UX is unchanged when this tab takes over from Overview.
  const isOpenAICompat = providerType === 'openai-compatibility';
  const modelHints = isOpenAICompat
    ? {
        name: 'upstream model (e.g. anthropic/claude-3-5-sonnet)',
        alias: 'client alias (e.g. claude-sonnet)',
        displayName: 'display name (optional)',
      }
    : {
        name: 'upstream name',
        alias: 'client alias',
        displayName: 'display name',
      };

  // Section header — kept consistent with OverviewTab's form-section
  // styling so the tab body looks native to the rest of the editor.
  return (
    <section>
      <div className="form-section">
        <div className="form-section__title">Models</div>
        <div className="form-section__hint">
          Upstream models this provider serves. Rows map the upstream
          <code className="mono"> name</code> to a client-facing
          <code className="mono"> alias</code>. The alias is what API
          callers send; the name is what gets sent upstream.
        </div>
        <div className="form-section__row">
          {providerType === 'opencode-go' ? (
            <OpenCodeGoModelListEditor rows={rows} onChange={setModels} />
          ) : (
            <ModelListEditor rows={rows} onChange={setModels}
              fieldHints={modelHints} />
          )}
        </div>
      </div>

      {/* Fetch Models — verbatim lift of the section-level inline from
          OverviewTab. Hidden for non-fetchable provider types (OAuth-only
          flows) and for opencode-go (which has its own catalog flow in
          the Quota tab). */}
      {FETCHABLE_TYPES.has(providerType) && (
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
              setModels(merged);
            }
          }}
        />
      )}
    </section>
  );
}

export default ModelsTab;