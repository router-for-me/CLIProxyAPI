import React, { useState, useEffect } from 'react';
import ModelIdCombobox from './ModelIdCombobox.jsx';
import ModelRouteConfigSection from './ModelRouteConfigSection.jsx';

// The four complexity tiers the router scores requests into, in ascending
// difficulty. Each maps to one or more concrete upstream models configured by
// the operator (a single target, or several targets spread by weight).
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

// TARGET_STRATEGIES drives the per-tier segmented control that decides how the
// tier's multiple target models are picked per request. A single target always
// applies; the control only appears once a second target is added.
const TARGET_STRATEGIES = [
  {
    value: '',
    label: 'Weighted',
    short: 'weighted',
    blurb: 'Pick a target model at random, weighted by each model’s weight (higher weight = more requests).',
  },
  {
    value: 'priority',
    label: 'Priority',
    short: 'priority',
    blurb: 'Always pick the highest-weight target model; ties resolve to the first listed. No randomness.',
  },
];

var routerShape = (tier) => ({
  tier,
  targets: [{ model: '', weight: 1, route: emptyRoute() }],
  target_strategy: '',
  providers: [],
  strategy: '',
  priorities: [],
});

// emptyRoute is a per-target route entry: when all empty the target inherits
// the tier's default routing.
function emptyRoute() {
  return { providers: [], strategy: '', priorities: [] };
}

// targetHasAnyRoute reports whether a target carries explicit routing of its
// own. A target with no route values inherits the tier default (or the model's
// default providers + global strategy when the tier has no default). Exported
// for tests.
export function targetHasAnyRoute(t) {
  const r = t?.route || {};
  return (r.providers || []).length > 0
    || (r.strategy || '').trim() !== ''
    || (r.priorities || []).length > 0;
}

// RANK_GLYPHS/rankGlyph render the 1-based slot number of a tier target (①, ②,
// …), mirroring the rank markers used by ModelRouteConfigSection.
const RANK_GLYPHS = ['①', '②', '③', '④', '⑤', '⑥', '⑦', '⑧', '⑨', '⑩'];

function rankGlyph(i) {
  return i < RANK_GLYPHS.length ? RANK_GLYPHS[i] : `${i + 1}`;
}

// EMPTY_ROUTER is the pristine form shape for a brand-new router.
export const EMPTY_ROUTER = {
  name: '',
  model_id: '',
  description: '',
  display_name: '',
  vision_bridge_model: '',
  jev_enabled: false,
  jev_min_confidence: '',
  jev_timeout_ms: '',
  jev_model_override: '',
  enabled: true,
  pricing: { input_per_1m_usd: '', cached_input_per_1m_usd: '', cached_read_per_1m_usd: '', output_per_1m_usd: '' },
  mappings: TIERS.map((t) => routerShape(t.tier)),
};

// persistedTargets normalizes a persisted tier mapping's target models into the
// form's target list, carrying each target's own per-model routing (when
// present). Multi-target mappings come back as `targets`; legacy single-target
// mappings as `model`. Always yields at least one row so the primary target
// combobox is visible.
function persistedTargets(found) {
  if (Array.isArray(found?.targets)) {
    const stored = found.targets.filter((x) => x && (x.model || '').trim() !== '');
    if (stored.length > 0) {
      return stored.map((x) => ({
        model: x.model || '',
        weight: Number(x.weight) > 0 ? Number(x.weight) : 1,
        route: {
          providers: Array.isArray(x.providers) ? [...x.providers] : [],
          strategy: x.strategy || '',
          priorities: Array.isArray(x.priorities)
            ? x.priorities.map((p) => ({ provider: p?.provider || '', priority: Number(p?.priority) || 0 }))
            : [],
        },
      }));
    }
  }
  if ((found?.model || '').trim() !== '') {
    return [{ model: found.model, weight: 1, route: emptyRoute() }];
  }
  return [{ model: '', weight: 1, route: emptyRoute() }];
}

