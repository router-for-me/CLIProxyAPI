import React, { useState, useEffect } from 'react';
import ModelMultiSelect from '../../components/ModelMultiSelect.jsx';

// LiteLLMPolicyForm — controlled form for editing a Manage-LiteLLM key policy.
//
// Mirrors the shape accepted by PUT /v0/management/litellm/keys/:id/policy and
// POST /v0/management/litellm/keys. Unlike the runtime PolicyForm, this is
// LiteLLM-complete: a single budget_usd + budget_duration pair (LiteLLM
// semantics), a key-level tpm_limit, and a model→alias map. It intentionally
// does NOT include hourly/weekly/monthly window budgets or model_group_id —
// those are runtime-specific concepts.
//
// Field semantics:
//   - budget_usd: single lifetime USD cap for the key. Empty = unlimited.
//   - budget_duration: '1d' | '7d' | '30d' | '' — periodic reset window.
//   - rpm_limit / tpm_limit: sliding-window counters per key.
//   - aliases: optional friendly names per model (LiteLLM aliases map).
const EMPTY_POLICY = {
  rpm_limit: '',
  tpm_limit: '',
  budget_usd: '',
  budget_duration: '',
  max_parallel_requests: '',
  allowed_models: [],
  blocked_models: [],
  aliases: {},
  allowed_ips: [],
  blocked_ips: [],
};

export function liteLLMPolicyToForm(policy) {
  if (!policy) return { ...EMPTY_POLICY };
  return {
    rpm_limit: policy.rpm_limit ?? '',
    tpm_limit: policy.tpm_limit ?? '',
    budget_usd: policy.budget_usd ?? '',
    budget_duration: policy.budget_duration ?? '',
    max_parallel_requests: policy.max_parallel_requests ?? '',
    allowed_models: Array.isArray(policy.allowed_models) ? [...policy.allowed_models] : [],
    blocked_models: Array.isArray(policy.blocked_models) ? [...policy.blocked_models] : [],
    aliases: policy.aliases && typeof policy.aliases === 'object' ? { ...policy.aliases } : {},
    allowed_ips: Array.isArray(policy.allowed_ips) ? [...policy.allowed_ips] : [],
    blocked_ips: Array.isArray(policy.blocked_ips) ? [...policy.blocked_ips] : [],
  };
}

export function liteLLMFormToPolicy(form) {
  const policy = {
    rpm_limit: numOrNull(form.rpm_limit),
    tpm_limit: numOrNull(form.tpm_limit),
    budget_usd: numOrNull(form.budget_usd),
    budget_duration: form.budget_duration || '',
    max_parallel_requests: numOrNull(form.max_parallel_requests),
    allowed_models: dedupe(listFromField(form.allowed_models)),
    blocked_models: dedupe(listFromField(form.blocked_models)),
    aliases: cleanAliases(form.aliases),
    allowed_ips: dedupe(listFromField(form.allowed_ips)),
    blocked_ips: dedupe(listFromField(form.blocked_ips)),
  };
  return policy;
}

function numOrNull(s) {
  if (s === '' || s === null || s === undefined) return null;
  const n = Number(s);
  return Number.isFinite(n) ? n : null;
}

function listFromField(v) {
  if (Array.isArray(v)) return v.map((s) => String(s).trim()).filter(Boolean);
  if (typeof v === 'string') return v.split('\n').map((l) => l.trim()).filter(Boolean);
  return [];
}

function dedupe(arr) {
  return Array.from(new Set(arr));
}

function cleanAliases(aliases) {
  if (!aliases || typeof aliases !== 'object') return {};
  const out = {};
  Object.entries(aliases).forEach(([model, alias]) => {
    const m = String(model || '').trim();
    const a = String(alias || '').trim();
    if (m && a) out[m] = a;
  });
  return out;
}

