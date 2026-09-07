// ============================================================================
// OpenCode Go panel — quota probe + catalog actions
// ============================================================================

// OpenCodeGoActions renders the opencode-go-specific editor affordances:
//   - a quota probe per saved api_key_entry (manual, fail-open — the
//     upstream quota API is not live yet, so most probes return a 404
//     explanation until OpenCode ships it)
//   - seed-models / refresh-models buttons that fill the catalog server-side
// Pure helpers are exported for tests.

import React, { useMemo, useState } from 'react';
import {
  fetchUpstreamProviderQuota,
  refreshUpstreamProviderModels,
  seedUpstreamProviderModels,
} from '../../api/client.js';

// WINDOW_LABELS maps the canonical window keys returned by the quota probe
// to their dashboard labels.
export const WINDOW_LABELS = { '5h': '5 hours', weekly: 'Weekly', monthly: 'Monthly' };

// normalizeWindows coerces the response's windows array (or missing shape)
// into an ordered, display-ready list.
export function normalizeWindows(res) {
  const wins = Array.isArray(res?.windows) ? res.windows : [];
  const order = ['5h', 'weekly', 'monthly'];
  return wins
    .map((w) => ({
      key: String(w?.key || ''),
      label: WINDOW_LABELS[w?.key] || w?.key || '?',
      used: Number(w?.used) || 0,
      limit: Number(w?.limit) || 0,
      percent: Number(w?.percent_used ?? -1),
      resetAt: String(w?.reset_at || ''),
    }))
    .sort((a, b) => order.indexOf(a.key) - order.indexOf(b.key));
}

// quotaErrorText maps a failed probe to operator-friendly copy. The 404 case
// is the expected one today, so it gets the explanatory badge text.
export function quotaErrorText(res) {
  const err = String(res?.error || '');
  if (/not available upstream \(HTTP 40[45]\)/.test(err)) {
    return 'Quota API belum tersedia di upstream. Set extra_config.quota_url bila OpenCode menerbitkan endpoint resmi.';
  }
  return err || 'Quota probe failed.';
}

// percentText renders one window's usage percentage; -1 means unknown.
export function percentText(p) {
  const n = Number(p);
  if (!Number.isFinite(n) || n < 0) return '—';
  return `${Math.round(n)}%`;
}

// QuotaResult renders the three window bars for one probe result.
export function QuotaResult({ res }) {
  if (!res) return null;
  if (!res.ok) {
    return (
      <div className="dim" style={{ marginTop: 8, fontSize: 12 }} data-testid="quota-error">
        {quotaErrorText(res)}
      </div>
    );
  }
  const wins = normalizeWindows(res);
  if (wins.length === 0) {
    return <div className="dim" style={{ marginTop: 8, fontSize: 12 }}>No usage windows reported.</div>;
  }
  return (
    <div style={{ marginTop: 8, fontSize: 12 }} data-testid="quota-windows">
      {wins.map((w) => (
        <div key={w.key} style={{ display: 'flex', alignItems: 'center', gap: 8, marginBottom: 4 }}>
          <span style={{ minWidth: 64 }}>{w.label}</span>
          <span className="dim" style={{ minWidth: 110 }}>
            {w.used}/{w.limit}
          </span>
          <span
            style={{
              flex: 1,
              height: 6,
              background: 'var(--bg)',
              border: '1px solid var(--border)',
              borderRadius: 3,
              overflow: 'hidden',
            }}
          >
            <span
              style={{
                display: 'block',
                height: '100%',
                width: `${Math.max(0, Math.min(100, w.percent))}%`,
                background: w.percent >= 90 ? 'var(--danger, var(--error, #c0392b))' : 'var(--success, #2e7d32)',
              }}
            />
          </span>
          <span style={{ minWidth: 40, textAlign: 'right' }}>{percentText(w.percent)}</span>
        </div>
      ))}
    </div>
  );
}

// entryChoiceLabel renders one entry dropdown label (saved rows only).
function entryChoiceLabel(e) {
  const base = (typeof e.name === 'string' && e.name.trim()) || `key-${e.id}`;
  return e.disabled ? `${base} (disabled)` : base;
}