// routerToForm normalizes a persisted router into the editable form shape so
// partial edits keep all four tiers present and contiguous.
export function routerToForm(r) {
  return {
    name: r?.name || '',
    model_id: r?.model_id || '',
    description: r?.description || '',
    display_name: r?.display_name || '',
    vision_bridge_model: r?.vision_bridge_model || '',
    jev_enabled: r?.jev_enabled ?? false,
    // 0 is the server's "unset" sentinel for these two, so render it blank
    // rather than as a literal 0 the operator would have to clear.
    jev_min_confidence: Number(r?.jev_min_confidence) > 0 ? String(r.jev_min_confidence) : '',
    jev_timeout_ms: Number(r?.jev_timeout_ms) > 0 ? String(r.jev_timeout_ms) : '',
    jev_model_override: r?.jev_model_override || '',
    enabled: r?.enabled ?? true,
    pricing: {
      input_per_1m_usd: r?.pricing?.input_per_1m_usd ?? '',
      cached_input_per_1m_usd: r?.pricing?.cached_input_per_1m_usd ?? '',
      cached_read_per_1m_usd: r?.pricing?.cached_read_per_1m_usd ?? '',
      output_per_1m_usd: r?.pricing?.output_per_1m_usd ?? '',
    },
    mappings: TIERS.map((t) => {
      const found = (r?.mappings || []).find((m) => (m.tier || '').toLowerCase() === t.tier);
      const targets = persistedTargets(found);
      const tierRoute = {
        providers: Array.isArray(found?.providers) ? [...found.providers] : [],
        strategy: found?.strategy || '',
        priorities: Array.isArray(found?.priorities)
          ? found.priorities.map((p) => ({ provider: p?.provider || '', priority: Number(p?.priority) || 0 }))
          : [],
      };
      // A single-target tier's routing IS the primary target's routing (the
      // runtime applies the tier route to the one picked target). Migrate the
      // tier-level route onto the primary target so the UI has a single place
      // to edit routing, and clear the now-redundant tier-level fields. A
      // target that already carries its own route is left untouched.
      const filledCount = targets.filter((x) => (x.model || '').trim() !== '').length;
      if (filledCount <= 1 && targets.length > 0 && !targetHasAnyRoute(targets[0])) {
        targets[0].route = {
          providers: [...tierRoute.providers],
          strategy: tierRoute.strategy,
          priorities: tierRoute.priorities.map((p) => ({ ...p })),
        };
        tierRoute.providers = [];
        tierRoute.strategy = '';
        tierRoute.priorities = [];
      }
      return {
        tier: t.tier,
        targets,
        target_strategy: found?.target_strategy || '',
        providers: tierRoute.providers,
        strategy: tierRoute.strategy,
        priorities: tierRoute.priorities,
      };
    }),
  };
}

