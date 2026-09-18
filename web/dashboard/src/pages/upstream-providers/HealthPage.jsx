import React, { useEffect, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { useAsync } from '../../hooks/useAsync.js';
import { listUpstreamProviders, listUpstreamProviderLiveStatus } from '../../api/client.js';
import { coerceLiveStatusResponse } from '../../api/liveStatus.js';
import { getHealthSummary, partitionByHealth } from './health.js';
import { HealthSummary } from './HealthSummary.jsx';
import { HealthQuadrant } from './HealthQuadrant.jsx';
import { Spinner, ErrorBanner } from '../../components/Primitives.jsx';

// HealthPage — /upstream-providers/health — four-quadrant picker over
// the upstream provider list, partitioned by live/cooldown/breaker/
// stale-or-disabled. Manual refresh only.
export function HealthPage() {
  const navigate = useNavigate();
  const [refreshing, setRefreshing] = useState(false);
  const providersAsync = useAsync(() => listUpstreamProviders(), []);
  const [liveStatus, setLiveStatus] = useState({});

  const refreshHealth = async () => {
    setRefreshing(true);
    try {
      const json = await listUpstreamProviderLiveStatus();
      setLiveStatus(coerceLiveStatusResponse(json));
    } catch {
      setLiveStatus({});
    } finally {
      setRefreshing(false);
    }
  };

  useEffect(() => { refreshHealth(); }, []);

  if (providersAsync.loading) return <div className="main"><Spinner /></div>;
  if (providersAsync.error) return <div className="main"><ErrorBanner error={providersAsync.error} onRetry={providersAsync.reload} /></div>;

  const providers = providersAsync.data?.providers ?? [];
  const summary = getHealthSummary(providers, liveStatus);
  const partition = partitionByHealth(providers, liveStatus);

  return (
    <div className="main">
      <header className="main__header">
        <h1 className="main__title">Upstream Health</h1>
        <p className="main__subtitle">Live status across all configured upstream providers.</p>
      </header>
      <div className="card p-3 mb-4">
        <HealthSummary summary={summary} onNavigate={(k) => navigate(k ? `/upstream-providers?health=${k}` : '/upstream-providers')} />
        <button type="button" className="btn btn-secondary mt-2" onClick={refreshHealth} disabled={refreshing}>
          {refreshing ? 'Refreshing…' : '↻ Refresh health'}
        </button>
      </div>
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <HealthQuadrant title="Live" tone="emerald" rows={partition.live} emptyHint="No providers currently live." onEdit={(r) => navigate(`/upstream-providers/${r.id}/overview`)} onToggleDisabled={() => { /* wired in follow-up */ }} />
        <HealthQuadrant title="Cooldown" tone="amber" rows={partition.cooldown} emptyHint="No providers in cooldown." onEdit={(r) => navigate(`/upstream-providers/${r.id}/overview`)} onToggleDisabled={() => {}} />
        <HealthQuadrant title="Breaker open" tone="rose" rows={partition.breaker} emptyHint="No circuit breakers open." onEdit={(r) => navigate(`/upstream-providers/${r.id}/overview`)} onToggleDisabled={() => {}} />
        <HealthQuadrant title="Stale or disabled" tone="zinc" rows={partition.staleOrDisabled} emptyHint="No stale or disabled providers." onEdit={(r) => navigate(`/upstream-providers/${r.id}/overview`)} onToggleDisabled={() => {}} />
      </div>
    </div>
  );
}

export default HealthPage;
