import React, { useState } from 'react';
import ModelMultiSelect from './ModelMultiSelect.jsx';
import ModelRouteEntryModal from './ModelRouteEntryModal.jsx';

// ModelGroupForm — controlled form for editing a ModelGroup template.
//
// Allowed models render as a combined table: one row per concrete model with
// compact summaries of its per-model routing (strategy + pinned providers)
// and per-model caps (RPM / Max Budget), plus Edit / Delete row actions and
// an "+ Add Model" affordance. Editing a row opens ModelRouteEntryModal —
// the same workflow the per-model routing editor uses (provider chip
// toggles, priority inputs, strategy segmented control) plus the caps.
//
// Wildcard entries (e.g. gpt-4*) live in a separate combobox below the
// table: they cannot be pinned to providers nor carry caps.
//
// Mirrors the shape accepted by POST / PUT /v0/management/model-groups.
// Callers receive the up-to-date payload via onChange; submit is delegated
// to the parent.
//
// Fields:
//   name        — unique, required.
//   description — free-form label.
//   allowed_models — grant list. Empty = all allowed. Supports wildcards.
//   blocked_models — takes precedence over allowed. Supports wildcards.
//   model_routes   — per-concrete-model provider pinning + RPM/budget caps.
//   metadata       — free-form JSON.
const EMPTY = {
  name: '',
  description: '',
  allowed_models: [],
  blocked_models: [],
  model_routes: [],
  metadata: '{}',
};

function numToStr(v) {
  if (v === null || v === undefined || v === '') return '';
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? String(n) : '';
}

export function groupToForm(group) {
  if (!group) return { ...EMPTY, metadata: '{}' };
  return {
    name: group.name ?? '',
    description: group.description ?? '',
    allowed_models: Array.isArray(group.allowed_models) ? [...group.allowed_models] : [],
    blocked_models: Array.isArray(group.blocked_models) ? [...group.blocked_models] : [],
    model_routes: Array.isArray(group.model_routes)
      ? group.model_routes.map((r) => ({
          model: r.model || '',
          providers: Array.isArray(r.providers) ? [...r.providers] : [],
          strategy: r.strategy || '',
          rpm: numToStr(r.rpm_limit),
          max_budget: numToStr(r.max_budget_usd),
          priorities: Array.isArray(r.priorities)
            ? r.priorities.map((pr) => ({ provider: pr.provider || '', priority: Number(pr.priority) || 0 }))
            : [],
        }))
      : [],
    metadata: group.metadata ? JSON.stringify(group.metadata, null, 2) : '{}',
  };
}

export function formToGroup(form) {
  return {
    name: (form.name || '').trim(),
    description: (form.description || '').trim(),
    allowed_models: dedupe(listFromField(form.allowed_models)),
    blocked_models: dedupe(listFromField(form.blocked_models)),
    model_routes: (Array.isArray(form.model_routes) ? form.model_routes : [])
      .filter((r) => {
        if (!r || !r.model || r.model.endsWith('*')) return false;
        if (!form.allowed_models.includes(r.model)) return false;
        // Keep rows that pin providers, set a strategy, or carry caps.
        const hasProviders = Array.isArray(r.providers) && r.providers.length > 0;
        const hasStrategy = r.strategy === 'priority' || r.strategy === 'failover';
        const hasCaps = numToStr(r.rpm) !== '' || numToStr(r.max_budget) !== '';
        return hasProviders || hasStrategy || hasCaps;
      })
      .map((r) => {
        const out = { model: r.model, providers: dedupe(r.providers || []) };
        const strategy = String(r.strategy || '').trim();
        if (strategy === 'priority' || strategy === 'failover') {
          out.strategy = strategy;
          const priorities = (Array.isArray(r.priorities) ? r.priorities : [])
            .filter((pr) => pr && pr.provider && out.providers.includes(pr.provider))
            .map((pr) => ({ provider: pr.provider, priority: Number(pr.priority) || 0 }));
          if (priorities.length > 0) out.priorities = priorities;
        }
        const rpm = Number(r.rpm);
        if (Number.isFinite(rpm) && rpm > 0) out.rpm_limit = Math.trunc(rpm);
        const budget = Number(r.max_budget);
        if (Number.isFinite(budget) && budget > 0) out.max_budget_usd = budget;
        return out;
      }),
    metadata: parseMetadata(form.metadata),
  };
}