// formToRouter converts the form into the API wire shape, dropping empty
// per-tier mappings and zero pricing so unmapped tiers are not persisted.
// A single-target tier is emitted in the legacy `model` form with the primary
// target's route lifted to the tier-level providers/strategy/priorities (the
// runtime applies them to the one picked target — identical outcome to storing
// them on the target). A multi-target tier emits `targets` (+ `target_strategy`)
// with `model` kept as the first target for display/back-compat. Each multi
// target emits its own providers/strategy/priorities only when it differs from
// the tier-level default routing; otherwise the runtime inherits it.
export function formToRouter(form) {
  const mappings = (form.mappings || [])
    .map((m) => {
      const tierProviders = (m.providers || []).filter((p) => (p || '').trim() !== '');
      const tierStrategy = (m.strategy || '').trim();
      const tierPriorities = (m.priorities || [])
        .filter((p) => p && (p.provider || '').trim() !== '')
        .map((p) => ({ provider: (p.provider || '').trim(), priority: Number(p.priority) || 0 }));
      const targets = (m.targets || [])
        .map((x) => {
          const routeProviders = (x.route?.providers || []).filter((p) => (p || '').trim() !== '');
          const routeStrategy = (x.route?.strategy || '').trim();
          const routePriorities = (x.route?.priorities || [])
            .filter((p) => p && (p.provider || '').trim() !== '')
            .map((p) => ({ provider: (p.provider || '').trim(), priority: Number(p.priority) || 0 }));
          const target = {
            model: (x.model || '').trim(),
            weight: Math.max(1, Math.trunc(Number(x.weight)) || 1),
          };
          // Every target persists its own explicit routing: a target added via
          // "+ Add target" always carries a seeded custom route, so it is never
          // silently collapsed back to the tier default. Targets with a cleared
          // (empty) route emit no fields and inherit at runtime.
          if (routeProviders.length > 0) target.providers = routeProviders;
          if (routeStrategy !== '') target.strategy = routeStrategy;
          if (routePriorities.length > 0) target.priorities = routePriorities;
          return target;
        })
        .filter((x) => x.model !== '');
      if (targets.length === 0) return null;
      // Single-target tier: the primary target's route is the tier's routing.
      // Lift it to the legacy tier-level fields so the wire format (and the
      // runtime) stay unchanged. The mapped target carries its route flattened
      // onto providers/strategy/priorities (the .map above strips .route).
      if (targets.length === 1) {
        const t0 = targets[0];
        const out = { tier: m.tier, model: t0.model };
        const provs = (t0.providers && t0.providers.length ? t0.providers : (t0.route?.providers || []))
          .filter((p) => (p || '').trim() !== '');
        const strat = (t0.strategy || t0.route?.strategy || '').trim();
        const prios = (t0.priorities && t0.priorities.length ? t0.priorities : (t0.route?.priorities || []))
          .filter((p) => p && (p.provider || '').trim() !== '')
          .map((p) => ({ provider: (p.provider || '').trim(), priority: Number(p.priority) || 0 }));
        if (provs.length > 0) out.providers = provs;
        if (strat !== '') out.strategy = strat;
        if (prios.length > 0) out.priorities = prios;
        return out;
      }
      const target_strategy = (m.target_strategy || '').trim() === 'priority' ? 'priority' : '';
      const out = {
        tier: m.tier,
        model: targets[0].model,
        providers: tierProviders,
        strategy: tierStrategy,
        priorities: tierPriorities,
        targets,
        target_strategy,
      };
      return out;
    })
    .filter(Boolean);
  const num = (v) => {
    const n = Number(v);
    return Number.isFinite(n) && n > 0 ? n : null;
  };
  // The server treats 0 as "unset, use the default" for both classifier knobs,
  // so a blank or invalid entry must serialize as 0 rather than NaN.
  const fractionOrZero = (v) => {
    const n = Number(v);
    return Number.isFinite(n) && n > 0 ? Math.min(n, 1) : 0;
  };
  const intOrZero = (v) => {
    const n = Math.round(Number(v));
    return Number.isFinite(n) && n > 0 ? n : 0;
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
    // The classifier is opt-in per router; its knobs are only meaningful when
    // it is on, so they are zeroed (the server's "use the default" sentinel)
    // whenever it is off. That keeps a disabled router's stored row free of
    // stale tuning values that would silently apply if it were re-enabled.
    jev_enabled: !!form.jev_enabled,
    jev_min_confidence: fractionOrZero(form.jev_min_confidence),
    jev_timeout_ms: intOrZero(form.jev_timeout_ms),
    jev_model_override: (form.jev_model_override || '').trim(),
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
  // expandedTargets tracks, per tier index, which per-target route editors are
  // open (Set of target indexes). Kept as local UI state only — never persisted.
  const [expandedTargets, setExpandedTargets] = useState({});

  // Reset the form whenever the persisted router changes (initial is a fresh
  // object from the API after save/reload).
  useEffect(() => {
    setForm(initial ? routerToForm(initial) : { ...EMPTY_ROUTER });
    setExpandedTargets({});
  }, [initial]);

  // Report every committed form change to the parent. Calling onChange here —
  // AFTER the state has been committed — guarantees the parent's form snapshot
  // is always exactly the latest internal form. (Calling it inside the setForm
  // updater is impure: React may run updaters more than once or discard them,
  // which can leave the parent with a stale form and silently drop edits on
  // save.)
  const onChangeRef = React.useRef(onChange);
  useEffect(() => {
    onChangeRef.current = onChange;
  }, [onChange]);
  useEffect(() => {
    onChangeRef.current?.(form);
  }, [form]);

  function toggleTargetRoute(i, ti) {
    setExpandedTargets((prev) => {
      const tier = prev[i] ? new Set(prev[i]) : new Set();
      if (tier.has(ti)) tier.delete(ti);
      else tier.add(ti);
      return { ...prev, [i]: tier };
    });
  }

  function isTargetRouteOpen(i, ti) {
    return !!(expandedTargets[i] && expandedTargets[i].has(ti));
  }

  function set(field, value) {
    setForm((f) => ({ ...f, [field]: value }));
  }

  function setMapping(i, field, value) {
    setForm((f) => ({
      ...f,
      mappings: f.mappings.map((m, idx) => (idx === i ? { ...m, [field]: value } : m)),
    }));
  }

  function setTarget(i, ti, field, value) {
    setForm((f) => ({
      ...f,
      mappings: f.mappings.map((m, idx) => (
        idx === i
          ? { ...m, targets: m.targets.map((t, tIdx) => (tIdx === ti ? { ...t, [field]: value } : t)) }
          : m
      )),
    }));
  }

  // addTarget appends a new target row to a tier. The new target always starts
  // with an empty route so it inherits whatever the tier routing section
  // resolves to (the tier default if one is configured, otherwise the model's
  // default providers). Seeding the route with the primary target's pins
  // made "pinned providers count = N+1" misleading: every operator-visible
  // count of pinned providers included the silently-pre-selected seed that the
  // operator never actually chose. Start empty so the on-screen pin count
  // matches what the operator genuinely selected.
  function addTarget(i) {
    const mapping = form.mappings[i] || { targets: [], providers: [], strategy: '', priorities: [] };
    const newIndex = (mapping.targets || []).length;
    setForm((f) => ({
      ...f,
      mappings: f.mappings.map((m, idx) => (
        idx === i ? { ...m, targets: [...m.targets, { model: '', weight: 1, route: emptyRoute() }] } : m
      )),
    }));
    // Auto-expand the new target's route editor only once it has a model
    // selected (route editor is gated on `filled`). For now mark it open so the
    // operator sees the inheritance hint immediately.
    setExpandedTargets((prev) => {
      const tier = prev[i] ? new Set(prev[i]) : new Set();
      tier.add(newIndex);
      return { ...prev, [i]: tier };
    });
  }

  function removeTarget(i, ti) {
    setForm((f) => ({
      ...f,
      mappings: f.mappings.map((m, idx) => (
        idx === i
          ? { ...m, targets: m.targets.length > 1 ? m.targets.filter((_, tIdx) => tIdx !== ti) : m.targets }
          : m
      )),
    }));
  }

  // setTargetRoute merges a per-model route (providers/strategy/priorities) from
  // ModelRouteConfigSection into the given target's own routing.
  function setTargetRoute(i, ti, route) {
    setForm((f) => ({
      ...f,
      mappings: f.mappings.map((m, idx) => (
        idx === i
          ? {
              ...m,
              targets: m.targets.map((t, tIdx) => (
                tIdx === ti
                  ? {
                      ...t,
                      route: {
                        providers: Array.isArray(route.providers) ? route.providers : [],
                        strategy: route.strategy || '',
                        priorities: Array.isArray(route.priorities) ? route.priorities : [],
                      },
                    }
                  : t
              )),
            }
          : m
      )),
    }));
  }

  // setTierRoute merges a per-model route (providers/strategy/priorities) from
  // ModelRouteConfigSection into the given tier's mapping.
  function setTierRoute(i, route) {
    setForm((f) => ({
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
    }));
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
            <label className="ar-form__label" htmlFor="ar-jev-enabled" style={{ display: 'flex', alignItems: 'center', gap: 8, cursor: 'pointer' }}>
              <input
                id="ar-jev-enabled"
                type="checkbox"
                checked={!!form.jev_enabled}
                onChange={(e) => set('jev_enabled', e.target.checked)}
                style={{ width: 'auto' }}
              />
              <span>Jev AI classification for this router</span>
            </label>
            <div className="ar-form__hint">
              When on — and the global Jev AI switches are on in Settings — this router consults the
              classifier before scoring and routes by the tier it picks, as long as its confidence clears
              the threshold below. Any failure or a low-confidence answer falls back to the heuristic tier,
              which is still recorded for comparison.
            </div>
          </div>

          {form.jev_enabled && (
            <div className="ar-form__grid" style={{ display: 'grid', gridTemplateColumns: 'repeat(auto-fit, minmax(180px, 1fr))', gap: 12 }}>
              <div className="ar-form__field">
                <label className="ar-form__label" htmlFor="ar-jev-conf">Minimum confidence</label>
                <input
                  id="ar-jev-conf"
                  type="number"
                  min="0"
                  max="1"
                  step="0.05"
                  value={form.jev_min_confidence}
                  onChange={(e) => set('jev_min_confidence', e.target.value)}
                  placeholder="0.5"
                />
                <div className="ar-form__hint">0–1. Below this, the heuristic tier wins.</div>
              </div>
              <div className="ar-form__field">
                <label className="ar-form__label" htmlFor="ar-jev-timeout">Timeout (ms)</label>
                <input
                  id="ar-jev-timeout"
                  type="number"
                  min="1"
                  step="50"
                  value={form.jev_timeout_ms}
                  onChange={(e) => set('jev_timeout_ms', e.target.value)}
                  placeholder="400"
                />
                <div className="ar-form__hint">Blank uses the 400 ms default.</div>
              </div>
              <div className="ar-form__field">
                <label className="ar-form__label" htmlFor="ar-jev-model">Classifier model override</label>
                <input
                  id="ar-jev-model"
                  type="text"
                  value={form.jev_model_override}
                  onChange={(e) => set('jev_model_override', e.target.value)}
                  placeholder="jev-1.13.0"
                  spellCheck={false}
                />
                <div className="ar-form__hint">Blank uses the model from Settings.</div>
              </div>
            </div>
          )}

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
              model(s) you choose. A single target carries the tier’s routing directly; add more targets to
              spread traffic across models and route each individually (or inherit a tier default). A tier
              left empty falls back to the next-lower mapped tier.</p>
          </div>
        </header>

        <div className="ar-tiers">
          {form.mappings.map((m, i) => {
            const meta = TIERS[i];
            const tierRoute = {
              providers: m.providers || [],
              strategy: m.strategy || '',
              priorities: m.priorities || [],
            };
            const mapped = (m.targets || []).some((t) => (t.model || '').trim() !== '');
            // A tier is "multi" only once it actually maps more than one model,
            // so an extra empty target row does not flip the UI into multi mode.
            const filledTargets = (m.targets || []).filter((t) => (t.model || '').trim() !== '');
            const multi = filledTargets.length > 1;
            const primaryModel = filledTargets[0]?.model || '';
            const targetStrategy = (m.target_strategy || '').trim() === 'priority' ? 'priority' : '';
            const activeStrategy = TARGET_STRATEGIES.find((o) => o.value === targetStrategy) || TARGET_STRATEGIES[0];
            const hasDefaultRoute = tierRoute.providers.length > 0
              || tierRoute.strategy !== ''
              || tierRoute.priorities.length > 0;
            const totalWeight = filledTargets.reduce((s, t) => s + (Number(t.weight) > 0 ? Number(t.weight) : 1), 0);
            // The tier default routing is only meaningful for multi-target
            // tiers. A single-target tier's routing IS the primary target's
            // routing — it is edited directly on the target, so the separate
            // "Routing" section below is dropped entirely.
            const inheritCount = filledTargets.filter((t) => !targetHasAnyRoute(t)).length;
            const hasEmptyTargetRow = (m.targets || []).some((t) => (t.model || '').trim() === '');
            const showDefaultRoute = multi && (inheritCount > 0 || hasEmptyTargetRow);
            return (
              <div
                className={`ar-tier ar-tier--${meta.accent} ${mapped ? 'ar-tier--mapped' : ''}`}
                key={m.tier}
              >
                <div className="ar-tier__head">
                  <div className="ar-tier__badgewrap">
                    <span className={`ar-tier__badge ar-tier__badge--${meta.accent}`}>{meta.label}</span>
                    <span className="ar-tier__range">{meta.range}</span>
                  </div>
                  <span className="ar-tier__status">
                    {mapped ? `mapped${multi ? ` · ${filledTargets.length} targets` : ''}` : 'empty'}
                  </span>
                </div>

                <p className="ar-tier__hint">{meta.hint}</p>

                <div className="ar-targets">
                  {(m.targets || []).map((t, ti) => {
                    const filled = (t.model || '').trim() !== '';
                    const weight = Number(t.weight) > 0 ? Number(t.weight) : 1;
                    const share = totalWeight > 0 ? Math.round((weight / totalWeight) * 100) : 0;
                    const custom = targetHasAnyRoute(t);
                    const open = isTargetRouteOpen(i, ti);
                    return (
                      <div className={`ar-target-block ${filled ? 'ar-target-block--filled' : ''}`} key={ti}>
                        <div className="ar-target">
                          <div className={`ar-target__rank ar-target__rank--${meta.accent}`} aria-hidden="true">{rankGlyph(ti)}</div>
                          <div className="ar-target__field">
                            <label className="ar-form__label">
                              {ti === 0 ? 'Primary target' : `Target ${ti + 1}`}
                            </label>
                            <ModelIdCombobox
                              value={t.model}
                              onChange={(val) => setTarget(i, ti, 'model', val)}
                              placeholder={t.model || (ti === 0 ? 'search available models…' : 'add another model…')}
                              autoFocus={false}
                            />
                          </div>
                          {multi && (
                            <div className="ar-target__weight">
                              <label className="ar-form__label" htmlFor={`ar-weight-${m.tier}-${ti}`}>Weight</label>
                              <div className="ar-target__weightrow">
                                <input
                                  id={`ar-weight-${m.tier}-${ti}`}
                                  type="number"
                                  min={1}
                                  step={1}
                                  value={t.weight}
                                  onChange={(e) => setTarget(i, ti, 'weight', Math.max(1, parseInt(e.target.value, 10) || 1))}
                                  aria-label={`Weight for target ${ti + 1} of ${m.tier} tier`}
                                />
                                <span className="ar-target__share" aria-hidden="true">{filled ? `${share}%` : '—'}</span>
                              </div>
                            </div>
                          )}
                          <div className="ar-target__meta">
                            {filled && multi && (
                              <span className={`ar-target__pill ${custom ? 'ar-target__pill--custom' : ''}`}>
                                {custom ? 'custom route' : 'no route · inherit'}
                              </span>
                            )}
                            {multi && (
                              <button
                                type="button"
                                className="row-actions__btn ar-target__toggle"
                                onClick={() => toggleTargetRoute(i, ti)}
                                aria-expanded={open}
                                aria-label={`${open ? 'Hide' : 'Show'} routing for target ${ti + 1}`}
                                disabled={!filled}
                              >
                                {open ? '▾' : '▸'} Route
                              </button>
                            )}
                            {(m.targets || []).length > 1 && (
                              <button
                                type="button"
                                className="row-actions__btn ar-target__remove"
                                onClick={() => removeTarget(i, ti)}
                                aria-label={`Remove target ${ti + 1} from ${m.tier} tier`}
                              >Remove</button>
                            )}
                          </div>
                        </div>
                        {filled && (!multi || open) && (
                          <div className="ar-target__route">
                            <div className="ar-target__routelabel">
                              {multi ? (
                                <>Route for <span className="mono">{t.model.trim()}</span> — leave unset to inherit the tier default below</>
                              ) : (
                                <>Routing for <span className="mono">{t.model.trim()}</span> — this tier’s per-provider routing</>
                              )}
                            </div>
                            <ModelRouteConfigSection
                              model={t.model.trim()}
                              route={t.route || { providers: [], strategy: '', priorities: [] }}
                              onChange={(route) => setTargetRoute(i, ti, route)}
                            />
                          </div>
                        )}
                      </div>
                    );
                  })}
                </div>

                {multi && (
                  <div className="ar-targets__distribution">
                    <div className="ar-targets__distbar" role="img" aria-label={`${targetStrategy === 'priority' ? 'Priority' : 'Weighted'} distribution across ${filledTargets.length} targets`}>
                      {filledTargets.map((t, ti) => {
                        const weight = Number(t.weight) > 0 ? Number(t.weight) : 1;
                        return <span key={ti} className={`ar-targets__distseg ar-targets__distseg--${meta.accent}`} style={{ flexGrow: weight }} title={`${t.model} — weight ${weight}`} />;
                      })}
                    </div>
                    <div className="ar-targets__distlegend">
                      {filledTargets.map((t, ti) => {
                        const weight = Number(t.weight) > 0 ? Number(t.weight) : 1;
                        const pct = totalWeight > 0 ? Math.round((weight / totalWeight) * 100) : 0;
                        // Under priority, the highest weight wins; ties break to
                        // the first listed target, mirroring the resolver.
                        const maxWeight = Math.max(...filledTargets.map((x) => (Number(x.weight) > 0 ? Number(x.weight) : 1)));
                        const isWinner = targetStrategy === 'priority'
                          && weight === maxWeight
                          && filledTargets.findIndex((x) => (Number(x.weight) > 0 ? Number(x.weight) : 1) === maxWeight) === ti;
                        return (
                          <span key={ti} className="ar-targets__distlegend-item">
                            <span className={`ar-targets__distslot ar-targets__distslot--${meta.accent}`}>{rankGlyph(ti)}</span>
                            <span className="mono ar-targets__distmodel">{t.model.trim()}</span>
                            {targetStrategy === 'priority'
                              ? <span className={`ar-targets__distpct ${isWinner ? '' : 'muted'}`}>{isWinner ? 'pick #1' : '—'}</span>
                              : <span className="ar-targets__distpct muted">{pct}%</span>}
                          </span>
                        );
                      })}
                    </div>
                  </div>
                )}

                <div className="ar-targets__actions">
                  <button
                    type="button"
                    className="row-actions__btn"
                    onClick={() => addTarget(i)}
                    aria-label={`Add a target model to the ${m.tier} tier`}
                  >+ Add target</button>
                  {multi && (
                    <div className="ar-targets__strategy">
                      <div className="seg" role="group" aria-label={`Target selection strategy for ${m.tier} tier`}>
                        {TARGET_STRATEGIES.map((opt) => {
                          const active = (targetStrategy || '') === opt.value;
                          return (
                            <button
                              key={opt.value || 'weighted'}
                              type="button"
                              className={`seg__btn ${active ? 'seg__btn--active' : ''}`}
                              onClick={() => setMapping(i, 'target_strategy', opt.value)}
                              title={opt.blurb}
                              aria-pressed={active}
                            >
                              {opt.label}
                            </button>
                          );
                        })}
                      </div>
                      <div className="model-routes__strategyblurb muted">{activeStrategy.blurb}</div>
                    </div>
                  )}
                </div>

                {showDefaultRoute && (
                  <div className="ar-tier__defaultroute">
                    <div className="ar-tier__routelabel">
                      <span className={`ar-tier__defroute-chevron ${hasDefaultRoute ? '' : 'ar-tier__defroute-chevron--off'}`} aria-hidden="true">{hasDefaultRoute ? '●' : '○'}</span>
                      {multi ? 'Default routing' : 'Routing'}
                      {multi && inheritCount > 0 && (
                        <span className="ar-tier__defroute-sub"> — applied to {inheritCount} target{inheritCount === 1 ? '' : 's'} without their own</span>
                      )}
                      {multi && inheritCount === 0 && (
                        <span className="ar-tier__defroute-sub"> — used by targets without their own</span>
                      )}
                      {!multi && hasDefaultRoute && (
                        <span className="ar-tier__defroute-sub"> — this tier’s per-provider routing</span>
                      )}
                    </div>
                    {primaryModel ? (
                      <ModelRouteConfigSection
                        model={primaryModel}
                        route={tierRoute}
                        onChange={(route) => setTierRoute(i, route)}
                      />
                    ) : (
                      <div className="ar-tier__routemuted">
                        Pick a target model above to configure per-provider routing
                        (pin providers, priority, failover).
                      </div>
                    )}
                  </div>
                )}
              </div>
            );
          })}
        </div>
      </section>
    </div>
  );
}
