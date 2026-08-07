import React, { useState, useEffect } from 'react';
import ModelIdCombobox from './ModelIdCombobox.jsx';
import ModelRouteConfigSection from './ModelRouteConfigSection.jsx';

// The four complexity tiers the router scores requests into, in ascending
// difficulty. Each maps to a concrete upstream model configured by the operator.
const TIERS = [
  {
    tier: 'simple',
    label: 'Simple',
    accent: 'success',
    range: '< 0.15',
    hint: 'Trivial or quick requests — greetings, single-fact lookups.',
  },
  {
    tier: 'medium',
    label: 'Medium',
    accent: 'accent',
    range: '0.15 – 0.35',
    hint: 'One clear generation task — summarize, write, translate.',
  },
  {
    tier: 'complex',
    label: 'Complex',
    accent: 'warning',
    range: '0.35 – 0.60',
    hint: 'Code, multi-step engineering, architecture design.',
  },
  {
    tier: 'reasoning',
    label: 'Reasoning',
    accent: 'danger',
    range: '> 0.60 · markers',
    hint: 'Deep analysis, math, proofs, concurrency correctness.',
  },
];

var routerShape = (tier) => ({
  tier,
  model: '',
  providers: [],
  strategy: '',
  priorities: [],
});

// EMPTY_ROUTER is the pristine form shape for a brand-new router.
export const EMPTY_ROUTER = {
  name: '',
  model_id: '',
  description: '',
  display_name: '',
  vision_bridge_model: '',
  enabled: true,
  pricing: { input_per_1m_usd: '', cached_input_per_1m_usd: '', cached_read_per_1m_usd: '', output_per_1m_usd: '' },
  mappings: TIERS.map((t) => routerShape(t.tier)),
};

// routerToForm normalizes a persisted router into the editable form shape so
// partial edits keep all four tiers present and contiguous.
export function routerToForm(r) {
  return {
    name: r?.name || '',
    model_id: r?.model_id || '',
    description: r?.description || '',
    display_name: r?.display_name || '',
    vision_bridge_model: r?.vision_bridge_model || '',
    enabled: r?.enabled ?? true,
    pricing: {
      input_per_1m_usd: r?.pricing?.input_per_1m_usd ?? '',
      cached_input_per_1m_usd: r?.pricing?.cached_input_per_1m_usd ?? '',
      cached_read_per_1m_usd: r?.pricing?.cached_read_per_1m_usd ?? '',
      output_per_1m_usd: r?.pricing?.output_per_1m_usd ?? '',
    },
    mappings: TIERS.map((t) => {
      const found = (r?.mappings || []).find((m) => (m.tier || '').toLowerCase() === t.tier);
      return {
        tier: t.tier,
        model: found?.model || '',
        providers: Array.isArray(found?.providers) ? [...found.providers] : [],
        strategy: found?.strategy || '',
        priorities: Array.isArray(found?.priorities)
          ? found.priorities.map((p) => ({ provider: p?.provider || '', priority: Number(p?.priority) || 0 }))
          : [],
      };
    }),
  };
}

// formToRouter converts the form into the API wire shape, dropping empty
// per-tier mappings and zero pricing so unmapped tiers are not persisted.
export function formToRouter(form) {
  const mappings = (form.mappings || [])
    .filter((m) => (m.model || '').trim() !== '')
    .map((m) => ({
      tier: m.tier,
      model: (m.model || '').trim(),
      providers: (m.providers || []).filter((p) => (p || '').trim() !== ''),
      strategy: (m.strategy || '').trim(),
      priorities: (m.priorities || [])
        .filter((p) => p && (p.provider || '').trim() !== '')
        .map((p) => ({ provider: (p.provider || '').trim(), priority: Number(p.priority) || 0 })),
    }));
  const num = (v) => {
    const n = Number(v);
    return Number.isFinite(n) && n > 0 ? n : null;
  };
  const inputPer = num(form.pricing?.input_per_1m_usd);
  const cachedPer = num(form.pricing?.cached_input_per_1m_usd);
  const cachedReadPer = num(form.pricing?.cached_read_per_1m_usd);
  const outputPer = num(form.pricing?.output_per_1m_usd);
  const hasPricing = inputPer != null || cachedPer != null || cachedReadPer != null || outputPer != null;
  return {
    name: (form.name || '').trim(),
    model_id: (form.model_id || '').trim(),
    description: (form.description || '').trim(),
    display_name: (form.display_name || '').trim(),
    vision_bridge_model: (form.vision_bridge_model || '').trim(),
    enabled: form.enabled !== false,
    pricing: !hasPricing ? null : {
      input_per_1m_usd: inputPer ?? 0,
      cached_input_per_1m_usd: cachedPer ?? 0,
      cached_read_per_1m_usd: cachedReadPer ?? 0,
      output_per_1m_usd: outputPer ?? 0,
    },
    mappings,
  };
}

