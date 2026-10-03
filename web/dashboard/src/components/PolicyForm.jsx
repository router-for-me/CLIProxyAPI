import React, { useState, useEffect } from 'react';
import { Link } from 'react-router-dom';
import ModelMultiSelect from './ModelMultiSelect.jsx';
import ModelRoutesEditor from './ModelRoutesEditor.jsx';
import { dedupeStrings, routeToWire } from './modelRoute.js';
import { listModelGroups, getModelGroup } from '../api/client.js';

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
// Model access source: when model_group_id is set, the group becomes the
// source of truth (the entity's own allowed/blocked/routes are ignored at
// enforcement time). The form hides those fields and shows a read-only
// summary of the attached group instead. Operators can still edit the group
// itself on /model-groups/:id.
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
  model_group_id: '',
  allowed_ips: [],
  blocked_ips: [],
  store_request_bodies: false,
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
      ? policy.model_routes.map((r) => ({
          model: r.model || '',
          providers: Array.isArray(r.providers) ? [...r.providers] : [],
          strategy: r.strategy || '',
          priorities: Array.isArray(r.priorities)
            ? r.priorities.map((pr) => ({ provider: pr.provider || '', priority: Number(pr.priority) || 0 }))
            : [],
        }))
      : [],
    model_group_id: policy.model_group_id ?? '',
    allowed_ips: Array.isArray(policy.allowed_ips) ? [...policy.allowed_ips] : [],
    blocked_ips: Array.isArray(policy.blocked_ips) ? [...policy.blocked_ips] : [],
    store_request_bodies: policy.store_request_bodies ?? false,
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
    model_group_id: form.model_group_id || null,
    // When a group is attached, the group is the source of truth — send empty
    // entity fields so the persisted policy reflects the dashboard's intent
    // (the enforcement path overrides these anyway, but keeping them empty
    // avoids confusion when reading the row directly).
    allowed_models: form.model_group_id ? [] : dedupeStrings(form.allowed_models),
    blocked_models: form.model_group_id ? [] : dedupeStrings(form.blocked_models),
    model_routes: form.model_group_id
      ? []
      : (Array.isArray(form.model_routes) ? form.model_routes : [])
          .filter((r) => r && r.model && !r.model.endsWith('*') && form.allowed_models.includes(r.model) && Array.isArray(r.providers) && r.providers.length > 0)
          .map((r) => routeToWire(r)),
    allowed_ips: dedupeStrings(form.allowed_ips),
    blocked_ips: dedupeStrings(form.blocked_ips),
    store_request_bodies: !!form.store_request_bodies,
  };
  return policy;
}

function numOrNull(s) {
  if (s === '' || s === null || s === undefined) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}
function floatOrNull(s) { return numOrNull(s); }

