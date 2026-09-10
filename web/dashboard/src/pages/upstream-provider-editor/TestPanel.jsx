// ============================================================================
// Upstream provider editor — test panel
// ============================================================================

// TestPanel sends one pinned chat-completion probe (a server-generated
// random math question) through the real execution pipeline — the same
// authManager.Execute path live traffic takes — and reports the outcome
// inline: latency, the question, the expected vs actual answer, or the
// error. Read-only: it consumes form state (models + entries) but never
// writes to it, so running a test never marks the form dirty.

import React, { useMemo, useState } from 'react';
import { testUpstreamProvider } from '../../api/client.js';

// entryLabel renders one entry's dropdown label: the operator identity when
// set, else the stable key-<id> fallback. Disabled entries get a suffix so
// they stay testable but visibly marked.
export function entryLabel(e) {
  if (!e) return '';
  const base = (typeof e.name === 'string' && e.name.trim()) || `key-${e.id}`;
  return e.disabled ? `${base} (disabled)` : base;
}

// entryOptions builds the Entry dropdown: a provider-level option first,
// then one option per persisted entry (id > 0). Unsaved entries (id === 0)
// are filtered out — the server cannot resolve a credential that has never
// been rendered into the registry.
export function entryOptions(form) {
  const entries = Array.isArray(form?.api_key_entries) ? form.api_key_entries : [];
  const opts = [{ value: '', label: '(provider-level)' }];
  for (const e of entries) {
    if (!e || !(Number(e.id) > 0)) continue;
    opts.push({ value: String(e.id), label: entryLabel(e) });
  }
  return opts;
}

// modelOptions builds the Model dropdown from the form's model rows. The
// registry registers the ALIAS as the model id (buildConfiguredModelInfo:
// ID = alias, falling back to the upstream name), and the selection gate
// rejects probes naming an unregistered id — so the dropdown must offer the
// same id the pipeline knows: alias when set, upstream name otherwise.
// Deduplicated, empties dropped.
export function modelOptions(form) {
  const rows = Array.isArray(form?.models) ? form.models : [];
  const seen = new Set();
  const out = [];
  for (const r of rows) {
    const m = (r && (r.alias || r.name) || '').trim();
    if (!m || seen.has(m)) continue;
    seen.add(m);
    out.push(m);
  }
  return out;
}

// describeResult maps one probe response into the panel's display shape.
// A successful run with a mismatching answer still counts as connectivity
// OK but is flagged — the probe tests the pipeline, not the model's math.
export function describeResult(res) {
  if (!res) return null;
  if (!res.ok) {
    return { ok: false, flag: null, title: `✗ ${res.error || 'probe failed'}`, detail: latencyText(res) };
  }
  const answer = (res.answer || '').trim();
  const expected = res.expected_answer != null ? String(res.expected_answer) : '';
  const mismatch = expected !== '' && answer !== expected;
  return {
    ok: true,
    flag: mismatch ? 'wrong answer' : null,
    title: `✓ ${latencyText(res)}`,
    detail: { question: res.question || '', expected, answer },
  };
}

function latencyText(res) {
  const ms = Number(res?.latency_ms);
  return Number.isFinite(ms) ? `${ms}ms` : '';
}

// isEntryBearing reports whether the provider type uses the multi-row
// entries editor (openai-compatibility, claude-api-key, and opencode-go).
export function isEntryBearing(providerType) {
  return providerType === 'openai-compatibility'
    || providerType === 'claude-api-key'
    || providerType === 'opencode-go';
}

export default function TestPanel({ provider, form }) {
  const entryOpts = useMemo(() => entryOptions(form), [form]);
  const modelOpts = useMemo(() => modelOptions(form), [form]);

  const [entryId, setEntryId] = useState('');
  const [model, setModel] = useState(modelOpts[0] || '');
  const [running, setRunning] = useState(false);
  const [result, setResult] = useState(null);
  const [error, setError] = useState('');

  const entryBearing = isEntryBearing(provider?.provider_type);
  const canRun = !!model.trim() && !running;

  async function run() {
    if (!canRun) return;
    setRunning(true);
    setError('');
    try {
      const res = await testUpstreamProvider(provider.id, {
        entryId: entryId === '' ? null : Number(entryId),
        model: model.trim(),
      });
      setResult(res);
    } catch (err) {
      setError(err.message || 'Test request failed.');
    } finally {
      setRunning(false);
    }
  }

  const described = useMemo(() => describeResult(result), [result]);

  return (
    <div className="form-section" data-testid="provider-test-panel">
      <div className="form-section__title">Test entry</div>
      <div className="form-section__hint">
        Sends one small chat-completion probe (a random math question) pinned
        to this credential through the live pipeline. Read-only — never marks
        the form dirty.
      </div>
      <div className="form-section__row">
        {entryBearing && (
          <label>
            <span className="form__label">Entry</span>
            <select
              value={entryId}
              onChange={(e) => setEntryId(e.target.value)}
              data-testid="test-panel-entry"
              disabled={running}
            >
              {entryOpts.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
        )}
        <label>
          <span className="form__label">Model</span>
          {modelOpts.length > 0 ? (
            <>
              <select
                value={modelOpts.includes(model) ? model : ''}
                onChange={(e) => setModel(e.target.value)}
                data-testid="test-panel-model"
                disabled={running}
              >
                {!modelOpts.includes(model) && model && <option value="">{model} (custom)</option>}
                {modelOpts.map((m) => (
                  <option key={m} value={m}>{m}</option>
                ))}
              </select>
              <input
                type="text"
                value={model}
                onChange={(e) => setModel(e.target.value)}
                placeholder="or type a model id"
                spellCheck={false}
                style={{ marginTop: 6 }}
                disabled={running}
              />
            </>
          ) : (
            <input
              type="text"
              value={model}
              onChange={(e) => setModel(e.target.value)}
              placeholder="model id (e.g. gpt-4o)"
              spellCheck={false}
              data-testid="test-panel-model"
              disabled={running}
            />
          )}
        </label>
        <button
          type="button"
          className="primary"
          onClick={run}
          disabled={!canRun}
          data-testid="test-panel-run"
        >
          {running ? 'Testing…' : 'Run test'}
        </button>
      </div>

      {error && <div className="error-banner" role="alert">{error}</div>}
      {described && (
        <div
          style={{
            background: described.ok ? 'var(--success-dim)' : 'var(--error-dim, var(--bg))',
            border: `1px solid ${described.ok ? 'var(--success)' : 'var(--border)'}`,
            color: described.ok ? 'var(--success)' : 'var(--text)',
            padding: '8px 12px',
            borderRadius: 'var(--radius-sm)',
            marginTop: 10,
            fontSize: 12,
          }}
          data-testid="test-panel-result"
        >
          <div style={{ fontWeight: 600 }}>
            {described.title}
            {described.flag && (
              <span style={{ color: 'var(--warning, var(--text))', fontWeight: 400 }}>
                {' '}· ⚠ {described.flag}
              </span>
            )}
          </div>
          {described.ok && described.detail && (
            <div className="dim" style={{ marginTop: 4 }}>
              Q: {described.detail.question}
              <br />
              Expected: {described.detail.expected || '—'}
              {described.detail.answer && <> · Model replied: “{described.detail.answer}”</>}
            </div>
          )}
        </div>
      )}
    </div>
  );
}
