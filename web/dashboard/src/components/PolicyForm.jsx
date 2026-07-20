import React, { useState, useEffect } from 'react';
import ModelMultiSelect from './ModelMultiSelect.jsx';

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
      <ModelMultiSelect
        label="Allowed models"
        value={form.allowed_models}
        onChange={(v) => update({ allowed_models: v })}
        placeholder="leave empty for all models"
        hint="Pick from the available catalog or press Enter to add a custom / wildcard token (e.g. gpt-4*). Empty = all models."
      />
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
