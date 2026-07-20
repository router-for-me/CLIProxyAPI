import React, { useState, useEffect } from 'react';
import ModelMultiSelect from './ModelMultiSelect.jsx';

// InternalUserPolicyForm — controlled form for editing an InternalUser's
// budget / RPM / TPM / model grant fields. Mirrors the shape accepted by
// PATCH /v0/management/internal-users/:id. Callers receive the up-to-date
// patch object via onChange; submit is delegated to the parent.
//
// Field semantics (mirrors LiteLLM InternalUser):
//   - max_budget: USD cap; empty = unlimited. When surpassed the policy
//     middleware returns 402 (Payment Required). Running spend lives on the
//     user row's Spend field, surfaced separately.
//   - budget_duration: '1d' | '7d' | '30d' | '' . When set, budget_reset_at
//     is auto-computed on save (server-side SetUserStore.ResetSpend re-arms
//     it when the window elapses).
//   - rpm_limit / tpm_limit: in-memory sliding-window counters per user_id.
//   - models: allow-list. Empty = all models.
const EMPTY = {
  user_alias: '',
  user_email: '',
  user_role: 'internal_user',
  max_budget: '',
  budget_duration: '',
  rpm_limit: '',
  tpm_limit: '',
  max_parallel_requests: '',
  models: [],
  metadata: '{}',
};

export function userToForm(user) {
  if (!user) return { ...EMPTY, metadata: '{}' };
  return {
    user_alias: user.user_alias ?? '',
    user_email: user.user_email ?? '',
    user_role: user.user_role || 'internal_user',
    max_budget: user.max_budget ?? '',
    budget_duration: user.budget_duration ?? '',
    rpm_limit: user.rpm_limit ?? '',
    tpm_limit: user.tpm_limit ?? '',
    max_parallel_requests: user.max_parallel_requests ?? '',
    models: Array.isArray(user.models) ? [...user.models] : [],
    metadata: user.metadata ? JSON.stringify(user.metadata, null, 2) : '{}',
  };
}

export function formToPatch(form) {
  const patch = {
    user_alias: form.user_alias,
    user_email: form.user_email,
    user_role: form.user_role,
    max_budget: numOrNull(form.max_budget),
    budget_duration: form.budget_duration || null,
    rpm_limit: numOrNull(form.rpm_limit),
    tpm_limit: numOrNull(form.tpm_limit),
    max_parallel_requests: numOrNull(form.max_parallel_requests),
    models: dedupe(listFromField(form.models)),
    metadata: parseMetadata(form.metadata),
  };
  return patch;
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

function parseMetadata(s) {
  if (!s || s.trim() === '' || s.trim() === '{}') return {};
  try {
    const parsed = JSON.parse(s);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

const ROLE_OPTIONS = [
  { value: 'internal_user', label: 'internal_user (subject to per-user budget/RPM)' },
  { value: 'proxy_admin', label: 'proxy_admin (bypasses per-user caps)' },
  { value: 'proxy_admin_viewer', label: 'proxy_admin_viewer (read-only style)' },
];

const DURATION_OPTIONS = [
  { value: '', label: 'no periodic reset (one-shot)' },
  { value: '1d', label: '1 day' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
];

export default function InternalUserPolicyForm({ initial, onChange }) {
  const [form, setForm] = useState(() => userToForm(initial));

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
          <label className="form__label">Alias</label>
          <input type="text" value={form.user_alias}
            onChange={(e) => update({ user_alias: e.target.value })}
            placeholder="human-readable label" />
        </div>
        <div className="form__row">
          <label className="form__label">Email</label>
          <input type="email" value={form.user_email}
            onChange={(e) => update({ user_email: e.target.value })}
            placeholder="user@example.com" />
        </div>
        <div className="form__row">
          <label className="form__label">Role</label>
          <select value={form.user_role}
            onChange={(e) => update({ user_role: e.target.value })}>
            {ROLE_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </div>
        <div className="form__row">
          <label className="form__label">Budget duration</label>
          <select value={form.budget_duration}
            onChange={(e) => update({ budget_duration: e.target.value })}>
            {DURATION_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
          </select>
        </div>
        <div className="form__row">
          <label className="form__label">Max budget (USD)</label>
          <input type="number" step="0.01" min="0" value={form.max_budget}
            onChange={(e) => update({ max_budget: e.target.value })} placeholder="unset = unlimited" />
        </div>
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
          <label className="form__label">Max parallel requests</label>
          <input type="number" min="0" value={form.max_parallel_requests}
            onChange={(e) => update({ max_parallel_requests: e.target.value })}
            placeholder="unset = unlimited concurrent" />
        </div>
      </div>
      <ModelMultiSelect
        label="Allowed models"
        value={form.models}
        onChange={(v) => update({ models: v })}
        placeholder="leave empty for all models"
        hint="User-level model allow-list. Per-key model access still applies; the user allow-list is intersected with the key policy."
      />
      <div className="form__row">
        <label className="form__label">Metadata (JSON)</label>
        <textarea rows={3} value={form.metadata}
          onChange={(e) => update({ metadata: e.target.value })}
          placeholder="{}" />
      </div>
    </div>
  );
}
