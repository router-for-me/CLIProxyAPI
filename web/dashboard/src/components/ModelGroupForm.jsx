import React, { useState } from 'react';
import ModelMultiSelect from './ModelMultiSelect.jsx';
import ModelRoutesEditor from './ModelRoutesEditor.jsx';

// ModelGroupForm — controlled form for editing a ModelGroup template.
//
// Mirrors the shape accepted by POST / PUT /v0/management/model-groups.
// Callers receive the up-to-date payload via onChange; submit is delegated
// to the parent.
//
// Fields:
//   name        — unique, required.
//   description — free-form label.
//   allowed_models — grant list. Empty = all allowed. Supports wildcards.
//   blocked_models — takes precedence over allowed. Supports wildcards.
//   model_routes   — per-concrete-model upstream provider pinning.
//   metadata       — free-form JSON.
const EMPTY = {
  name: '',
  description: '',
  allowed_models: [],
  blocked_models: [],
  model_routes: [],
  metadata: '{}',
};

export function groupToForm(group) {
  if (!group) return { ...EMPTY, metadata: '{}' };
  return {
    name: group.name ?? '',
    description: group.description ?? '',
    allowed_models: Array.isArray(group.allowed_models) ? [...group.allowed_models] : [],
    blocked_models: Array.isArray(group.blocked_models) ? [...group.blocked_models] : [],
    model_routes: Array.isArray(group.model_routes)
      ? group.model_routes.map((r) => ({ model: r.model || '', providers: Array.isArray(r.providers) ? [...r.providers] : [] }))
      : [],
    metadata: group.metadata ? JSON.stringify(group.metadata, null, 2) : '{}',
  };
}

export function formToGroup(form) {
  return {
    name: (form.name || '').trim(),
    description: (form.description || '').trim(),
    allowed_models: dedupe(listFromField(form.allowed_models)),
    blocked_models: dedupe(listFromField(form.blocked_models)),
    model_routes: (Array.isArray(form.model_routes) ? form.model_routes : [])
      .filter((r) => r && r.model && !r.model.endsWith('*') && form.allowed_models.includes(r.model) && Array.isArray(r.providers) && r.providers.length > 0)
      .map((r) => ({ model: r.model, providers: dedupe(r.providers) })),
    metadata: parseMetadata(form.metadata),
  };
}

function listFromField(v) {
  if (Array.isArray(v)) return v.map((s) => String(s).trim()).filter(Boolean);
  if (typeof v === 'string') return v.split('\n').map((l) => l.trim()).filter(Boolean);
  return [];
}
function dedupe(arr) { return Array.from(new Set(arr)); }
function parseMetadata(s) {
  if (!s || s.trim() === '' || s.trim() === '{}') return {};
  try {
    const parsed = JSON.parse(s);
    return parsed && typeof parsed === 'object' ? parsed : {};
  } catch {
    return {};
  }
}

export default function ModelGroupForm({ initial, onChange }) {
  const [form, setForm] = useState(() => groupToForm(initial));

  function update(partial) {
    setForm((f) => ({ ...f, ...partial }));
  }

  // Keep parent in sync whenever the form changes. We only call onChange with
  // the raw form — the parent decides when to convert via formToGroup.
  React.useEffect(() => {
    onChange?.(form);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [form]);

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
      <ModelMultiSelect
        label="Allowed models"
        value={form.allowed_models}
        onChange={(v) => update({ allowed_models: v })}
        placeholder="leave empty for all models"
        hint="The group's grant list. Empty = all allowed. Supports wildcards like gpt-4*. When the group is attached to an entity, this list becomes the source of truth for that entity's allowed models."
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
      <div className="form__row">
        <label className="form__label">Metadata (JSON)</label>
        <textarea rows={3} value={form.metadata}
          onChange={(e) => update({ metadata: e.target.value })}
          placeholder="{}" />
      </div>
    </div>
  );
}
