import React, { useEffect, useMemo, useState } from 'react';
import { getGlobalModel, putGlobalModel, ApiError } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import ModelRouteConfigSection from './ModelRouteConfigSection.jsx';
import { Modal, Spinner, ErrorBanner } from './Primitives.jsx';

// GlobalModelModal — edit a model id's attributes AND pricing once, fanning
// the change out to every catalog row that shares the id (across providers).
//
// Why this exists: models_catalog is keyed on (id, provider), so editing one
// row does not touch the same model id served by another provider. Pricing is
// already global per model id (model_pricing.id), but the attribute columns
// (context length, modalities, official_provider, ...) are per-row. This modal
// issues a single PUT /models-catalog/global/:id that:
//   - applies every "Apply globally"-checked attribute to all matching rows
//     (omitted fields are left untouched), and
//   - writes the global pricing row.
//
// Flow:
//   - GET /models-catalog/global/:id on open → canonical attributes (seeded
//     from the first row ordered by provider) + the list of affected providers
//     + current pricing.
//   - Each attribute field has a checkbox (default on). Unchecked fields are
//     omitted from the PUT body so they are never nullified.
//   - Save → PUT, then onSaved(result) so the parent can reload + refresh the
//     summary stats.
export default function GlobalModelModal({ modelId, onClose, onSaved }) {
  const { data, error, loading, reload } = useAsync(() => getGlobalModel(modelId), [modelId]);

  // Form state for attributes. Each entry carries the value + an "apply"
  // flag. The apply flag is what controls whether the field is sent.
  const [form, setForm] = useState(null);
  const [pricing, setPricing] = useState(null);
  const [applyPricing, setApplyPricing] = useState(false);
  const [routing, setRouting] = useState(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');

  useEffect(() => {
    if (data && !form) {
      const c = data.canonical || {};
      // Seed user_defined from the canonical row when present, otherwise
      // default to true so the operator's intent is preserved on first open.
      // The Apply flag is a separate toggle the operator uses to scope the
      // branch to a single global edit.
      setForm({
        official_provider: { value: c.official_provider || '', apply: true },
        display_name: { value: c.display_name || '', apply: true },
        description: { value: c.description || '', apply: true },
        context_length: { value: String(c.context_length || 0), apply: true },
        max_completion_tokens: { value: String(c.max_completion_tokens || 0), apply: true },
        input_token_limit: { value: String(c.input_token_limit || 0), apply: true },
        output_token_limit: { value: String(c.output_token_limit || 0), apply: true },
        input_modalities: { value: (c.input_modalities || []).join('\n'), apply: true },
        output_modalities: { value: (c.output_modalities || []).join('\n'), apply: true },
        user_defined: { value: !!c.user_defined, apply: false },
      });
      const p = data.pricing;
      setPricing({
        input_per_1m_usd: String(p?.input_per_1m_usd ?? 0),
        output_per_1m_usd: String(p?.output_per_1m_usd ?? 0),
        cached_input_per_1m_usd: String(p?.cached_input_per_1m_usd ?? 0),
        cached_read_per_1m_usd: String(p?.cached_read_per_1m_usd ?? 0),
        reasoning_per_1m_usd: String(p?.reasoning_per_1m_usd ?? 0),
      });
      const r = data.routing;
      setRouting({
        providers: Array.isArray(r?.providers) ? r.providers : [],
        strategy: r?.strategy || '',
        priorities: Array.isArray(r?.priorities) ? r.priorities.map((pr) => ({ ...pr })) : [],
      });
    }
  }, [data, form]);

  function updateField(name, value) {
    setForm((f) => ({ ...f, [name]: { ...f[name], value } }));
  }
  function toggleApply(name) {
    setForm((f) => ({ ...f, [name]: { ...f[name], apply: !f[name].apply } }));
  }
  function updatePricing(name, value) {
    setPricing((p) => ({ ...p, [name]: value }));
    setApplyPricing(true);
  }
  // updateRouting receives the fresh route entry emitted by ModelRouteConfigSection.
  function updateRouting(next) {
    setRouting(next);
  }

  const providerCount = data?.provider_count ?? 0;
  const affectedRows = useMemo(() => data?.rows || [], [data]);

  async function handleSubmit(e) {
    e.preventDefault();
    if (saving) return;
    setSaving(true);
    setSaveError('');

    const body = {};
    const attrs = {};
    let anyAttr = false;
    for (const [name, entry] of Object.entries(form)) {
      if (!entry.apply) continue;
      anyAttr = true;
      if (name === 'input_modalities' || name === 'output_modalities') {
        attrs[name] = splitList(entry.value);
      } else if (name === 'user_defined') {
        attrs[name] = !!entry.value;
      } else if (['context_length', 'max_completion_tokens', 'input_token_limit', 'output_token_limit'].includes(name)) {
        attrs[name] = Number(entry.value) || 0;
      } else {
        attrs[name] = entry.value;
      }
    }
    if (anyAttr) body.attributes = attrs;
    // Routing is always submitted: whichever providers are pinned define the
    // route (empty = no pin → clears the global route; the default strategy).
    const route = routing || { providers: [], strategy: '', priorities: [] };
    body.routing = {
      providers: Array.isArray(route.providers) ? route.providers : [],
      strategy: route.strategy || '',
      priorities: Array.isArray(route.priorities) ? route.priorities : [],
    };
    if (applyPricing) {
      body.pricing = {
        input_per_1m_usd: Number(pricing.input_per_1m_usd) || 0,
        output_per_1m_usd: Number(pricing.output_per_1m_usd) || 0,
        cached_input_per_1m_usd: Number(pricing.cached_input_per_1m_usd) || 0,
        cached_read_per_1m_usd: Number(pricing.cached_read_per_1m_usd) || 0,
        reasoning_per_1m_usd: Number(pricing.reasoning_per_1m_usd) || 0,
      };
    }
    if (!body.attributes && !body.pricing && !body.routing) {
      setSaveError('Select at least one attribute to apply, edit the routing, or edit the pricing.');
      setSaving(false);
      return;
    }
    try {
      const result = await putGlobalModel(modelId, body);
      onSaved?.(result);
    } catch (err) {
      setSaveError(err.message || 'Save failed');
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal title={`Global edit — ${modelId}`} size="lg" onClose={onClose}>
      {loading && <Spinner label="Loading global model…" />}
      <ErrorBanner error={error} onRetry={reload} />
      {data && form && (
        <>
          {/* Affected-provider preview banner */}
          <div className="callout callout--info" style={{ marginBottom: 12 }}>
            <strong>This will update {providerCount} catalog row{providerCount === 1 ? '' : 's'}</strong> across{' '}
            {affectedRows.length} provider{affectedRows.length === 1 ? '' : 's'} that share the model id{' '}
            <code>{modelId}</code>.
            <div className="dim" style={{ marginTop: 4, fontSize: 12 }}>
              Uncheck "Apply" next to any field you do not want to overwrite.
              Pricing is always global per model id.
            </div>
          </div>

          {affectedRows.length > 0 && (
            <details style={{ marginBottom: 12 }}>
              <summary className="form__label" style={{ cursor: 'pointer' }}>
                Affected providers ({affectedRows.length})
              </summary>
              <div style={{ marginTop: 8 }}>
                {affectedRows.map((r, i) => (
                  <div key={`${r.provider}|${i}`} className="row row--between" style={{ padding: '2px 0' }}>
                    <span className="mono">{r.provider}{r.user_defined ? ' · user-defined' : ''}</span>
                    <span className="dim" style={{ fontSize: 12 }}>{r.official_provider || '—'}</span>
                  </div>
                ))}
              </div>
            </details>
          )}

          {saveError && <div className="error-banner">{saveError}</div>}
          <form onSubmit={handleSubmit}>
            {/* Identity (read-only: the model id is the global key) */}
            <div className="form__row">
              <label className="form__label">Model ID (global key)</label>
              <input type="text" value={modelId} disabled />
            </div>

            {/* Apply-globally attributes */}
            <div className="grid grid--2">
              <GlobalField label="Official provider" name="official_provider" form={form}
                onUpdate={updateField} onToggle={toggleApply} />
              <GlobalField label="Display name" name="display_name" form={form}
                onUpdate={updateField} onToggle={toggleApply} />
              <GlobalField label="Context length" name="context_length" type="number" form={form}
                onUpdate={updateField} onToggle={toggleApply} />
              <GlobalField label="Max completion tokens" name="max_completion_tokens" type="number" form={form}
                onUpdate={updateField} onToggle={toggleApply} />
              <GlobalField label="Input token limit" name="input_token_limit" type="number" form={form}
                onUpdate={updateField} onToggle={toggleApply} />
              <GlobalField label="Output token limit" name="output_token_limit" type="number" form={form}
                onUpdate={updateField} onToggle={toggleApply} />
            </div>
            <div className="form__row">
              <label className="form__label">Description</label>
              <textarea rows={2} value={form.description.value}
                onChange={(e) => updateField('description', e.target.value)} />
              <ApplyToggle checked={form.description.apply} onChange={() => toggleApply('description')} />
            </div>
            <div className="grid grid--2">
              <GlobalFieldTextarea label="Input modalities (one per line)" name="input_modalities" form={form}
                onUpdate={updateField} onToggle={toggleApply} placeholder="TEXT&#10;IMAGE" />
              <GlobalFieldTextarea label="Output modalities (one per line)" name="output_modalities" form={form}
                onUpdate={updateField} onToggle={toggleApply} placeholder="TEXT" />
            </div>

            {/* User-defined flag — per-row, but checked here so a single
                "Apply globally" PUT can align the user_defined state across
                every upstream provider that shares this model id. Without
                this, the auto-sync path keeps newly-ingested upstream rows
                marked user_defined=false even after a global edit. */}
            <div className="form__row">
              <label className="row gap-sm" style={{ cursor: 'pointer', fontSize: 13 }}>
                <input
                  type="checkbox"
                  checked={!!form.user_defined.value}
                  onChange={(e) => updateField('user_defined', e.target.checked)}
                  style={{ width: 'auto' }}
                />
                <span className="form__label" style={{ margin: 0 }}>
                  User-defined (skip registry validation)
                </span>
              </label>
              <div className="form__hint" style={{ marginTop: 2 }}>
                Toggle is shared across every upstream provider that serves this model id.
                Tick "Apply globally" below to fan it out.
              </div>
              <ApplyToggle checked={form.user_defined.apply} onChange={() => toggleApply('user_defined')} />
            </div>

            {/* Routing section — global per model id (mirrors a Models Group route) */}
            <div className="form__section" style={{ marginTop: 16 }}>
              <div className="row row--between" style={{ marginBottom: 4 }}>
                <label className="form__label">Routing (global per model id)</label>
                <span className="dim" style={{ fontSize: 12 }}>
                  {routing && routing.providers && routing.providers.length > 0
                    ? 'Pinned — will be saved'
                    : 'No pin — default routing'}
                </span>
              </div>
              <p className="form__hint" style={{ marginBottom: 12 }}>
                Pin this model id to a subset of upstream providers. Applies to every request for this
                model, like a Models Group per-model route. A per-API-key Models Group route takes
                precedence for keys that define one. Leave all providers unpinned to use default routing
                (clears any global route).
              </p>
              <ModelRouteConfigSection model={modelId} route={routing || { providers: [], strategy: '', priorities: [] }}
                onChange={updateRouting} />
            </div>

            {/* Pricing section — always global per model id */}
            <div className="form__section" style={{ marginTop: 16 }}>
              <div className="row row--between" style={{ marginBottom: 4 }}>
                <label className="form__label">Pricing (global per model id)</label>
                <span className="dim" style={{ fontSize: 12 }}>
                  {applyPricing ? 'Will be saved' : 'No changes'}
                </span>
              </div>
              <p className="form__hint" style={{ marginBottom: 12 }}>
                USD per 1,000,000 tokens. Pricing is keyed by model id, so it applies to every
                provider serving this model.
              </p>
              <div className="grid grid--2">
                <PricingField label="Input / 1M" name="input_per_1m_usd" pricing={pricing} onUpdate={updatePricing} />
                <PricingField label="Output / 1M" name="output_per_1m_usd" pricing={pricing} onUpdate={updatePricing} />
                <PricingField label="Cached input (cache creation) / 1M" name="cached_input_per_1m_usd" pricing={pricing} onUpdate={updatePricing} />
                <PricingField label="Cached read / 1M" name="cached_read_per_1m_usd" pricing={pricing} onUpdate={updatePricing} />
                <PricingField label="Reasoning / 1M" name="reasoning_per_1m_usd" pricing={pricing} onUpdate={updatePricing} />
              </div>
            </div>

            <div className="form__actions">
              <button type="button" onClick={onClose} disabled={saving}>Cancel</button>
              <button type="submit" className="primary" disabled={saving}>
                {saving ? 'Saving…' : 'Apply globally'}
              </button>
            </div>
          </form>
        </>
      )}
    </Modal>
  );
}

// GlobalField renders a single attribute input + its "Apply" checkbox. The
// checkbox controls whether the field is included in the PUT body.
function GlobalField({ label, name, type = 'text', form, onUpdate, onToggle }) {
  const entry = form[name] || { value: '', apply: true };
  return (
    <div className="form__row">
      <label className="form__label">{label}</label>
      <input
        type={type} min={type === 'number' ? 0 : undefined} value={entry.value}
        onChange={(e) => onUpdate(name, e.target.value)}
        placeholder={label}
      />
      <ApplyToggle checked={entry.apply} onChange={() => onToggle(name)} />
    </div>
  );
}

function GlobalFieldTextarea({ label, name, form, onUpdate, onToggle, placeholder }) {
  const entry = form[name] || { value: '', apply: true };
  return (
    <div className="form__row">
      <label className="form__label">{label}</label>
      <textarea rows={2} value={entry.value}
        onChange={(e) => updateSafe(onUpdate, name, e.target.value)}
        placeholder={placeholder} />
      <ApplyToggle checked={entry.apply} onChange={() => onToggle(name)} />
    </div>
  );
}

// updateSafe wraps onUpdate so textarea handlers share the same call shape as
// GlobalField without risking an undefined name binding.
function updateSafe(onUpdate, name, value) {
  onUpdate(name, value);
}

function PricingField({ label, name, pricing, onUpdate }) {
  return (
    <div className="form__row">
      <label className="form__label">{label}</label>
      <input type="number" step="0.000001" min="0" value={pricing?.[name] ?? '0'}
        onChange={(e) => onUpdate(name, e.target.value)} />
    </div>
  );
}

function ApplyToggle({ checked, onChange }) {
  return (
    <label className="row gap-sm" style={{ cursor: 'pointer', marginTop: 4, fontSize: 12 }}>
      <input type="checkbox" checked={checked} onChange={onChange} style={{ width: 'auto' }} />
      <span className="dim">Apply globally</span>
    </label>
  );
}

function splitList(s) {
  if (!s) return [];
  return s.split('\n').map((line) => line.trim()).filter(Boolean);
}

export { ApiError };
