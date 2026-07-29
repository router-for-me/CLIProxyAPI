import React, { useState } from 'react';
import { Modal } from './Primitives.jsx';
import ModelRouteConfigSection from './ModelRouteConfigSection.jsx';

// ModelRouteEntryModal — single-model editor for a ModelGroup's combined
// "Allowed models & routing" table. Covers the complete per-model workflow:
// model id (add mode), provider pinning + strategy (via ModelRouteConfigSection,
// the same UI the PolicyForm editor uses) and the per-model RPM / Max Budget
// caps.
//
// Save is uncontrolled-by-parent: the modal owns its draft state and calls
// onSave(entry) where entry = { model, providers, strategy, priorities,
// rpm, max_budget } (rpm / max_budget are empty string when unset).
//
// Props:
//   existingModels — model ids already on the list (for add-mode dup guard).
//   initial        — existing route entry when editing; null/undefined = add.
//   onCancel       — close without saving.
//   onSave         — commit the draft.
function numOrEmpty(v) {
  if (v === null || v === undefined) return '';
  if (typeof v === 'number') return Number.isFinite(v) ? String(v) : '';
  const n = Number(v);
  return Number.isFinite(n) ? String(n) : '';
}

export default function ModelRouteEntryModal({ existingModels = [], initial = null, onCancel, onSave }) {
  const editing = initial && typeof initial.model === 'string' && initial.model !== '';
  const [model, setModel] = useState(() => (editing ? initial.model : ''));
  const [route, setRoute] = useState(() => ({
    providers: Array.isArray(initial?.providers) ? [...initial.providers] : [],
    strategy: typeof initial?.strategy === 'string' ? initial.strategy : '',
    priorities: Array.isArray(initial?.priorities)
      ? initial.priorities.map((p) => ({ provider: p.provider || '', priority: Number(p.priority) || 0 }))
      : [],
  }));
  const [rpm, setRpm] = useState(() => numOrEmpty(initial?.rpm));
  const [maxBudget, setMaxBudget] = useState(() => numOrEmpty(initial?.max_budget));

  const trimmedModel = (model || '').trim();
  const dup = !editing && trimmedModel !== '' && existingModels.includes(trimmedModel);
  const valid = trimmedModel !== '' && !dup && !trimmedModel.endsWith('*');

  function handleSave() {
    if (!valid) return;
    onSave({
      model: trimmedModel,
      providers: route.providers,
      strategy: route.strategy,
      priorities: route.priorities,
      rpm,
      max_budget: maxBudget,
    });
  }

  return (
    <Modal
      title={editing ? `Edit ${initial.model}` : 'Add Model'}
      onClose={onCancel}
      size="lg"
      footer={
        <>
          <button type="button" onClick={onCancel}>Cancel</button>
          <button type="button" className="primary" onClick={handleSave} disabled={!valid}>
            {editing ? 'Save changes' : 'Add model'}
          </button>
        </>
      }
    >
      <div className="form__row">
        <label className="form__label">Model *</label>
        {editing ? (
          <div className="mono" style={{ padding: '6px 0' }}>{initial.model}</div>
        ) : (
          <>
            <input
              type="text"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder="e.g. gpt-4o"
              autoFocus
            />
            {dup && <div className="form__hint" style={{ color: 'var(--danger, #f87171)' }}>This model is already on the list.</div>}
            {trimmedModel.endsWith('*') && (
              <div className="form__hint" style={{ color: 'var(--danger, #f87171)' }}>
                Wildcard patterns cannot be pinned to providers or carry caps — use the wildcard field below the table.
              </div>
            )}
          </>
        )}
      </div>

      {valid || editing ? (
        <>
          <div className="form__row">
            <label className="form__label">Per-model routing</label>
            <div className="form__hint" style={{ marginBottom: 10 }}>
              Pin to specific upstream providers, or leave empty to inherit the
              default round-robin. Strategy orders the pinned set.
            </div>
            <ModelRouteConfigSection
              model={trimmedModel}
              route={route}
              onChange={setRoute}
            />
          </div>

          <div className="grid grid--2">
            <div className="form__row">
              <label className="form__label">RPM limit</label>
              <input
                type="number"
                min="0"
                step="1"
                value={rpm}
                onChange={(e) => setRpm(e.target.value)}
                placeholder="unlimited"
              />
              <div className="form__hint">Requests per minute for this model on keys attached to the group. Breach → HTTP 429.</div>
            </div>
            <div className="form__row">
              <label className="form__label">Max budget (USD)</label>
              <input
                type="number"
                min="0"
                step="0.01"
                value={maxBudget}
                onChange={(e) => setMaxBudget(e.target.value)}
                placeholder="unlimited"
              />
              <div className="form__hint">Total lifetime spend cap for this model on attached keys (from usage_events). Breach → HTTP 402.</div>
            </div>
          </div>
        </>
      ) : (
        <div className="muted">Enter a model id to configure routing and caps.</div>
      )}
    </Modal>
  );
}
