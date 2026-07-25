import React, { useState, useEffect } from 'react';
import ModelMultiSelect from './ModelMultiSelect.jsx';
import { getModelProviders, listUpstreamProviders } from '../api/client.js';

// PolicyForm — reusable form for editing a Policy object.
//
// Used in two places:
//   1. The ApiKeyDetailPage "Edit Policy" panel (edit existing policy).
//   2. The ApiKeysPage "New API Key" modal (attach a policy at creation time).
//
// The component is controlled: callers receive the up-to-date policy object
// via onChange. Submit is delegated to the parent — this keeps worries about
// API calls (POST vs PUT) out of the form itself.
//
// Model allow/blacklist fields are arrays of strings (model IDs or wildcard
// tokens like `gpt-4*`). The ModelMultiSelect component loads the available
// models from /v0/management/models-catalog and lets the operator pick or
// type custom tokens.
const EMPTY_POLICY = {
  rpm_limit: '',
  hourly_rate_limit: '',
  budget_hourly_usd: '',
  budget_weekly_usd: '',
  budget_monthly_usd: '',
  max_parallel_requests: '',
  allowed_models: [],
  blocked_models: [],
  model_routes: [],
};

export function policyToForm(policy) {
  if (!policy) return { ...EMPTY_POLICY };
  return {
    rpm_limit: policy.rpm_limit ?? '',
    hourly_rate_limit: policy.hourly_rate_limit ?? '',
    budget_hourly_usd: policy.budget_hourly_usd ?? '',
    budget_weekly_usd: policy.budget_weekly_usd ?? '',
    budget_monthly_usd: policy.budget_monthly_usd ?? '',
    max_parallel_requests: policy.max_parallel_requests ?? '',
    allowed_models: Array.isArray(policy.allowed_models) ? [...policy.allowed_models] : [],
    blocked_models: Array.isArray(policy.blocked_models) ? [...policy.blocked_models] : [],
    model_routes: Array.isArray(policy.model_routes)
      ? policy.model_routes.map((r) => ({ model: r.model || '', providers: Array.isArray(r.providers) ? [...r.providers] : [] }))
      : [],
  };
}

export function formToPolicy(form, apiKeyId) {
  const policy = {
    api_key_id: apiKeyId || '',
    rpm_limit: numOrNull(form.rpm_limit),
    hourly_rate_limit: numOrNull(form.hourly_rate_limit),
    budget_hourly_usd: floatOrNull(form.budget_hourly_usd),
    budget_weekly_usd: floatOrNull(form.budget_weekly_usd),
    budget_monthly_usd: floatOrNull(form.budget_monthly_usd),
    max_parallel_requests: numOrNull(form.max_parallel_requests),
    allowed_models: dedupe(listFromField(form.allowed_models)),
    blocked_models: dedupe(listFromField(form.blocked_models)),
    // Only keep routes whose model is still in the allowed list and is a
    // concrete (non-wildcard) model id. Empty-provider routes are dropped
    // (they mean "use the registry default" — no need to persist).
    model_routes: (Array.isArray(form.model_routes) ? form.model_routes : [])
      .filter((r) => r && r.model && !r.model.endsWith('*') && form.allowed_models.includes(r.model) && Array.isArray(r.providers) && r.providers.length > 0)
      .map((r) => ({ model: r.model, providers: dedupe(r.providers) })),
  };
  return policy;
}

function numOrNull(s) {
  if (s === '' || s === null || s === undefined) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}
function floatOrNull(s) { return numOrNull(s); }
// Accept either an array of strings or a newline-joined textarea string so
// the helper stays resilient if a caller ever hands it raw text.
function listFromField(v) {
  if (Array.isArray(v)) return v.map((s) => String(s).trim()).filter(Boolean);
  if (typeof v === 'string') return v.split('\n').map((l) => l.trim()).filter(Boolean);
  return [];
}
function dedupe(arr) {
  return Array.from(new Set(arr));
}

