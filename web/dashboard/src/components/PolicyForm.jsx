import React, { useState, useEffect } from 'react';

// PolicyForm — reusable form for editing a Policy object.
//
// Used in two places:
//   1. The ApiKeyDetailPage "Edit Policy" panel (edit existing policy).
//   2. The ApiKeysPage "New API Key" modal (attach a policy at creation time).
//
// The component is controlled: callers receive the up-to-date policy object
// via onChange. Submit is delegated to the parent — this keeps worries about
// API calls (POST vs PUT) out of the form itself.
const EMPTY_POLICY = {
  rpm_limit: '',
  hourly_rate_limit: '',
  budget_hourly_usd: '',
  budget_weekly_usd: '',
  budget_monthly_usd: '',
  allowed_models: '',
  blocked_models: '',
};

export function policyToForm(policy) {
  if (!policy) return { ...EMPTY_POLICY };
  return {
    rpm_limit: policy.rpm_limit ?? '',
    hourly_rate_limit: policy.hourly_rate_limit ?? '',
    budget_hourly_usd: policy.budget_hourly_usd ?? '',
    budget_weekly_usd: policy.budget_weekly_usd ?? '',
    budget_monthly_usd: policy.budget_monthly_usd ?? '',
    allowed_models: (policy.allowed_models || []).join('\n'),
    blocked_models: (policy.blocked_models || []).join('\n'),
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
    allowed_models: listFromTextarea(form.allowed_models),
    blocked_models: listFromTextarea(form.blocked_models),
  };
  return policy;
}

function numOrNull(s) {
  if (s === '' || s === null || s === undefined) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}
function floatOrNull(s) { return numOrNull(s); }
function listFromTextarea(s) {
  if (!s) return [];
  return s.split('\n').map((l) => l.trim()).filter(Boolean);
}

export default function PolicyForm({ initial, onChange }) {
  const [form, setForm] = useState(() => policyToForm(initial));

  // Keep parent in sync whenever the form changes. We only call onChange with
  // the raw string form — the parent decides when to convert via formToPolicy.
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
      </div>
      <div className="form__row">
        <label className="form__label">Allowed models (one per line, supports `gpt-4*` wildcards)</label>
        <textarea rows={3} value={form.allowed_models}
          onChange={(e) => update({ allowed_models: e.target.value })}
          placeholder="leave empty for all models" />
      </div>
      <div className="form__row">
        <label className="form__label">Blocked models (one per line)</label>
        <textarea rows={3} value={form.blocked_models}
          onChange={(e) => update({ blocked_models: e.target.value })}
          placeholder="models that should always be rejected" />
      </div>
    </div>
  );
}