const DURATION_OPTIONS = [
  { value: '', label: 'no periodic reset (one-shot)' },
  { value: '1d', label: '1 day' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
];

export default function LiteLLMPolicyForm({ initial, onChange }) {
  const [form, setForm] = useState(() => liteLLMPolicyToForm(initial));

  useEffect(() => {
    onChange?.(form);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form]);

  function update(partial) {
    setForm((f) => ({ ...f, ...partial }));
  }

  function updateAlias(model, alias) {
    setForm((f) => {
      const next = { ...f.aliases };
      const m = String(model || '').trim();
      if (!m) return f;
      if (String(alias || '').trim()) {
        next[m] = String(alias).trim();
      } else {
        delete next[m];
      }
      return { ...f, aliases: next };
    });
  }

  const aliasModels = [...(form.allowed_models || [])].filter(
    (m) => m && !m.endsWith('*'),
  );

  return (
    <div>
      <div className="grid grid--2">
        <div className="form__row">
          <label className="form__label">RPM limit</label>
          <input type="number" min="0" value={form.rpm_limit}
            onChange={(e) => update({ rpm_limit: e.target.value })} placeholder="unset = unlimited" />
        </div>
        <div className="form__row">
          <label className="form__label">TPM limit</label>
          <input type="number" min="0" value={form.tpm_limit}
            onChange={(e) => update({ tpm_limit: e.target.value })} placeholder="unset = unlimited" />
        </div>
        <div className="form__row">
          <label className="form__label">Budget (USD)</label>
          <input type="number" step="0.01" min="0" value={form.budget_usd}
            onChange={(e) => update({ budget_usd: e.target.value })} placeholder="unset = unlimited" />
        </div>
        <div className="form__row">
          <label className="form__label">Budget duration</label>
          <select value={form.budget_duration}
            onChange={(e) => update({ budget_duration: e.target.value })}>
            {DURATION_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
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
        hint="Empty = all models. Wildcards like gpt-4* are supported."
      />
      <ModelMultiSelect
        label="Blocked models"
        value={form.blocked_models}
        onChange={(v) => update({ blocked_models: v })}
        placeholder="models that should always be rejected"
        hint="Takes precedence over the allowed list. Supports wildcards like claude-*."
      />

      {aliasModels.length > 0 && (
        <div className="form__row">
          <label className="form__label">Model aliases (LiteLLM)</label>
          <div className="litellm-aliases">
            {aliasModels.map((m) => (
              <div key={m} className="litellm-aliases__row">
                <code className="litellm-aliases__model">{m}</code>
                <input
                  type="text"
                  value={form.aliases?.[m] || ''}
                  onChange={(e) => updateAlias(m, e.target.value)}
                  placeholder="friendly alias"
                  style={{ flex: 1 }}
                />
              </div>
            ))}
          </div>
          <div className="form__hint">
            Optional per-model friendly names (LiteLLM aliases map). Empty leaves
            the model without an alias.
          </div>
        </div>
      )}

      <div className="form__row">
        <label className="form__label" htmlFor="litellm-allowed-ips">Allowed IPs / CIDRs (one per line)</label>
        <textarea id="litellm-allowed-ips" rows={3}
          value={Array.isArray(form.allowed_ips) ? form.allowed_ips.join('\n') : ''}
          onChange={(e) => update({ allowed_ips: e.target.value.split('\n') })}
          placeholder={'10.0.0.5\n10.0.0.0/8\n2001:db8::/32'} />
        <div className="form__hint">
          Single IPs (<code>10.0.0.5</code>) or CIDR ranges (<code>10.0.0.0/8</code>).
          Empty = all IPs allowed (subject to the block list).
        </div>
      </div>
      <div className="form__row">
        <label className="form__label" htmlFor="litellm-blocked-ips">Blocked IPs / CIDRs (one per line)</label>
        <textarea id="litellm-blocked-ips" rows={3}
          value={Array.isArray(form.blocked_ips) ? form.blocked_ips.join('\n') : ''}
          onChange={(e) => update({ blocked_ips: e.target.value.split('\n') })}
          placeholder={'203.0.113.0/24'} />
        <div className="form__hint">
          Blocked entries take precedence over the allow list — a match denies
          the request even if the IP is also allowlisted.
        </div>
      </div>
    </div>
  );
}