export default function PolicyForm({ initial, onChange }) {
  const [form, setForm] = useState(() => policyToForm(initial));
  // Map of model id -> upstream providers that actually serve it (from the
  // in-memory registry). Fetched lazily per routable allowed model so the
  // routing editor only shows providers that back each model.
  const [providersByModel, setProvidersByModel] = useState({});
  // All known upstream providers' executor keys (provider_key) sourced from
  // the upstream_providers table. The per-model routing picker uses this so
  // operators can pin a model to an upstream even when no live auth is
  // currently registered for it — previously the picker only offered
  // providers that the in-memory registry reported as "currently serving",
  // which silently capped the available targets to a handful (typically the
  // first few live clients) and hid every disabled or mid-refresh
  // provider. Non-disabled rows only, since disabled providers cannot be
  // selected at runtime anyway.
  const [allUpstreamProviderKeys, setAllUpstreamProviderKeys] = useState(null);

  // Per-model routing helpers. Routes only apply to concrete (non-wildcard)
  // allowed models; an empty provider list means "use the registry default".
  const routableModels = form.allowed_models.filter((m) => m && !m.endsWith('*'));

  // Fetch the catalog of configured upstream providers once on mount. The
  // resulting set of provider_key strings is merged with each model's live
  // providers below so every upstream that could serve the model is offered
  // for pinning, not just the ones with live auths right now.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await listUpstreamProviders();
        const rows = Array.isArray(res?.providers) ? res.providers : [];
        const keys = rows
          .filter((r) => !r.disabled)
          .map((r) => (typeof r.provider_key === 'string' ? r.provider_key : ''))
          .filter((k) => k)
          .sort((a, b) => a.localeCompare(b));
        if (!cancelled) setAllUpstreamProviderKeys(keys);
      } catch {
        if (!cancelled) setAllUpstreamProviderKeys([]);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  // Fetch the serving providers for each routable allowed model whenever the
  // set changes. Results are merged into providersByModel.
  useEffect(() => {
    let cancelled = false;
    (async () => {
      const next = { ...providersByModel };
      await Promise.all(
        routableModels.map(async (model) => {
          if (next[model]) return; // already loaded
          try {
            const res = await getModelProviders(model);
            next[model] = Array.isArray(res?.providers) ? res.providers : [];
          } catch {
            next[model] = [];
          }
        }),
      );
      if (!cancelled) setProvidersByModel(next);
    })();
    return () => { cancelled = true; };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form.allowed_models.join('|')]);

  // routeProviderChoices returns the deduped, ordered set of upstream
  // provider keys the picker should offer for a model: live registry
  // providers first (sorted by availability), followed by any configured
  // upstream not already included. This guarantees the picker never shows
  // fewer targets than the operator has configured, even when an upstream
  // has not registered a live auth yet (or is temporarily disabled at the
  // registry level while still enabled in the catalog).
  function routeProviderChoices(model) {
    const live = providersByModel[model] || [];
    const seen = new Set();
    const out = [];
    for (const p of live) {
      const key = String(p || '').trim();
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(key);
    }
    for (const key of allUpstreamProviderKeys || []) {
      if (!key || seen.has(key)) continue;
      seen.add(key);
      out.push(key);
    }
    return out;
  }

  // Keep parent in sync whenever the form changes. We only call onChange with
  // the raw form — the parent decides when to convert via formToPolicy.
  useEffect(() => {
    onChange?.(form);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form]);

  function update(partial) {
    setForm((f) => ({ ...f, ...partial }));
  }

  function routeProvidersFor(model) {
    const r = form.model_routes.find((x) => x.model === model);
    return r && Array.isArray(r.providers) ? r.providers : [];
  }
  function toggleRouteProvider(model, provider) {
    setForm((f) => {
      const routes = f.model_routes.map((x) => ({ ...x, providers: [...x.providers] }));
      let entry = routes.find((x) => x.model === model);
      if (!entry) {
        entry = { model, providers: [] };
        routes.push(entry);
      }
      const idx = entry.providers.indexOf(provider);
      if (idx >= 0) entry.providers.splice(idx, 1);
      else entry.providers.push(provider);
      // Drop entries that ended up empty so "default" isn't persisted as a
      // zero-provider route (kept in-form only while it has providers).
      return { ...f, model_routes: routes.filter((x) => x.providers.length > 0) };
    });
  }

  return (
    <div>
      <div className="grid grid--2">
        <div className="form__row">
          <label className="form__label">RPM limit</label>
          <input type="number" min="0" value={form.rpm_limit}
            onChange={(e) => update({ rpm_limit: e.target.value })} placeholder="unset" />
        </div>
        <div className="form__row">
          <label className="form__label">Hourly rate limit</label>
          <input type="number" min="0" value={form.hourly_rate_limit}
            onChange={(e) => update({ hourly_rate_limit: e.target.value })} placeholder="unset" />
        </div>
        <div className="form__row">
          <label className="form__label">Hourly budget (USD)</label>
          <input type="number" step="0.01" min="0" value={form.budget_hourly_usd}
            onChange={(e) => update({ budget_hourly_usd: e.target.value })} placeholder="unset" />
        </div>
        <div className="form__row">
          <label className="form__label">Weekly budget (USD)</label>
          <input type="number" step="0.01" min="0" value={form.budget_weekly_usd}
            onChange={(e) => update({ budget_weekly_usd: e.target.value })} placeholder="unset" />
        </div>
        <div className="form__row">
          <label className="form__label">Monthly budget (USD)</label>
          <input type="number" step="0.01" min="0" value={form.budget_monthly_usd}
            onChange={(e) => update({ budget_monthly_usd: e.target.value })} placeholder="unset" />
        </div>
        <div className="form__row">
          <label className="form__label">Max parallel requests</label>
          <input type="number" min="0" value={form.max_parallel_requests}
            onChange={(e) => update({ max_parallel_requests: e.target.value })}
            placeholder="unset = unlimited concurrent" />
        </div>
      </div>
      <ModelMultiSelect
        label="Allowed models"
        value={form.allowed_models}
        onChange={(v) => update({ allowed_models: v })}
        placeholder="leave empty for all models"
        hint="Pick from the available catalog or press Enter to add a custom / wildcard token (e.g. gpt-4*). Empty = all models. Per-model upstream routing can be configured below."
      />
      {routableModels.length > 0 && (
        <div className="form__row">
          <label className="form__label">Per-model routing</label>
          <div className="muted" style={{ marginBottom: 8 }}>
            Pin each model to specific upstream providers. Empty = use the default round-robin across all serving providers (no failover outside a pinned single-provider set).
          </div>
          <div className="model-routes">
            {routableModels.map((model) => {
              const selected = routeProvidersFor(model);
              const liveLoaded = model in providersByModel;
              // Choices combine live registry providers with every
              // configured upstream so the operator can pin to an upstream
              // that has no live auth yet. Loading state reflects both the
              // per-model live-probe AND the one-time upstream catalog
              // fetch; while either is pending we show the spinner instead
              // of a misleading "no providers" message.
              const choices = routeProviderChoices(model);
              const loading =
                !liveLoaded &&
                allUpstreamProviderKeys === null;
              const liveLoadedAndEmpty = liveLoaded && (providersByModel[model] || []).length === 0;
              const catalogEmpty =
                allUpstreamProviderKeys !== null && allUpstreamProviderKeys.length === 0;
              return (
                <div className="model-routes__row" key={model}>
                  <div className="model-routes__model mono">{model}</div>
                  <div className="model-routes__providers">
                    {loading && <span className="muted">Loading providers…</span>}
                    {!loading && choices.length === 0 && liveLoadedAndEmpty && catalogEmpty && (
                      <span className="muted">No live providers serve this model, and no upstream providers are configured.</span>
                    )}
                    {!loading && choices.length === 0 && liveLoadedAndEmpty && !catalogEmpty && (
                      <span className="muted">No live providers serve this model; pin to a configured upstream below.</span>
                    )}
                    {choices.map((p) => {
                      const on = selected.includes(p);
                      const isLive =
                        liveLoaded &&
                        (providersByModel[model] || []).includes(p);
                      return (
                        <button
                          type="button"
                          key={p}
                          className={`chip ${on ? 'chip--selected' : ''} mono`}
                          onClick={() => toggleRouteProvider(model, p)}
                          title={isLive ? 'Live provider (currently serving this model)' : 'Configured upstream (no live auth right now)'}
                        >
                          {p}{isLive ? '' : ' *'}
                        </button>
                      );
                    })}
                  </div>
                  <div className="muted model-routes__hint">
                    {selected.length === 0 ? 'default' : selected.join(', ')}
                  </div>
                </div>
              );
            })}
          </div>
        </div>
      )}
      <ModelMultiSelect
        label="Blocked models"
        value={form.blocked_models}
        onChange={(v) => update({ blocked_models: v })}
        placeholder="models that should always be rejected"
        hint="Takes precedence over the allowed list. Supports wildcards like claude-*."
      />
    </div>
  );
}
