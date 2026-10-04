// ProviderBudgetPage — per-entry USD budget vs actual spend for every
// upstream provider API key entry with a budget_usd set. Grouped by provider
// with color-coded usage percentage columns.

import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { getProviderBudget } from '../api/client.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { Spinner, ErrorBanner, EmptyState } from '../components/Primitives.jsx';

const WARN_PCT = 50;
const CRIT_PCT = 80;

export function severityForUsagePct(pct) {
  if (pct >= CRIT_PCT) return 'over';
  if (pct >= WARN_PCT) return 'warn';
  return 'ok';
}

function formatUSD(v) {
  if (v == null || Number.isNaN(v)) return '—';
  return `$${Number(v).toFixed(4)}`;
}

function formatPct(v) {
  if (v == null || Number.isNaN(v)) return '—';
  return `${Number(v).toFixed(1)}%`;
}

export default function ProviderBudgetPage() {
  const [data, setData] = useState(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState(null);

  const fetchBudget = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const resp = await getProviderBudget();
      setData(resp);
    } catch (err) {
      setError(err.message || 'Failed to load provider budget');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { fetchBudget(); }, [fetchBudget]);
  useAutoRefresh(fetchBudget, 30000);

  // Group by provider + compute totals
  const grouped = useMemo(() => {
    if (!data?.rows?.length) return [];
    const map = {};
    for (const r of data.rows) {
      const key = `${r.provider_id}`;
      if (!map[key]) {
        map[key] = {
          provider_id: r.provider_id,
          provider_name: r.provider_name,
          provider_type: r.provider_type,
          entries: [],
          total_budget: 0,
          total_spent: 0,
        };
      }
      map[key].entries.push(r);
      map[key].total_budget += r.budget_usd;
      map[key].total_spent += r.spent_usd;
    }
    return Object.values(map).sort((a, b) =>
      (a.provider_name || '').localeCompare(b.provider_name || ''),
    );
  }, [data]);

  const hasBudget = data?.rows?.length > 0;

  return (
    <div className="provider-budget-page">
      <style>{`
        .provider-budget-page {
          padding: 24px;
          max-width: 1200px;
        }
        .provider-budget-page h1 {
          font-size: 1.5rem;
          font-weight: 600;
          margin: 0 0 4px;
        }
        .provider-budget-page .subtitle {
          color: #666;
          font-size: 0.875rem;
          margin: 0 0 24px;
        }
        .provider-budget-page .period {
          color: #888;
          font-size: 0.8rem;
          margin-bottom: 16px;
        }
        .provider-group {
          margin-bottom: 24px;
          border: 1px solid #e0e0e0;
          border-radius: 8px;
          overflow: hidden;
        }
        .provider-group__header {
          display: flex;
          align-items: center;
          justify-content: space-between;
          padding: 12px 16px;
          background: #f5f5f5;
          font-weight: 600;
          font-size: 1rem;
          border-bottom: 1px solid #e0e0e0;
        }
        .provider-group__header .total-row {
          font-weight: 500;
          font-size: 0.875rem;
          color: #555;
        }
        .provider-group__header .total-row span {
          margin-left: 16px;
        }
        .budget-table {
          width: 100%;
          border-collapse: collapse;
        }
        .budget-table th {
          text-align: left;
          padding: 8px 12px;
          font-size: 0.75rem;
          text-transform: uppercase;
          letter-spacing: 0.5px;
          color: #888;
          border-bottom: 1px solid #e0e0e0;
          background: #fafafa;
        }
        .budget-table td {
          padding: 8px 12px;
          font-size: 0.875rem;
          border-bottom: 1px solid #f0f0f0;
        }
        .budget-table tr:last-child td {
          border-bottom: none;
        }
        .usage-bar {
          display: flex;
          align-items: center;
          gap: 8px;
        }
        .usage-bar__track {
          flex: 1;
          height: 8px;
          background: #e8e8e8;
          border-radius: 4px;
          overflow: hidden;
          min-width: 80px;
        }
        .usage-bar__fill {
          height: 100%;
          border-radius: 4px;
          transition: width 0.3s ease;
        }
        .usage-bar__fill.ok {
          background: #22c55e;
        }
        .usage-bar__fill.warn {
          background: #f59e0b;
        }
        .usage-bar__fill.over {
          background: #ef4444;
        }
        .usage-pct {
          font-weight: 600;
          font-size: 0.8rem;
          min-width: 48px;
          text-align: right;
        }
        .usage-pct.ok {
          color: #22c55e;
        }
        .usage-pct.warn {
          color: #f59e0b;
        }
        .usage-pct.over {
          color: #ef4444;
        }
        .remaining {
          font-weight: 500;
        }
        .remaining.ok {
          color: #22c55e;
        }
        .remaining.warn {
          color: #f59e0b;
        }
        .remaining.over {
          color: #ef4444;
        }
        .entry-name {
          font-family: monospace;
          font-size: 0.8125rem;
        }
      `}</style>

      <h1>Provider Budget</h1>
      <p className="subtitle">
        Per-entry USD budget vs actual spend across upstream providers
      </p>

      {loading && !data && <Spinner />}
      {error && <ErrorBanner message={error} onRetry={fetchBudget} />}

      {!loading && !error && !hasBudget && (
        <EmptyState message="No provider budgets configured. Set a budget_usd on an API key entry within an upstream provider to track spend." />
      )}

      {data && (
        <p className="period">
          Period: {new Date(data.from).toLocaleDateString()} — {new Date(data.to).toLocaleDateString()}
        </p>
      )}

      {grouped.map((group) => {
        const groupUsagePct = group.total_budget > 0
          ? (group.total_spent / group.total_budget) * 100
          : 0;
        const groupSev = severityForUsagePct(groupUsagePct);

        return (
          <div key={group.provider_id} className="provider-group">
            <div className="provider-group__header">
              <span>
                {group.provider_name || group.provider_type}
                <span className="provider-type" style={{ color: '#888', fontWeight: 400, marginLeft: 8, fontSize: '0.8rem' }}>
                  ({group.provider_type})
                </span>
              </span>
              <span className="total-row">
                <span>Budget: {formatUSD(group.total_budget)}</span>
                <span>Spent: {formatUSD(group.total_spent)}</span>
                <span className={`remaining ${groupSev}`}>Remaining: {formatUSD(Math.max(0, group.total_budget - group.total_spent))}</span>
              </span>
            </div>
            <table className="budget-table">
              <thead>
                <tr>
                  <th>Entry</th>
                  <th>API Key</th>
                  <th>Budget (USD)</th>
                  <th>Spent (USD)</th>
                  <th>Remaining (USD)</th>
                  <th>Usage</th>
                </tr>
              </thead>
              <tbody>
                {group.entries.map((r) => {
                  const sev = severityForUsagePct(r.usage_pct);
                  return (
                    <tr key={r.entry_id}>
                      <td className="entry-name">{r.entry_name || <span style={{ color: '#aaa' }}>—</span>}</td>
                      <td style={{ fontFamily: 'monospace', fontSize: '0.8125rem', color: '#888' }}>
                        {r.entry_api_key_prefix}...
                      </td>
                      <td>{formatUSD(r.budget_usd)}</td>
                      <td>{formatUSD(r.spent_usd)}</td>
                      <td className={`remaining ${sev}`}>{formatUSD(r.remaining_usd)}</td>
                      <td>
                        <div className="usage-bar">
                          <div className="usage-bar__track">
                            <div
                              className={`usage-bar__fill ${sev}`}
                              style={{ width: `${Math.min(r.usage_pct, 100)}%` }}
                            />
                          </div>
                          <span className={`usage-pct ${sev}`}>{formatPct(r.usage_pct)}</span>
                        </div>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        );
      })}
    </div>
  );
}