export default function PolicyForm({ initial, onChange }) {
  const [form, setForm] = useState(() => policyToForm(initial));

  // Available model groups (for the "Model access source" selector). When a
  // group is selected, it becomes the source of truth at enforcement time
  // and the per-entity allowed/blocked/routes fields below are hidden.
  const [modelGroups, setModelGroups] = useState([]);
  const [attachedGroup, setAttachedGroup] = useState(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const res = await listModelGroups({ page: 1, pageSize: 200 });
        if (!cancelled) setModelGroups(Array.isArray(res?.groups) ? res.groups : []);
      } catch {
        if (!cancelled) setModelGroups([]);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  // Load the name + grant summary of the attached group whenever the id
  // changes so the read-only summary block can render without a click.
  useEffect(() => {
    if (!form.model_group_id) { setAttachedGroup(null); return; }
    let cancelled = false;
    (async () => {
      try {
        const res = await getModelGroup(form.model_group_id);
        if (!cancelled) setAttachedGroup(res?.group || null);
      } catch {
        if (!cancelled) setAttachedGroup(null);
      }
    })();
    return () => { cancelled = true; };
  }, [form.model_group_id]);

  // Keep parent in sync whenever the form changes. We only call onChange with
  // the raw form — the parent decides when to convert via formToPolicy.
  useEffect(() => {
    onChange?.(form);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form]);

  function update(partial) {
    setForm((f) => ({ ...f, ...partial }));
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
      <div className="form__row">
        <label className="form__label">Model access source</label>
        <div className="seg-group model-access-source" role="tablist" aria-label="Model access source">
          <button
            type="button"
            className={`seg-btn ${!form.model_group_id ? 'seg-btn--active' : ''}`}
            onClick={() => update({ model_group_id: '' })}
          >Entity settings</button>
          <button
            type="button"
            className={`seg-btn ${form.model_group_id ? 'seg-btn--active' : ''}`}
            onClick={() => update({ model_group_id: modelGroups[0]?.id || '' })}
            disabled={modelGroups.length === 0}
            title={modelGroups.length === 0 ? 'No model groups configured. Create one on /model-groups first.' : 'Use a reusable Model Group as the source of truth.'}
          >Model Group</button>
        </div>
        <div className="muted" style={{ marginTop: 6 }}>
          {!form.model_group_id
            ? 'Allowed/blocked lists configured below apply directly to this API key.'
            : 'The attached group overrides this key\'s allowed/blocked lists and per-model routes at enforcement time.'}
        </div>
      </div>
      {form.model_group_id ? (
        <div className="form__row">
          <label className="form__label">Attached group</label>
          <select value={form.model_group_id} onChange={(e) => update({ model_group_id: e.target.value })}>
            {modelGroups.map((g) => <option key={g.id} value={g.id}>{g.name}</option>)}
          </select>
          {attachedGroup && (
            <div className="group-summary" style={{ marginTop: 8 }}>
              <div className="muted">
                Allowed: {Array.isArray(attachedGroup.allowed_models) && attachedGroup.allowed_models.length === 0
                  ? 'all models'
                  : (attachedGroup.allowed_models || []).join(', ') || '—'}
              </div>
              {Array.isArray(attachedGroup.blocked_models) && attachedGroup.blocked_models.length > 0 && (
                <div className="muted">Blocked: {attachedGroup.blocked_models.join(', ')}</div>
              )}
              {Array.isArray(attachedGroup.model_routes) && attachedGroup.model_routes.length > 0 && (
                <div className="muted">Routes: {attachedGroup.model_routes.length} pinned</div>
              )}
              <Link to="/model-groups" className="dim">Manage groups →</Link>
            </div>
          )}
        </div>
      ) : (
        <>
          <ModelMultiSelect
            label="Allowed models"
            value={form.allowed_models}
            onChange={(v) => update({ allowed_models: v })}
            placeholder="leave empty for all models"
            hint="Pick from the available catalog or press Enter to add a custom / wildcard token (e.g. gpt-4*). Empty = all models. Per-model upstream routing can be configured below."
          />
          <ModelRoutesEditor
            allowedModels={form.allowed_models}
            routes={form.model_routes}
            onChange={(routes) => update({ model_routes: routes })}
          />
          <ModelMultiSelect
            label="Blocked models"
            value={form.blocked_models}
            onChange={(v) => update({ blocked_models: v })}
            placeholder="models that should always be rejected"
            hint="Takes precedence over the allowed list. Supports wildcards like claude-*."
          />
        </>
      )}
      <div className="form__row">
        <label className="form__label" htmlFor="allowed-ips">Allowed IPs / CIDRs (one per line)</label>
        <textarea id="allowed-ips" rows={3}
          value={Array.isArray(form.allowed_ips) ? form.allowed_ips.join('\n') : ''}
          onChange={(e) => update({ allowed_ips: e.target.value.split('\n') })}
          placeholder={'10.0.0.5\n10.0.0.0/8\n2001:db8::/32'} />
        <div className="form__hint">
          Single IPs (<code>10.0.0.5</code>) or CIDR ranges (<code>10.0.0.0/8</code>,
          <code>2001:db8::/32</code>). Empty = all IPs allowed (subject to the block list).
        </div>
      </div>
      <div className="form__row">
        <label className="form__label" htmlFor="blocked-ips">Blocked IPs / CIDRs (one per line)</label>
        <textarea id="blocked-ips" rows={3}
          value={Array.isArray(form.blocked_ips) ? form.blocked_ips.join('\n') : ''}
          onChange={(e) => update({ blocked_ips: e.target.value.split('\n') })}
          placeholder={'203.0.113.0/24'} />
        <div className="form__hint">
          Blocked entries take precedence over the allow list — a match denies
          the request even if the IP is also allowlisted.
        </div>
      </div>
      <div className="form__row">
        <label className="form__label" style={{ display: 'flex', alignItems: 'center', gap: 8 }}>
          <input
            type="checkbox"
            checked={!!form.store_request_bodies}
            onChange={(e) => update({ store_request_bodies: e.target.checked })}
          />
          Simpan request/response log
        </label>
        <div className="form__hint">
          When on, this key's request and response bodies may be stored in the
          database for inspection. Capture happens when the upstream provider
          OR this key allows it.
        </div>
      </div>
    </div>
  );
}