// savedEntryOptions lists persisted entries (id > 0) — the only ones the
// server can resolve into a credential.
export function savedEntryOptions(form) {
  const entries = Array.isArray(form?.api_key_entries) ? form.api_key_entries : [];
  const opts = [];
  for (const e of entries) {
    if (!e || !(Number(e.id) > 0)) continue;
    opts.push({ value: String(e.id), label: entryChoiceLabel(e) });
  }
  return opts;
}

export default function OpenCodeGoActions({ provider, form, onCatalogChanged }) {
  const entryOpts = useMemo(() => savedEntryOptions(form), [form]);
  const [quotaEntryId, setQuotaEntryId] = useState(entryOpts[0]?.value || '');
  const [quotaRunning, setQuotaRunning] = useState(false);
  const [quotaRes, setQuotaRes] = useState(null);
  const [quotaErr, setQuotaErr] = useState('');
  const [busy, setBusy] = useState('');
  const [catalogMsg, setCatalogMsg] = useState('');

  const providerId = Number(provider?.id) || 0;

  async function runQuota() {
    if (!providerId || quotaEntryId === '' || quotaRunning) return;
    setQuotaRunning(true);
    setQuotaErr('');
    setQuotaRes(null);
    try {
      const res = await fetchUpstreamProviderQuota(providerId, Number(quotaEntryId));
      setQuotaRes(res);
    } catch (err) {
      setQuotaErr(err.message || 'Quota request failed.');
    } finally {
      setQuotaRunning(false);
    }
  }

  async function runCatalog(kind) {
    if (!providerId || busy) return;
    setBusy(kind);
    setCatalogMsg('');
    try {
      const res = kind === 'seed'
        ? await seedUpstreamProviderModels(providerId)
        : await refreshUpstreamProviderModels(providerId, {
            entryId: quotaEntryId === '' ? null : Number(quotaEntryId),
          });
      const n = Number(res?.added) || 0;
      setCatalogMsg(kind === 'seed'
        ? `Seed selesai: ${n} model ditambahkan (total ${res?.total ?? '?'}).`
        : `Refresh selesai: ${n} model baru (total ${res?.total ?? '?'}).`);
      if (n > 0 && typeof onCatalogChanged === 'function') onCatalogChanged();
    } catch (err) {
      setCatalogMsg(err.message || `${kind} failed.`);
    } finally {
      setBusy('');
    }
  }

  return (
    <div className="form-section" data-testid="opencodego-actions">
      <div className="form-section__title">OpenCode Go tools</div>
      <div className="form-section__hint">
        Manual quota check per API key entry, plus catalog seed/refresh against
        the upstream. Read-only — none of these mark the form dirty.
      </div>
      <div className="form-section__row">
        {entryOpts.length > 0 && (
          <label>
            <span className="form__label">Entry</span>
            <select
              value={quotaEntryId}
              onChange={(e) => setQuotaEntryId(e.target.value)}
              data-testid="opengo-entry"
              disabled={quotaRunning || !!busy}
            >
              {entryOpts.map((o) => (
                <option key={o.value} value={o.value}>{o.label}</option>
              ))}
            </select>
          </label>
        )}
        <button
          type="button"
          className="primary"
          onClick={runQuota}
          disabled={!providerId || quotaEntryId === '' || quotaRunning}
          data-testid="opengo-quota-run"
        >
          {quotaRunning ? 'Checking…' : 'Check quota'}
        </button>
        <button
          type="button"
          onClick={() => runCatalog('seed')}
          disabled={!providerId || !!busy}
          data-testid="opengo-seed"
        >
          {busy === 'seed' ? 'Seeding…' : 'Seed models'}
        </button>
        <button
          type="button"
          onClick={() => runCatalog('refresh')}
          disabled={!providerId || !!busy}
          data-testid="opengo-refresh"
        >
          {busy === 'refresh' ? 'Refreshing…' : 'Refresh models'}
        </button>
      </div>
      {quotaErr && <div className="error-banner" role="alert">{quotaErr}</div>}
      <QuotaResult res={quotaRes} />
      {catalogMsg && (
        <div className="dim" style={{ marginTop: 8, fontSize: 12 }} data-testid="opengo-catalog-msg">
          {catalogMsg}
        </div>
      )}
    </div>
  );
}
