import React from 'react';
import { Link } from 'react-router-dom';
import { useAsync } from '../hooks/useAsync.js';
import { getRoutingStrategy, getModelGroup } from '../api/client.js';

// strategyLabel renders the route strategy for the summary table.
function strategyLabel(strategy) {
  if (strategy === 'priority') return 'priority';
  if (strategy === 'failover') return 'failover';
  if (strategy === 'weighted') return 'weighted';
  return 'default';
}

function pinnedRoutes(routes) {
  return (Array.isArray(routes) ? routes : []).filter(
    (r) => r && Array.isArray(r.providers) && r.providers.length > 0,
  );
}

// RouteTable renders the pinned per-model routes compactly.
function RouteTable({ routes }) {
  const pinned = pinnedRoutes(routes);
  if (pinned.length === 0) {
    return <div className="muted">No per-model pins. Every model uses the global strategy.</div>;
  }
  const inherited = pinned.filter((r) => !r.strategy).length;
  const explicit = pinned.length - inherited;
  return (
    <>
      <div className="muted" style={{ marginBottom: 8 }}>
        {pinned.length} model{pinned.length === 1 ? '' : 's'} pinned
        {explicit > 0 ? ` · ${explicit} with an explicit strategy` : ''}
        {inherited > 0 ? ` · ${inherited} inheriting global` : ''}
      </div>
      <table className="table table--dense">
        <thead>
          <tr><th>Model</th><th>Strategy</th><th>Providers</th></tr>
        </thead>
        <tbody>
          {pinned.map((r) => (
            <tr key={r.model}>
              <td className="mono">{r.model}</td>
              <td>
                <span className={`sgl-strat sgl-strat--${strategyLabel(r.strategy)}`}>
                  {strategyLabel(r.strategy)}
                </span>
              </td>
              <td className="mono" title={r.providers.join(', ')}>{r.providers.join(', ')}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </>
  );
}

// RoutingSummaryCard is a read-only digest of "how does this entity route?".
//
// It answers the operator's recurring question — where is routing configured,
// and what is actually in effect? — without duplicating the editors. It
// surfaces the inherited global strategy (with a deep link to Settings) and
// every pinned per-model route. When a Model Group drives this entity it names
// the group as the source of truth and shows the group's routes instead.
//
// Props:
//   routes       — [{ model, providers, strategy, priorities }]
//   modelGroupId — when set, the attached Model Group overrides `routes`.
//   groupLink    — route path for the attached group (defaults /model-groups/{id}).
export default function RoutingSummaryCard({ routes = [], modelGroupId = '', groupLink }) {
  const strategyReq = useAsync(() => getRoutingStrategy(), []);
  const groupReq = useAsync(
    () => (modelGroupId ? getModelGroup(modelGroupId) : Promise.resolve(null)),
    [modelGroupId],
  );
  const globalStrategy = strategyReq.data?.strategy || 'round-robin';
  const group = modelGroupId ? (groupReq.data?.group || null) : null;

  return (
    <div className="card">
      <div className="row row--between" style={{ alignItems: 'baseline' }}>
        <h3 className="card__title" style={{ margin: 0 }}>Routing summary</h3>
        <Link to="/settings" className="dim">Global strategy: {globalStrategy} →</Link>
      </div>
      <p className="muted" style={{ marginTop: 4, marginBottom: 12 }}>
        Per-model provider pinning in effect here. Models with no pin follow the
        global strategy; per-upstream pool strategies apply within a provider.
      </p>

      {modelGroupId ? (
        <>
          <div className="form__row group-summary" style={{ marginBottom: 12 }}>
            <div className="form__label">Source of truth</div>
            {groupReq.loading ? (
              <div className="muted">Loading model group…</div>
            ) : group ? (
              <div>
                <span className="badge badge--info">Model Group</span>{' '}
                <Link to={groupLink || `/model-groups/${group.id}`} className="dim">{group.name}</Link>
                <div className="muted" style={{ marginTop: 4 }}>
                  This group overrides the entity&apos;s allowed/blocked lists and per-model routes.
                </div>
              </div>
            ) : (
              <div className="muted">
                Attached Model Group (id: <span className="mono">{modelGroupId}</span>) could not be loaded.
              </div>
            )}
          </div>
          {group && <RouteTable routes={group.model_routes} />}
        </>
      ) : (
        <RouteTable routes={routes} />
      )}
    </div>
  );
}