function listFromField(v) {
  if (Array.isArray(v)) return v.map((s) => String(s).trim()).filter(Boolean);
  if (typeof v === 'string') return v.split('\n').map((l) => l.trim()).filter(Boolean);
  return [];
}
function dedupe(arr) { return Array.from(new Set(arr)); }
function parseMetadata(s) {
  if (!s || s.trim() === '' || s.trim() === '{}') return {};
  try {
    const parsed = JSON.parse(s);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

function routeFor(routes, model) {
  return (routes || []).find((r) => r && r.model === model);
}

// strategyLabel renders the compact strategy cell value.
function strategyLabel(strategy) {
  if (strategy === 'priority') return 'priority';
  if (strategy === 'failover') return 'failover';
  return 'default';
}

export default function ModelGroupForm({ initial, onChange }) {
  const [form, setForm] = useState(() => groupToForm(initial));
  const [modal, setModal] = useState(null); // { mode: 'add' } | { mode: 'edit', model }
  const [confirmDelete, setConfirmDelete] = useState(null);

  function update(partial) {
    setForm((f) => ({ ...f, ...partial }));
  }

  // Keep parent in sync whenever the form changes. We only call onChange with
  // the raw form — the parent decides when to convert via formToGroup.
  React.useEffect(() => {
    onChange?.(form);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form]);

  const concreteModels = (form.allowed_models || []).filter((m) => m && !m.endsWith('*'));
  const wildcardModels = (form.allowed_models || []).filter((m) => m && m.endsWith('*'));

  function upsertEntry(entry) {
    setForm((f) => {
      const allowed = f.allowed_models.includes(entry.model)
        ? f.allowed_models
        : [...f.allowed_models, entry.model];
      const rest = (f.model_routes || []).filter((r) => r.model !== entry.model);
      const hasConfig =
        (entry.providers && entry.providers.length > 0) ||
        entry.strategy === 'priority' ||
        entry.strategy === 'failover' ||
        numToStr(entry.rpm) !== '' ||
        numToStr(entry.max_budget) !== '';
      const routes = hasConfig ? [...rest, entry] : rest;
      // Keep concrete list sorted by existing wildcard order stable: concrete
      // entries stay in insertion order, wildcards at their existing tail.
      return { ...f, allowed_models: allowed, model_routes: routes };
    });
    setModal(null);
  }

  function removeModel(model) {
    setForm((f) => ({
      ...f,
      allowed_models: (f.allowed_models || []).filter((m) => m !== model),
      model_routes: (f.model_routes || []).filter((r) => r.model !== model),
    }));
    setConfirmDelete(null);
  }

  return (
    <div>
      <div className="grid grid--2">
        <div className="form__row">
          <label className="form__label">Name *</label>
          <input type="text" value={form.name}
            onChange={(e) => update({ name: e.target.value })}
            placeholder="e.g. gpt-only, claude-pool, default" />
        </div>
        <div className="form__row">
          <label className="form__label">Description</label>
          <input type="text" value={form.description}
            onChange={(e) => update({ description: e.target.value })}
            placeholder="what this group grants" />
        </div>
      </div>

      <div className="form__row">
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
          <div>
            <label className="form__label" style={{ margin: 0 }}>Allowed models</label>
            <div className="form__hint" style={{ marginTop: 4 }}>
              The group's grant list. Each concrete model may pin providers,
              pick a routing strategy, and cap RPM / Max Budget for the keys
              attached to this group.
            </div>
          </div>
          <button type="button" onClick={() => setModal({ mode: 'add' })}>+ Add Model</button>
        </div>

        {concreteModels.length === 0 ? (
          <div className="muted" style={{ padding: '12px 0' }}>
            No concrete models yet. Use <strong>+ Add Model</strong> to grant
            a specific model, or add a wildcard pattern below for broad grants.
            Empty = all models allowed.
          </div>
        ) : (
          <table className="table sgl-table">
            <thead>
              <tr>
                <th>Model</th>
                <th>Strategy</th>
                <th>Providers</th>
                <th className="sgl-num">RPM</th>
                <th className="sgl-num">Max Budget</th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {concreteModels.map((model) => {
                const r = routeFor(form.model_routes, model);
                const providers = r?.providers || [];
                const rpm = r?.rpm || '';
                const budget = r?.max_budget || '';
                return (
                  <tr key={model}>
                    <td className="mono">{model}</td>
                    <td>
                      <span className={`sgl-strat sgl-strat--${strategyLabel(r?.strategy)}`}>
                        {strategyLabel(r?.strategy)}
                      </span>
                    </td>
                    <td>
                      {providers.length === 0
                        ? <span className="dim">all (default)</span>
                        : <span className="mono" title={providers.join(', ')}>{providers.join(', ')}</span>}
                    </td>
                    <td className="sgl-num mono">{rpm || <span className="dim">—</span>}</td>
                    <td className="sgl-num mono">{budget ? `$${budget}` : <span className="dim">—</span>}</td>
                    <td style={{ textAlign: 'right', whiteSpace: 'nowrap' }}>
                      {confirmDelete === model ? (
                        <>
                          <button
                            type="button"
                            className="row-actions__btn row-actions__btn--danger"
                            onClick={() => removeModel(model)}
                          >Confirm</button>
                          <button
                            type="button"
                            className="row-actions__btn"
                            onClick={() => setConfirmDelete(null)}
                          >Cancel</button>
                        </>
                      ) : (
                        <>
                          <button
                            type="button"
                            className="row-actions__btn"
                            onClick={() => setModal({ mode: 'edit', model })}
                          >Edit</button>
                          <button
                            type="button"
                            className="row-actions__btn row-actions__btn--danger"
                            onClick={() => setConfirmDelete(model)}
                          >Delete</button>
                        </>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>

      <ModelMultiSelect
        label="Wildcard allowed patterns"
        value={wildcardModels}
        onChange={(v) => update({ allowed_models: [...concreteModels, ...v] })}
        placeholder="e.g. gpt-4*"
        hint="Broad grant entries that match many model ids. Wildcards cannot be pinned to providers and cannot carry per-model caps."
      />

      <ModelMultiSelect
        label="Blocked models"
        value={form.blocked_models}
        onChange={(v) => update({ blocked_models: v })}
        placeholder="models that should always be rejected"
        hint="Takes precedence over the allowed list. Supports wildcards like claude-*."
      />
      <div className="form__row">
        <label className="form__label">Metadata (JSON)</label>
        <textarea rows={3} value={form.metadata}
          onChange={(e) => update({ metadata: e.target.value })}
          placeholder="{}" />
      </div>

      {modal && (
        <ModelRouteEntryModal
          existingModels={concreteModels}
          initial={modal.mode === 'edit' ? routeFor(form.model_routes, modal.model) || { model: modal.model } : null}
          onCancel={() => setModal(null)}
          onSave={upsertEntry}
        />
      )}
    </div>
  );
}
