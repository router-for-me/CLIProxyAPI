/** Cost & Tokens: API-equivalent cost and token usage of traffic through prox. */

import { useMemo, useState } from 'react';
import { useProx } from '../api';
import { compact, providerColor, providerLabel, usd } from '../format';
import type { CostRow, Costs } from '../types';
import styles from './ProxPage.module.scss';

const RANGES = [
  { id: '24h', label: '24 hours', step: '1h' },
  { id: '7d', label: '7 days', step: '1d' },
  { id: '30d', label: '30 days', step: '1d' },
] as const;

const GROUPS = [
  { id: 'model', label: 'Model' },
  { id: 'auth', label: 'Account' },
  { id: 'client', label: 'Client' },
  { id: 'provider', label: 'Provider' },
] as const;

type GroupId = (typeof GROUPS)[number]['id'];

const promptTokens = (r: CostRow) => r.tokens.input + r.tokens.cache_read + r.tokens.cache_write;

export function CostPage() {
  const [range, setRange] = useState<(typeof RANGES)[number]['id']>('7d');
  const [group, setGroup] = useState<GroupId>('model');
  const step = RANGES.find((r) => r.id === range)!.step;
  const series = useProx<Costs>(`/costs?range=${range}&group=provider&step=${step}`, 60_000);
  const table = useProx<Costs>(`/costs?range=${range}&group=provider,${group}`, 60_000);

  const buckets = useMemo(() => {
    const byStart = new Map<string, Map<string, number>>();
    for (const r of series.data?.rows ?? []) {
      const m = byStart.get(r.start) ?? new Map<string, number>();
      m.set(r.provider, (m.get(r.provider) ?? 0) + r.cost_usd);
      byStart.set(r.start, m);
    }
    return [...byStart.entries()].sort(([a], [b]) => a.localeCompare(b));
  }, [series.data]);
  const providers = useMemo(
    () => [...new Set((series.data?.rows ?? []).map((r) => r.provider))].sort(),
    [series.data]
  );
  const peak = Math.max(
    0.01,
    ...buckets.map(([, m]) => [...m.values()].reduce((a, b) => a + b, 0))
  );
  const total = table.data?.total;
  const labels = table.data?.labels ?? {};
  const rows = [...(table.data?.rows ?? [])].sort(
    (a, b) => b.cost_usd - a.cost_usd || b.requests - a.requests
  );
  const nameOf = (r: CostRow) =>
    group === 'auth'
      ? labels[r.auth_id] || r.auth_id || '--'
      : group === 'client'
        ? r.client || 'unknown'
        : group === 'provider'
          ? providerLabel(r.provider)
          : r.model || '--';
  const bucketLabel = (iso: string) =>
    step === '1h'
      ? new Date(iso).toLocaleTimeString(undefined, { hour: 'numeric' })
      : new Date(iso).toLocaleDateString(undefined, { month: 'short', day: 'numeric' });

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Cost & Tokens</h1>
          <p className={styles.meta}>API-equivalent cost of traffic through prox</p>
        </div>
        <div className={styles.controls}>
          <div className={styles.segmented}>
            {RANGES.map((r) => (
              <button
                key={r.id}
                type="button"
                aria-pressed={range === r.id}
                onClick={() => setRange(r.id)}
              >
                {r.label}
              </button>
            ))}
          </div>
        </div>
      </header>

      {(series.error || table.error) && (
        <div className={styles.error}>
          prox is off or unreachable: {series.error || table.error}
        </div>
      )}

      <div className={styles.tiles}>
        <div className={styles.tile}>
          <span className={styles.tileLabel}>Cost</span>
          <span className={styles.tileValue}>{usd(total?.cost_usd)}</span>
          <span className={styles.tileSub}>
            {total?.unpriced ? `${total.unpriced} unpriced requests` : 'all requests priced'}
          </span>
        </div>
        <div className={styles.tile}>
          <span className={styles.tileLabel}>Requests</span>
          <span className={styles.tileValue}>{compact(total?.requests)}</span>
          <span className={styles.tileSub}>{total?.failures ?? 0} failed</span>
        </div>
        <div className={styles.tile}>
          <span className={styles.tileLabel}>Input tokens</span>
          <span className={styles.tileValue}>{total ? compact(promptTokens(total)) : '--'}</span>
          <span className={styles.tileSub}>
            {total ? `${compact(total.tokens.cache_read)} cache read` : ''}
          </span>
        </div>
        <div className={styles.tile}>
          <span className={styles.tileLabel}>Output tokens</span>
          <span className={styles.tileValue}>{compact(total?.tokens.output)}</span>
          <span className={styles.tileSub}>
            {total ? `${compact(total.tokens.reasoning)} reasoning` : ''}
          </span>
        </div>
        <div
          className={styles.tile}
          title="Cache reads as a share of prompt tokens. Low values point at broken session affinity."
        >
          <span className={styles.tileLabel}>Cache read share</span>
          <span className={styles.tileValue}>
            {table.data?.cache_read_share == null
              ? '--'
              : `${(table.data.cache_read_share * 100).toFixed(0)}%`}
          </span>
          <span className={styles.tileSub}>of prompt tokens</span>
        </div>
      </div>

      <section className={styles.card}>
        <div className={styles.cardHead}>
          <h2>Cost per {step === '1h' ? 'hour' : 'day'}</h2>
          <div className={styles.legend}>
            {providers.map((p) => (
              <span key={p}>
                <i className={styles.swatch} style={{ background: providerColor(p) }} />
                {providerLabel(p)}
              </span>
            ))}
          </div>
        </div>
        <div className={styles.cardBody}>
          {buckets.length === 0 ? (
            <div className={styles.empty}>No traffic in this range.</div>
          ) : (
            <>
              <div className={styles.bars}>
                {buckets.map(([start, m]) => {
                  const sum = [...m.values()].reduce((a, b) => a + b, 0);
                  return (
                    <div
                      key={start}
                      className={styles.bar}
                      title={`${bucketLabel(start)}: ${usd(sum)}`}
                    >
                      {providers.map((p) =>
                        m.get(p) ? (
                          <div
                            key={p}
                            className={styles.barSeg}
                            style={{
                              height: `${((m.get(p) ?? 0) / peak) * 100}%`,
                              background: providerColor(p),
                            }}
                          />
                        ) : null
                      )}
                    </div>
                  );
                })}
              </div>
              <div className={styles.barAxis}>
                <span>{bucketLabel(buckets[0][0])}</span>
                <span>{bucketLabel(buckets[Math.floor(buckets.length / 2)][0])}</span>
                <span>{bucketLabel(buckets[buckets.length - 1][0])}</span>
              </div>
            </>
          )}
        </div>
      </section>

      <section className={styles.card}>
        <div className={styles.cardHead}>
          <h2>Breakdown</h2>
          <div className={styles.segmented}>
            {GROUPS.map((g) => (
              <button
                key={g.id}
                type="button"
                aria-pressed={group === g.id}
                onClick={() => setGroup(g.id)}
              >
                {g.label}
              </button>
            ))}
          </div>
        </div>
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>{GROUPS.find((g) => g.id === group)!.label}</th>
                {group !== 'provider' && <th>Provider</th>}
                <th className={styles.num}>Requests</th>
                <th className={styles.num}>Input</th>
                <th className={styles.num}>Cache read</th>
                <th className={styles.num}>Cache write</th>
                <th className={styles.num}>Output</th>
                <th className={styles.num}>Cache share</th>
                <th className={styles.num}>Cost</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r, i) => {
                const prompt = promptTokens(r);
                return (
                  <tr key={`${r.provider}:${r.model}:${r.auth_id}:${r.client}:${i}`}>
                    <td className={group === 'model' || group === 'auth' ? styles.mono : undefined}>
                      {nameOf(r)}
                    </td>
                    {group !== 'provider' && (
                      <td>
                        <i
                          className={styles.swatch}
                          style={{ background: providerColor(r.provider) }}
                        />
                        {providerLabel(r.provider)}
                      </td>
                    )}
                    <td className={styles.num}>{r.requests.toLocaleString()}</td>
                    <td className={styles.num}>{compact(r.tokens.input)}</td>
                    <td className={styles.num}>{compact(r.tokens.cache_read)}</td>
                    <td className={styles.num}>{compact(r.tokens.cache_write)}</td>
                    <td className={styles.num}>{compact(r.tokens.output)}</td>
                    <td className={styles.num}>
                      {prompt ? `${((r.tokens.cache_read / prompt) * 100).toFixed(0)}%` : '--'}
                    </td>
                    <td className={styles.num}>
                      {r.unpriced && !r.cost_usd ? (
                        <span className={styles.pill}>unpriced</span>
                      ) : (
                        usd(r.cost_usd)
                      )}
                    </td>
                  </tr>
                );
              })}
              {rows.length === 0 && (
                <tr>
                  <td colSpan={9} className={styles.empty}>
                    No traffic in this range.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>
    </div>
  );
}
