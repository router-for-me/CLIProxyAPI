import React, { useState, useEffect } from 'react';
import { putModelEntry, deleteModelEntry } from '../api/client.js';
import { Modal, Spinner } from './Primitives.jsx';

// ModelEntryModal — create or edit a single row in models_catalog.
//
// Modes:
//   - create: id and provider fields are editable. Use the "+" button on
//     the catalog page to add a user-defined model the upstream registry
//     does not know about.
//   - edit:   id and provider are immutable (they are the primary key);
//     the operator can tweak display fields, context length, capabilities,
//     etc.
//
// Field list mirrors the backend ModelEntryRequest shape exactly so the
// round-trip stays 1:1.
const EMPTY_ENTRY = {
  id: '',
  provider: '',
  official_provider: '',
  object: 'model',
  created: 0,
  owned_by: '',
  type: '',
  display_name: '',
  name: '',
  version: '',
  description: '',
  input_token_limit: 0,
  output_token_limit: 0,
  supported_generation_methods: '',
  context_length: 0,
  max_completion_tokens: 0,
  supported_parameters: '',
  input_modalities: '',
  output_modalities: '',
  supports_web_search: false,
  thinking_json: '',
  override_header_json: '',
  user_defined: true,
};

export default function ModelEntryModal({ mode, initial, onClose, onSaved, onDeleted }) {
  const isCreate = mode === 'create';

  const [form, setForm] = useState(() => buildFormFromInitial(initial, isCreate));
  const [saving, setSaving] = useState(false);
  const [deleting, setDeleting] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState('');

  useEffect(() => {
    setError('');
  }, [form]);

  function update(partial) {
    setForm((f) => ({ ...f, ...partial }));
  }

  async function handleSubmit(e) {
    e.preventDefault();
    if (saving) return;
    setSaving(true);
    setError('');

    const id = form.id.trim();
    const provider = form.provider.trim();
    if (!id || !provider) {
      setError('id and provider are required');
      setSaving(false);
      return;
    }

    // Validate optional JSON fields before sending — better UX than a 400.
    const thinking = parseJSON(form.thinking_json, 'thinking');
    if (thinking === PARSE_ERROR) {
      setError('thinking must be valid JSON (or empty).');
      setSaving(false);
      return;
    }
    const override = parseJSON(form.override_header_json, 'override_header');
    if (override === PARSE_ERROR) {
      setError('override_header must be valid JSON (or empty).');
      setSaving(false);
      return;
    }

    const body = {
      object: form.object || 'model',
      created: Number(form.created) || 0,
      owned_by: form.owned_by || provider,
      type: form.type || provider,
      official_provider: form.official_provider || '',
      display_name: form.display_name,
      name: form.name,
      version: form.version,
      description: form.description,
      input_token_limit: Number(form.input_token_limit) || 0,
      output_token_limit: Number(form.output_token_limit) || 0,
      supported_generation_methods: splitList(form.supported_generation_methods),
      context_length: Number(form.context_length) || 0,
      max_completion_tokens: Number(form.max_completion_tokens) || 0,
      supported_parameters: splitList(form.supported_parameters),
      input_modalities: splitList(form.input_modalities),
      output_modalities: splitList(form.output_modalities),
      supports_web_search: !!form.supports_web_search,
      thinking: thinking,
      override_header: override,
      user_defined: !!form.user_defined,
    };

    try {
      const saved = await putModelEntry(id, provider, body);
      onSaved?.(saved);
    } catch (err) {
      setError(err.message || 'Save failed');
    } finally {
      setSaving(false);
    }
  }

  async function handleDelete() {
    if (deleting) return;
    if (!confirmDelete) {
      setConfirmDelete(true);
      return;
    }
    setDeleting(true);
    setError('');
    try {
      await deleteModelEntry(form.id, form.provider);
      onDeleted?.();
    } catch (err) {
      setError(err.message || 'Delete failed');
      setConfirmDelete(false);
    } finally {
      setDeleting(false);
    }
  }

  return (
    <Modal
      title={isCreate ? 'Add model to catalog' : `Edit model — ${initial?.id}`}
      onClose={onClose}
    >
      {error && <div className="error-banner">{error}</div>}
      <form onSubmit={handleSubmit}>
        {/* Identity (locked when editing) */}
        <div className="grid grid--3">
          <div className="form__row">
            <label className="form__label">Model ID *</label>
            <input
              type="text" required value={form.id}
              onChange={(e) => update({ id: e.target.value })}
              disabled={!isCreate}
              placeholder="e.g. gpt-4o-mini-2024-07-18"
            />
          </div>
          <div className="form__row">
            <label className="form__label">Upstream Provider *</label>
            <input
              type="text" required value={form.provider}
              onChange={(e) => update({ provider: e.target.value })}
              disabled={!isCreate}
              placeholder="e.g. openai (proxy/router name)"
            />
          </div>
          <div className="form__row">
            <label className="form__label">Provider (official)</label>
            <input
              type="text" value={form.official_provider}
              onChange={(e) => update({ official_provider: e.target.value })}
              placeholder="e.g. openai (official name)"
            />
            <div className="form__hint">Official provider behind this model. Leave blank when same as upstream.</div>
          </div>
        </div>

        {/* Display fields */}
        <div className="grid grid--2">
          <div className="form__row">
            <label className="form__label">Display name</label>
            <input
              type="text" value={form.display_name}
              onChange={(e) => update({ display_name: e.target.value })}
              placeholder="e.g. GPT-4o mini"
            />
          </div>
          <div className="form__row">
            <label className="form__label">Object</label>
            <input
              type="text" value={form.object}
              onChange={(e) => update({ object: e.target.value })}
              placeholder="model"
            />
          </div>
          <div className="form__row">
            <label className="form__label">Type</label>
            <input
              type="text" value={form.type}
              onChange={(e) => update({ type: e.target.value })}
              placeholder="e.g. openai"
            />
          </div>
          <div className="form__row">
            <label className="form__label">Owned by</label>
            <input
              type="text" value={form.owned_by}
              onChange={(e) => update({ owned_by: e.target.value })}
              placeholder="e.g. openai"
            />
          </div>
          <div className="form__row">
            <label className="form__label">Version</label>
            <input
              type="text" value={form.version}
              onChange={(e) => update({ version: e.target.value })}
              placeholder="e.g. 2024-07-18"
            />
          </div>
          <div className="form__row">
            <label className="form__label">Name (Gemini-style)</label>
            <input
              type="text" value={form.name}
              onChange={(e) => update({ name: e.target.value })}
              placeholder="e.g. models/gemini-2.0-flash"
            />
          </div>
        </div>

        <div className="form__row">
          <label className="form__label">Description</label>
          <textarea
            rows={2} value={form.description}
            onChange={(e) => update({ description: e.target.value })}
            placeholder="Free-form description shown in tooling"
          />
        </div>

        {/* Capabilities */}
        <div className="grid grid--4">
          <div className="form__row">
            <label className="form__label">Context length</label>
            <input type="number" min="0" value={form.context_length}
              onChange={(e) => update({ context_length: e.target.value })} />
          </div>
          <div className="form__row">
            <label className="form__label">Max completion tokens</label>
            <input type="number" min="0" value={form.max_completion_tokens}
              onChange={(e) => update({ max_completion_tokens: e.target.value })} />
          </div>
          <div className="form__row">
            <label className="form__label">Input token limit</label>
            <input type="number" min="0" value={form.input_token_limit}
              onChange={(e) => update({ input_token_limit: e.target.value })} />
          </div>
          <div className="form__row">
            <label className="form__label">Output token limit</label>
            <input type="number" min="0" value={form.output_token_limit}
              onChange={(e) => update({ output_token_limit: e.target.value })} />
          </div>
        </div>

        <div className="grid grid--2">
          <div className="form__row">
            <label className="form__label">Input modalities (one per line)</label>
            <textarea rows={2} value={form.input_modalities}
              onChange={(e) => update({ input_modalities: e.target.value })}
              placeholder="TEXT&#10;IMAGE" />
          </div>
          <div className="form__row">
            <label className="form__label">Output modalities (one per line)</label>
            <textarea rows={2} value={form.output_modalities}
              onChange={(e) => update({ output_modalities: e.target.value })}
              placeholder="TEXT" />
          </div>
          <div className="form__row">
            <label className="form__label">Supported parameters (one per line)</label>
            <textarea rows={2} value={form.supported_parameters}
              onChange={(e) => update({ supported_parameters: e.target.value })}
              placeholder="max_tokens&#10;stop" />
          </div>
          <div className="form__row">
            <label className="form__label">Supported generation methods (one per line)</label>
            <textarea rows={2} value={form.supported_generation_methods}
              onChange={(e) => update({ supported_generation_methods: e.target.value })}
              placeholder="generateContent&#10;streamGenerateContent" />
          </div>
        </div>

        <div className="grid grid--2">
          <div className="form__row">
            <label className="form__label">Thinking config (JSON, optional)</label>
            <textarea rows={2} value={form.thinking_json}
              onChange={(e) => update({ thinking_json: e.target.value })}
              placeholder={'{"min":1024,"max":128000,"zero_allowed":true}'} />
            <div className="form__hint">Leave empty for non-reasoning models.</div>
          </div>
          <div className="form__row">
            <label className="form__label">Override header (JSON, optional)</label>
            <textarea rows={2} value={form.override_header_json}
              onChange={(e) => update({ override_header_json: e.target.value })}
              placeholder={'{"user-agent":"custom"}'} />
            <div className="form__hint">Forces upstream HTTP headers when present.</div>
          </div>
        </div>

        <div className="row gap-lg" style={{ flexWrap: 'wrap', marginTop: 8 }}>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input type="checkbox"
              checked={form.supports_web_search}
              onChange={(e) => update({ supports_web_search: e.target.checked })}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>Supports web search</span>
          </label>
          <label className="row gap-sm" style={{ cursor: 'pointer' }}>
            <input type="checkbox"
              checked={form.user_defined}
              onChange={(e) => update({ user_defined: e.target.checked })}
              style={{ width: 'auto' }}
            />
            <span className="form__label" style={{ margin: 0 }}>User-defined (skip registry validation)</span>
          </label>
          <div className="form__row" style={{ flex: '1 1 140px', marginBottom: 0 }}>
            <label className="form__label">Created (epoch)</label>
            <input type="number" value={form.created}
              onChange={(e) => update({ created: e.target.value })}
              placeholder="0" />
          </div>
        </div>

        <div className="form__actions">
          {!isCreate && (
            <>
              <button
                type="button"
                className="danger"
                onClick={handleDelete}
                disabled={deleting || saving}
              >
                {deleting ? 'Deleting…' : confirmDelete ? 'Confirm deletion' : 'Delete model'}
              </button>
              <span style={{ flex: 1 }} />
            </>
          )}
          <button type="button" onClick={onClose} disabled={saving || deleting}>Cancel</button>
          <button type="submit" className="primary" disabled={saving || deleting}>
            {saving ? 'Saving…' : (isCreate ? 'Add model' : 'Save changes')}
          </button>
        </div>
      </form>
    </Modal>
  );
}

