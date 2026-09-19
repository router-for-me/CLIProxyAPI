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
// `state`, `setModels`, `providerType`, `isEdit`, `initial`, `save`, `saving`
// — ModelsTab only needs `state.models`, `setModels`, `providerType`,
// `isEdit`, plus `initial`/`save`/`saving` for the opencode-go Save & Seed
// footer button.
//
// PR 3 layout:
//   - Two-column `.models-tab` grid: configured models on the left
//     (ModelListEditor + OpenCodeGoModelListEditor), FetchModelsInline on
//     the right (where the provider type is fetchable).
//   - Search input above the left column filters the rows visible to the
//     editor. The filter is non-mutating — `state.models` stays the
//     source of truth and a translate step (handleModelsChange) splices
//     edits back into the unfiltered list.
//   - Footer shows model counts and (opencode-go only) a Save & Seed
//     button that runs save() and then the seed-models endpoint.
//
// Surgical substitutions vs the previous inline rendering:
//   local form.models               -> useEditorState().state.models
//   local update('models', v)       -> useEditorState().setModels(v)

import React, { useMemo, useState } from 'react';
import { ModelListEditor } from '../manage-cpa/FormPrimitives.jsx';
import FetchModelsInline from '../manage-cpa/FetchModelsInline.jsx';
import { useEditorState } from './useEditorState.jsx';
import { TYPE_SIMPLE } from './schemas.js';
import { useToast } from '../../components/Toast.jsx';
import { seedUpstreamProviderModels } from '../../api/client.js';

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
  const { state, setModels, providerType, isEdit, initial, save, saving } = useEditorState();
  const rows = state.models || [];

  // PR 3 — search + Save & Seed live state. Search filters which rows the
  // operator sees, but does not mutate state.models (the source of truth
  // stays intact so save() persists every row including filtered-out ones).
  const toast = useToast();
  const [search, setSearch] = useState('');
  const [seedRunning, setSeedRunning] = useState(false);

  // Local filter — case-insensitive against the row keys the list editor
  // actually uses (kebab-case `display-name`, plain `name`/`alias`). Falls
  // back to the raw rows when search is empty so the editor's render path
  // stays a single ModelListEditor branch. We keep a parallel index map so
  // onChange can splice edited rows back into the unfiltered source.
  const filtered = useMemo(() => {
    if (!search) return rows.map((m, i) => ({ m, i }));
    const q = search.toLowerCase();
    return rows
      .map((m, i) => ({ m, i }))
      .filter(({ m }) => (
        (m?.name || '').toLowerCase().includes(q)
        || (m?.alias || '').toLowerCase().includes(q)
        || (m?.['display-name'] || '').toLowerCase().includes(q)
      ));
  }, [rows, search]);

  // The list editor's onChange returns the full (filtered) array. With
  // no search active, that IS the new state.models. With a search active
  // we splice edited rows back into their original positions, drop any
  // rows that were removed (entries present in filtered but not in
  // nextFiltered), and append any newly-added rows (entries in
  // nextFiltered without a matching original index) to the tail so
  // unfiltered-out rows survive.
  function handleModelsChange(nextFiltered) {
    if (!search) {
      setModels(nextFiltered);
      return;
    }
    // Build the patch map: original index -> edited row. Anything in
    // nextFiltered past the end of `filtered` is an append (no original).
    const patches = new Map();
    const additions = [];
    nextFiltered.forEach((row, fIdx) => {
      const original = filtered[fIdx];
      if (original) patches.set(original.i, row);
      else additions.push(row);
    });
    // Apply patches to the unfiltered rows, drop originals not in patches.
    const merged = rows
      .map((row, i) => (patches.has(i) ? patches.get(i) : row))
      .filter((_, i) => patches.has(i) || !filtered.some((f) => f.i === i));
    setModels([...merged, ...additions]);
  }

  // Save & Seed — opencode-go only. save() runs first; if it succeeds and
  // the seed endpoint throws, surface a specific toast so the operator
  // knows the form is saved but the catalog still needs seeding (the
  // Quota tab has a standalone Seed button for retries).
  async function handleSaveAndSeed() {
    if (saving || seedRunning) return;
    setSeedRunning(true);
    try {
      await save();
      if (providerType === 'opencode-go' && initial?.id) {
        try {
          const result = await seedUpstreamProviderModels(initial.id);
          toast.success(`Saved and seeded (${result?.added ?? 0} models)`);
        } catch (seedErr) {
          const msg = seedErr?.message || 'Seed failed';
          toast.error(`Saved, but seeding failed — retry from Models tab (${msg})`);
        }
      }
    } finally {
      setSeedRunning(false);
    }
  }

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
  const aliasCount = rows.filter((m) => m?.alias).length;
  const forkCount = rows.filter((m) => m?.fork).length;
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

        {/* PR 3 — two-column layout: configured models (left, with search)
            + FetchModelsInline probe (right). The left column passes the
            filtered list to the editor; onChange still feeds setModels
            so unfiltered rows remain the source of truth. */}
        <div className="models-tab">
          <div className="models-tab__left">
            <div className="models-tab__search">
              <input
                type="text"
                value={search}
                onChange={(e) => setSearch(e.target.value)}
                placeholder="Filter by name, alias, or display name…"
                aria-label="Filter models"
                spellCheck={false}
              />
              {search && (
                <button
                  type="button"
                  className="models-tab__search-clear"
                  onClick={() => setSearch('')}
                  aria-label="Clear search"
                  title="Clear search"
                >
                  ×
                </button>
              )}
            </div>
            {providerType === 'opencode-go' ? (
              <OpenCodeGoModelListEditor rows={filtered.map((f) => f.m)} onChange={handleModelsChange} />
            ) : (
              <ModelListEditor rows={filtered.map((f) => f.m)} onChange={handleModelsChange}
                fieldHints={modelHints} />
            )}
          </div>

          {FETCHABLE_TYPES.has(providerType) && (
            <div className="models-tab__right">
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
            </div>
          )}
        </div>

        {/* Footer — model counts on every provider type; Save & Seed is
            opencode-go only (combines the standard save() flow with the
            catalog seed endpoint). */}
        <div className="models-tab__footer">
          <span className="dim">
            {rows.length} models · {aliasCount} with aliases · {forkCount} with fork flag
          </span>
          {providerType === 'opencode-go' && (
            <button
              type="button"
              className="btn btn--primary"
              onClick={handleSaveAndSeed}
              disabled={saving || seedRunning}
              data-testid="models-save-and-seed"
            >
              {seedRunning ? 'Seeding…' : 'Save & Seed'}
            </button>
          )}
        </div>
      </div>
    </section>
  );
}

export default ModelsTab;