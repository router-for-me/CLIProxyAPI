import React, { useEffect, useMemo, useState } from 'react';
import { listModelsCatalog, listUpstreamProviders, getModelHealth } from '../api/client.js';
import { filterLiveModels, unwrapList } from './modelPickerFilters.js';

export default function ModelPicker({ value, onChange }) {
  const [tab, setTab] = useState('catalog');
  const [catalog, setCatalog] = useState([]);
  const [upstreams, setUpstreams] = useState([]);
  const [health, setHealth] = useState([]);
  const [loading, setLoading] = useState(true);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const [c, u, h] = await Promise.all([
          listModelsCatalog({ pageSize: 500 }).catch(() => []),
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

  return (
    <div className="space-y-2">
      <div className="flex gap-2 border-b border-border pb-1">
        <button onClick={() => setTab('catalog')} className={tab === 'catalog' ? 'font-semibold' : ''}>Catalog</button>
        <button onClick={() => setTab('upstream')} className={tab === 'upstream' ? 'font-semibold' : ''}>Upstream (LIVE)</button>
      </div>
      {loading ? (
        <div className="text-sm text-muted-foreground">Loading models…</div>
      ) : tab === 'catalog' ? (
        <CatalogList models={catalog} value={value} onChange={onChange} />
      ) : (
        <UpstreamList upstreams={liveUpstreams} value={value} onChange={onChange} />
      )}
    </div>
  );
}

function CatalogList({ models, value, onChange }) {
  return (
    <select
      className="w-full border rounded px-2 py-1 bg-background"
      value={value || ''}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">Select a model…</option>
      {models.map((m) => (
        <option key={m.id || m.model} value={m.model || m.id}>
          {m.model || m.id} {m.provider ? `· ${m.provider}` : ''}
        </option>
      ))}
    </select>
  );
}

function UpstreamList({ upstreams, value, onChange }) {
  if (!upstreams.length) {
    return <div className="text-sm text-muted-foreground">No LIVE models detected.</div>;
  }
  return (
    <select
      className="w-full border rounded px-2 py-1 bg-background"
      value={value || ''}
      onChange={(e) => onChange(e.target.value)}
    >
      <option value="">Select a LIVE model…</option>
      {upstreams.map((u) => (
        <option key={u.id} value={u.model}>
          {u.model} · {u.provider_type}
        </option>
      ))}
    </select>
  );
}
