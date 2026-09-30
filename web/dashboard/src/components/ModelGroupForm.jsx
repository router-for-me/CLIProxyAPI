import React, { useState, useMemo } from 'react';
import ModelMultiSelect from './ModelMultiSelect.jsx';
import ModelRouteEntryModal from './ModelRouteEntryModal.jsx';
import { dedupeStrings, routeToWire, routeHasConfig } from './modelRoute.js';
import { useAsync } from '../hooks/useAsync.js';
import { listModelsCatalog } from '../api/client.js';
import { fmtRate, pricingHasRates, TOKEN_SEGMENTS } from '../pages/usageShared.jsx';

// ModelGroupForm — controlled form for editing a ModelGroup template.
//
// Allowed models render as a combined table: one row per concrete model with
// compact summaries of its per-model routing (strategy + pinned providers),
// per-model caps (RPM / Max Budget), per-model discount, and the model's
// catalog pricing rates (USD/1M tokens), plus Edit / Delete row actions and
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
//   discount_pct — group-level default discount % (0–100) applied to cost_usd.
//   allowed_models — grant list. Empty = all allowed. Supports wildcards.
//   blocked_models — takes precedence over allowed. Supports wildcards.
//   model_routes   — per-concrete-model provider pinning + RPM/budget caps +
//                    per-model discount_pct (wins over the group default).
//   metadata       — free-form JSON.
const EMPTY = {
  name: '',
  description: '',
  discount_pct: '',
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

// discountPctToStr is like numToStr but also accepts a 0 (clearing the
// discount) — kept separate so a stray 0 from an empty backend default does
// not materialize as a stray "0" in the group-default input.
function discountPctToStr(v) {
  if (v === null || v === undefined || v === '') return '';
  const n = Number(v);
  return Number.isFinite(n) && n > 0 ? String(n) : '';
}

export function groupToForm(group) {
  if (!group) return { ...EMPTY, metadata: '{}' };
  return {
    name: group.name ?? '',
    description: group.description ?? '',
    discount_pct: discountPctToStr(group.discount_pct),
    allowed_models: Array.isArray(group.allowed_models) ? [...group.allowed_models] : [],
    blocked_models: Array.isArray(group.blocked_models) ? [...group.blocked_models] : [],
    model_routes: Array.isArray(group.model_routes)
      ? group.model_routes.map((r) => ({
          model: r.model || '',
          providers: Array.isArray(r.providers) ? [...r.providers] : [],
          strategy: r.strategy || '',
          rpm: numToStr(r.rpm_limit),
          max_budget: numToStr(r.max_budget_usd),
          discount: discountPctToStr(r.discount_pct),
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
    discount_pct: discountPctToNum(form.discount_pct),
    allowed_models: dedupeStrings(form.allowed_models),
    blocked_models: dedupeStrings(form.blocked_models),
    model_routes: (Array.isArray(form.model_routes) ? form.model_routes : [])
      .filter((r) => {
        if (!r || !r.model || r.model.endsWith('*')) return false;
        if (!form.allowed_models.includes(r.model)) return false;
        // Keep rows that pin providers, set a strategy, or carry caps/discounts.
        const hasCaps = numToStr(r.rpm) !== '' || numToStr(r.max_budget) !== '' || discountPctToStr(r.discount) !== '';
        return routeHasConfig(r, { hasExtras: hasCaps });
      })
      .map((r) => {
        const out = routeToWire(r);
        const rpm = Number(r.rpm);
        if (Number.isFinite(rpm) && rpm > 0) out.rpm_limit = Math.trunc(rpm);
        const budget = Number(r.max_budget);
        if (Number.isFinite(budget) && budget > 0) out.max_budget_usd = budget;
        const discount = discountPctToNum(r.discount);
        if (discount > 0) out.discount_pct = discount;
        return out;
      }),
    metadata: parseMetadata(form.metadata),
  };
}

// discountPctToNum parses a discount percentage string into a number in
// [0,100]; returns 0 (treated as "no discount" by the backend, which omits
// the field) when the value is empty/invalid/out of range.
function discountPctToNum(v) {
  if (v === null || v === undefined || v === '') return 0;
  const n = Number(v);
  if (!Number.isFinite(n) || n <= 0) return 0;
  if (n > 100) return 100;
  return n;
}

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

// InlinePricing renders a compact per-segment rate summary for one model from
// its catalog pricing object. Only segments with a non-zero rate are shown, so
// free/unpriced models read cleanly (a banner distinguishes the two below).
// Rates are USD per 1M tokens, mirroring the cost-derivation table on Recent
// Events. When the pricing object has no rates at all (pricingHasRates=false)
// we render an "unpriced" hint rather than a row of $0s.
function InlinePricing({ pricing }) {
  if (!pricing) {
    return <span className="dim">—</span>;
  }
  if (!pricingHasRates(pricing)) {
    return <span className="dim" title="No pricing row configured for this model">unpriced</span>;
  }
  const segs = TOKEN_SEGMENTS
    .map((s) => ({ ...s, rate: Number(pricing[s.rateKey] || 0) }))
    .filter((s) => s.rate > 0);
  return (
    <span className="mgf-pricing" title="USD per 1M tokens (click Edit on the model to set rates in Model Catalog)">
      {segs.map((s) => (
        <span key={s.key} className="mgf-pricing__seg">
          <span className="mgf-pricing__lbl dim">{s.short}</span>
          <span className="mono">{fmtRate(s.rate)}</span>
        </span>
      ))}
    </span>
  );
}

export default function ModelGroupForm({ initial, onChange }) {
  const [form, setForm] = useState(() => groupToForm(initial));
  const [modal, setModal] = useState(null); // { mode: 'add' } | { mode: 'edit', model }
  const [confirmDelete, setConfirmDelete] = useState(null);

  // Load the available-models snapshot once so the per-model table can show
  // each model's pricing inline. We page through the catalog (distinctIds
  // dedupes to one row per model id) and retain the embedded `pricing` object
  // — the listing endpoint already enriches rows with pricing server-side, so
  // no per-model fetch is needed. Failures degrade gracefully: an empty map
  // leaves the Pricing column blank rather than blocking the form.
  const catalog = useAsync(async () => {
    const acc = []; // [{ id, pricing }]
    let page = 1;
    const PAGE_SIZE = 200;
    const MAX = 1000; // safety cap consistent with ModelMultiSelect
    while (acc.length < MAX) {
      const res = await listModelsCatalog({ page, pageSize: PAGE_SIZE, availableOnly: true, distinctIds: true });
      const rows = Array.isArray(res?.models) ? res.models : [];
      for (const r of rows) {
        const id = r?.id || r?.name;
        if (!id) continue;
        acc.push({ id, pricing: r?.pricing || null, displayName: r?.display_name || r?.displayName || '' });
      }
      const totalPages = Number(res?.total_pages) || 1;
      if (page >= totalPages || rows.length === 0) break;
      page += 1;
    }
    return acc;
  }, []);

  // pricingById maps a model id → its pricing object (the five per-segment
  // USD/1M rates). Memoized so the per-model table rows don't recompute the
  // lookup on every render.
  const pricingById = useMemo(() => {
    const m = new Map();
    for (const r of catalog.data || []) {
      if (!r?.id) continue;
      // Keep the first occurrence (distinctIds already dedupes server-side,
      // but a wildcard-free string match means case sensitivity matters: the
      // catalog sends lowercase ids, allowed_models may carry mixed case — we
      // key on the raw id and lowercase both sides at lookup time).
      if (!m.has(r.id)) m.set(r.id, r.pricing);
    }
    return m;
  }, [catalog.data]);

  function lookupPricing(modelId) {
    if (!modelId) return null;
    if (pricingById.has(modelId)) return pricingById.get(modelId);
    // Case-insensitive fallback so an allowed_models entry like "GPT-4O" still
    // resolves to its catalog pricing row.
    const lower = modelId.toLowerCase();
    for (const [k, v] of pricingById) {
      if (k.toLowerCase() === lower) return v;
    }
    return null;
  }

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
        numToStr(entry.max_budget) !== '' ||
        discountPctToStr(entry.discount) !== '';
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
        <label className="form__label">Default Discount %</label>
        <input type="number" min="0" max="100" step="1" value={form.discount_pct}
          onChange={(e) => update({ discount_pct: e.target.value })}
          placeholder="none" style={{ maxWidth: 220 }} />
        <div className="form__hint">
          Percentage off cost_usd applied to every model in this group (0–100). A
          per-model discount set on a row below takes precedence. 20 = billed at
          80% of the model pricing. Applies to usage_events and budget windows.
        </div>
      </div>

      <div className="form__row">
        <div className="row" style={{ justifyContent: 'space-between', alignItems: 'center', marginBottom: 8 }}>
          <div>
            <label className="form__label" style={{ margin: 0 }}>Allowed models</label>
            <div className="form__hint" style={{ marginTop: 4 }}>
              The group's grant list. Each concrete model may pin providers,
              pick a routing strategy, and cap RPM / Max Budget for the keys
              attached to this group. The Pricing column shows each model's
              catalog rates (USD/1M tokens) for reference.
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
                <th className="sgl-num">Discount</th>
                <th>Pricing <span className="dim" style={{ textTransform: 'none', fontWeight: 400 }}>(USD/1M)</span></th>
                <th aria-label="Actions" />
              </tr>
            </thead>
            <tbody>
              {concreteModels.map((model) => {
                const r = routeFor(form.model_routes, model);
                const providers = r?.providers || [];
                const rpm = r?.rpm || '';
                const budget = r?.max_budget || '';
                const discount = r?.discount || '';
                const pricing = lookupPricing(model);
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
                    <td className="sgl-num mono">{discount ? `${discount}%` : <span className="dim">—</span>}</td>
                    <td className="mgf-pricing-cell"><InlinePricing pricing={pricing} /></td>
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
