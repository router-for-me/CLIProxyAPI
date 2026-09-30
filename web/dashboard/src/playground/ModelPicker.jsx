// Unified model picker for the playground. Combines the catalog and the
// upstream (LIVE) lists into a single searchable, provider-grouped list
// that uses the hand-rolled .playground-picker-row CSS classes. Props
// match the prior contract: { value, onChange }.

import React, { useEffect, useMemo, useState } from 'react';
import { listModelsCatalog, listUpstreamProviders, getModelHealth } from '../api/client.js';
import { filterLiveModels, unwrapList } from './modelPickerFilters.js';

export default function ModelPicker({ value, onChange }) {
  const [catalog, setCatalog] = useState([]);
  const [upstreams, setUpstreams] = useState([]);
  const [health, setHealth] = useState([]);
  const [loading, setLoading] = useState(true);
  const [query, setQuery] = useState('');

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [c, u, h] = await Promise.all([
          listModelsCatalog({ pageSize: 500, scope: 'all' }).catch(() => []),
          listUpstreamProviders({ providerType: '' }).catch(() => []),
          getModelHealth().catch(() => []),
        ]);
        if (cancelled) return;
        setCatalog(unwrapList(c));
        setUpstreams(unwrapList(u));
        setHealth(unwrapList(h));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  const liveUpstreams = useMemo(() => filterLiveModels(upstreams, health), [upstreams, health]);

  // Cooldown snapshot is published by the management alerts runner via
  // window.__nixllmCooldownSnapshot. Absent gracefully — rows just lose
  // the COOLDOWN badge.
  const cooldownByProvider = useMemo(() => {
    const snap = typeof window !== 'undefined' ? window.__nixllmCooldownSnapshot : null;
    const providers = snap && typeof snap === 'object' ? snap.providers : null;
    if (!providers || typeof providers !== 'object') return new Set();
    const out = new Set();
    for (const [k, v] of Object.entries(providers)) {
      if (v && v.state === 'cooldown') out.add(k);
    }
    return out;
  }, [loading]);

  // Build a deduped, status-tagged list keyed by model name. Upstream
  // (LIVE) entries win over catalog-only entries when a model name is
  // present in both, so we always surface availability info.
  const unified = useMemo(() => {
    const byModel = new Map();
    for (const u of liveUpstreams) {
      const model = (u?.model || '').trim();
      if (!model) continue;
      byModel.set(model, {
        model,
        provider: u.provider_type || u.provider || 'other',
        status: 'live',
      });
    }
    for (const m of catalog) {
      const model = (m?.model || m?.id || '').trim();
      if (!model) continue;
      if (byModel.has(model)) continue;
      const provider = m.provider || m.provider_type || 'other';
      const inCooldown = cooldownByProvider.has(provider);
      byModel.set(model, {
        model,
        provider,
        status: inCooldown ? 'cooldown' : 'catalog',
      });
    }
    // Backfill cooldown for live rows whose provider currently has a
    // active cooldown entry — the alarms runner is the source of truth.
    for (const entry of byModel.values()) {
      if (entry.status === 'live' && cooldownByProvider.has(entry.provider)) {
        entry.status = 'cooldown';
      }
    }
    return Array.from(byModel.values()).sort((a, b) =>
      a.provider.localeCompare(b.provider) || a.model.localeCompare(b.model),
    );
  }, [catalog, liveUpstreams, cooldownByProvider]);

  const filtered = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return unified;
    return unified.filter(
      (e) => e.model.toLowerCase().includes(q) || (e.provider || '').toLowerCase().includes(q),
    );
  }, [unified, query]);

  const grouped = useMemo(() => {
    const map = new Map();
    for (const e of filtered) {
      const key = e.provider || 'other';
      if (!map.has(key)) map.set(key, []);
      map.get(key).push(e);
    }
    return Array.from(map.entries()).sort(([a], [b]) => a.localeCompare(b));
  }, [filtered]);

  return (
    <div className="playground-picker">
      <input
        type="text"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Search models or providers…"
        aria-label="Filter models"
        style={inputStyle}
      />
      {loading ? (
        <div style={messageStyle}>Loading models…</div>
      ) : grouped.length === 0 ? (
        <div style={messageStyle}>{query ? 'No models match' : 'No models available'}</div>
      ) : (
        <div role="listbox" aria-label="Models" style={listStyle}>
          {grouped.map(([provider, rows]) => (
            <div key={provider} style={groupStyle}>
              <div style={groupHeaderStyle}>{provider}</div>
              {rows.map((row) => {
                const isSelected = value && row.model === value;
                return (
                  <button
                    key={`${provider}:${row.model}`}
                    type="button"
                    role="option"
                    aria-selected={isSelected}
                    className={`playground-picker-row${isSelected ? ' is-selected' : ''}`}
                    onClick={() => onChange(row.model)}
                    style={{ width: '100%', textAlign: 'left' }}
                  >
                    <span style={{ fontWeight: isSelected ? 600 : 400 }}>{row.model}</span>
                    {row.status === 'live' && (
                      <span className="playground-picker-row__badge is-live">LIVE</span>
                    )}
                    {row.status === 'cooldown' && (
                      <span className="playground-picker-row__badge is-cooldown">COOLDOWN</span>
                    )}
                  </button>
                );
              })}
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

// Minimal hand-rolled styles for the bits that don't yet have a
// .playground-picker-* rule in global.css. Picker rows themselves use
// the global .playground-picker-row class.
const inputStyle = {
  width: '100%',
  padding: '6px 8px',
  border: '1px solid var(--border)',
  borderRadius: 'var(--radius-sm)',
  background: 'var(--bg)',
  color: 'var(--text)',
  fontSize: '12px',
  marginBottom: '8px',
};

const messageStyle = {
  fontSize: '12px',
  color: 'var(--text-muted)',
  padding: '8px 4px',
};

const listStyle = {
  maxHeight: '320px',
  overflowY: 'auto',
};

const groupStyle = {
  marginBottom: '8px',
};

const groupHeaderStyle = {
  fontSize: '10px',
  textTransform: 'uppercase',
  letterSpacing: '0.05em',
  color: 'var(--text-muted)',
  padding: '4px 2px',
};