// AutoRouterForm is the editable form for an Auto Router. It manages its own
// state from `initial` and reports every change upward via onChange (which is
// called with the raw editable form shape). Use routerToForm / formToRouter to
// convert between the API shape and this form shape.
export default function AutoRouterForm({ initial, onChange }) {
  const [form, setForm] = useState(() => (initial ? routerToForm(initial) : { ...EMPTY_ROUTER }));

  useEffect(() => {
    setForm(initial ? routerToForm(initial) : { ...EMPTY_ROUTER });
  }, [initial]);

  function set(field, value) {
    setForm((f) => {
      const next = { ...f, [field]: value };
      onChange?.(next);
      return next;
    });
  }

  function setMapping(i, field, value) {
    setForm((f) => {
      const next = {
        ...f,
        mappings: f.mappings.map((m, idx) => (idx === i ? { ...m, [field]: value } : m)),
      };
      onChange?.(next);
      return next;
    });
  }

  // setTierRoute merges a per-model route (providers/strategy/priorities) from
  // ModelRouteConfigSection into the given tier's mapping.
  function setTierRoute(i, route) {
    setForm((f) => {
      const next = {
        ...f,
        mappings: f.mappings.map((m, idx) => (
          idx === i
            ? {
                ...m,
                providers: Array.isArray(route.providers) ? route.providers : m.providers,
                strategy: route.strategy || '',
                priorities: Array.isArray(route.priorities) ? route.priorities : m.priorities,
              }
            : m
        )),
      };
      onChange?.(next);
      return next;
    });
  }

  return (
    <div className="ar-page">
      {/* ---- Identity + pricing section ---- */}
      <section className="ar-modal__section">
        <header className="ar-modal__sectionhead">
          <span className="ar-modal__sectionnum">01</span>
          <div>
            <h4 className="ar-modal__sectiontitle">Identity</h4>
            <p className="ar-modal__sectionhint">How this router is named and called. Like any model, it
              gets its own requestable model id and pricing.</p>
          </div>
        </header>

        <div className="ar-form">
          <div className="ar-form__grid">
            <div className="ar-form__field">
              <label className="ar-form__label" htmlFor="ar-name">Name</label>
              <input
                id="ar-name"
                type="text"
                value={form.name}
                onChange={(e) => set('name', e.target.value)}
                placeholder="e.g. smart-router"
              />
            </div>

            <div className="ar-form__field">
              <label className="ar-form__label" htmlFor="ar-modelid">Model ID (client-facing)</label>
              <input
                id="ar-modelid"
                type="text"
                value={form.model_id}
                onChange={(e) => set('model_id', e.target.value)}
                placeholder="e.g. smart-router"
              />
              <div className="ar-form__hint">Requesters call this id (exactly as typed, no prefix); the router picks the tier model behind it.</div>
            </div>
          </div>

          <div className="ar-form__grid ar-form__grid--5">
            <div className="ar-form__field">
              <label className="ar-form__label" htmlFor="ar-display">Display name</label>
              <input
                id="ar-display"
                type="text"
                value={form.display_name}
                onChange={(e) => set('display_name', e.target.value)}
                placeholder="e.g. Smart Router"
              />
            </div>
            <div className="ar-form__field">
              <label className="ar-form__label" htmlFor="ar-in">Input $/1M</label>
              <input
                id="ar-in"
                type="number"
                min="0"
                step="0.000001"
                value={form.pricing.input_per_1m_usd}
                onChange={(e) => set('pricing', { ...form.pricing, input_per_1m_usd: e.target.value })}
                placeholder="0.00"
              />
            </div>
            <div className="ar-form__field">
              <label className="ar-form__label" htmlFor="ar-cached">Cached input $/1M</label>
              <input
                id="ar-cached"
                type="number"
                min="0"
                step="0.000001"
                value={form.pricing.cached_input_per_1m_usd}
                onChange={(e) => set('pricing', { ...form.pricing, cached_input_per_1m_usd: e.target.value })}
                placeholder="0.00"
              />
            </div>
            <div className="ar-form__field">
              <label className="ar-form__label" htmlFor="ar-cached-read">Cache read $/1M</label>
              <input
                id="ar-cached-read"
                type="number"
                min="0"
                step="0.000001"
                value={form.pricing.cached_read_per_1m_usd}
                onChange={(e) => set('pricing', { ...form.pricing, cached_read_per_1m_usd: e.target.value })}
                placeholder="0.00"
              />
            </div>
            <div className="ar-form__field">
              <label className="ar-form__label" htmlFor="ar-out">Output $/1M</label>
              <input
                id="ar-out"
                type="number"
                min="0"
                step="0.000001"
                value={form.pricing.output_per_1m_usd}
                onChange={(e) => set('pricing', { ...form.pricing, output_per_1m_usd: e.target.value })}
                placeholder="0.00"
              />
            </div>
          </div>

          <div className="ar-form__field">
            <label className="ar-form__label" htmlFor="ar-vision">Vision bridge model</label>
            <ModelIdCombobox
              value={form.vision_bridge_model}
              onChange={(val) => set('vision_bridge_model', val)}
              placeholder={form.vision_bridge_model || 'search live / available models…'}
              autoFocus={false}
            />
            <div className="ar-form__hint">
              Choose a model from the live / available Model Catalog. When a tier's target model doesn't
              support images, the request's image(s) are sent to this model for analysis, then the described
              text is passed to the tier model. Leave empty to disable.
            </div>
          </div>

          <div className="ar-form__field">
            <label className="ar-form__label" htmlFor="ar-desc">Description</label>
            <textarea
              id="ar-desc"
              rows={2}
              value={form.description}
              onChange={(e) => set('description', e.target.value)}
              placeholder="What this router is for…"
            />
          </div>

          <div className="ar-form__toggle">
            <input
              type="checkbox"
              id="ar-enabled"
              checked={form.enabled !== false}
              onChange={(e) => set('enabled', e.target.checked)}
            />
            <label htmlFor="ar-enabled">Enabled — resolve requests to this router at runtime</label>
          </div>
        </div>
      </section>

      {/* ---- Tier → model mapping section ---- */}
      <section className="ar-modal__section">
        <header className="ar-modal__sectionhead">
          <span className="ar-modal__sectionnum">02</span>
          <div>
            <h4 className="ar-modal__sectiontitle">Tier → model mapping</h4>
            <p className="ar-modal__sectionhint">Each complexity tier forwards the scored request to the
              model you choose. A tier left empty falls back to the next-lower mapped tier.</p>
          </div>
        </header>

        <div className="ar-tiers">
          {form.mappings.map((m, i) => {
            const meta = TIERS[i];
            return (
              <div
                className={`ar-tier ${(m.model || '').trim() ? 'ar-tier--mapped' : ''}`}
                key={m.tier}
              >
                <div className="ar-tier__head">
                  <div className="ar-tier__badgewrap">
                    <span className={`ar-tier__badge ar-tier__badge--${meta.accent}`}>{meta.label}</span>
                    <span className="ar-tier__range">{meta.range}</span>
                  </div>
                  <span className="ar-tier__status">
                    {(m.model || '').trim() ? 'mapped' : 'empty'}
                  </span>
                </div>

                <p className="ar-tier__hint">{meta.hint}</p>

                <div className="ar-tier__modelrow">
                  <label className="ar-form__label">Target model</label>
                  <ModelIdCombobox
                    value={m.model}
                    onChange={(val) => setMapping(i, 'model', val)}
                    placeholder={m.model || 'search available models…'}
                    autoFocus={false}
                  />
                </div>

                <div className="ar-tier__route">
                  {(m.model || '').trim() ? (
                    <ModelRouteConfigSection
                      model={m.model.trim()}
                      route={{
                        providers: m.providers || [],
                        strategy: m.strategy || '',
                        priorities: m.priorities || [],
                      }}
                      onChange={(route) => setTierRoute(i, route)}
                    />
                  ) : (
                    <div className="ar-tier__routemuted">
                      Pick a target model above to configure per-provider routing
                      (pin providers, priority, failover).
                    </div>
                  )}
                </div>
              </div>
            );
          })}
        </div>
      </section>
    </div>
  );
}