function buildFormFromInitial(initial, isCreate) {
  if (isCreate || !initial) return { ...EMPTY_ENTRY };
  return {
    id: initial.id || '',
    provider: initial.provider || '',
    official_provider: initial.official_provider || initial.provider || '',
    object: initial.object || 'model',
    created: initial.created || 0,
    owned_by: initial.owned_by || initial.provider || '',
    type: initial.type || initial.provider || '',
    display_name: initial.display_name || '',
    name: initial.name || '',
    version: initial.version || '',
    description: initial.description || '',
    input_token_limit: initial.input_token_limit || 0,
    output_token_limit: initial.output_token_limit || 0,
    supported_generation_methods: (initial.supported_generation_methods || []).join('\n'),
    context_length: initial.context_length || 0,
    max_completion_tokens: initial.max_completion_tokens || 0,
    supported_parameters: (initial.supported_parameters || []).join('\n'),
    input_modalities: (initial.input_modalities || []).join('\n'),
    output_modalities: (initial.output_modalities || []).join('\n'),
    supports_web_search: !!initial.supports_web_search,
    thinking_json: initial.thinking ? JSON.stringify(initial.thinking, null, 2) : '',
    override_header_json: initial.override_header ? JSON.stringify(initial.override_header, null, 2) : '',
    user_defined: initial.user_defined ?? true,
  };
}

function splitList(s) {
  if (!s) return [];
  return s.split('\n').map((line) => line.trim()).filter(Boolean);
}

const PARSE_ERROR = Symbol('parse-error');

function parseJSON(s, _fieldName) {
  const trimmed = (s || '').trim();
  if (!trimmed) return null;
  try {
    return JSON.parse(trimmed);
  } catch {
    return PARSE_ERROR;
  }
}
