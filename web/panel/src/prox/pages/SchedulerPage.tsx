/** Scheduler: routing mode, next-session picks, live decisions, and sticky sessions. */

import { useProx } from '../api';
import { providerColor, providerLabel, relative, when } from '../format';
import type { Activity, Decision, Overview, Sessions } from '../types';
import styles from './ProxPage.module.scss';

const outcomeTone = (d: Decision) => {
  if (d.result?.failed) return 'bad';
  if (d.outcome === 'affinity' || d.outcome === 'routed' || d.outcome === 'new_session')
    return 'good';
  if (d.outcome === 'failover' || d.outcome === 'rebind') return 'warn';
  return undefined;
};

const short = (s?: string) => (s ? (s.length > 12 ? `${s.slice(0, 6)}…${s.slice(-4)}` : s) : '--');

export function SchedulerPage() {
  const overview = useProx<Overview>('/overview', 10_000);
  const activity = useProx<Activity>('/activity?limit=60', 10_000);
  const sessions = useProx<Sessions>('/sessions', 15_000);
  const o = overview.data;
  const labels = activity.data?.labels ?? {};
  const label = (id?: string) => (id ? labels[id] || id : '--');
  const c = o?.counters ?? {};

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Scheduler</h1>
          <p className={styles.meta}>
            {o
              ? `${o.mode === 'active' ? 'active' : 'shadow'} · ${o.policy} · affinity ${o.affinity.enabled ? o.affinity.ttl : 'off'}`
              : 'loading'}
          </p>
        </div>
      </header>

      {overview.error && (
        <div className={styles.error}>prox is off or unreachable: {overview.error}</div>
      )}

      <div className={styles.tiles}>
        {[
          ['Picks', c.picks, 'since start'],
          ['Sticky', c.affinity, 'reused session account'],
          ['New sessions', c.new_session, 'placed by policy'],
          ['Failovers', c.failovers, 'account failed mid-session'],
          ['Rebinds', c.rebinds, 'moved to a better account'],
          ['Delegated', c.delegated, 'left to CPA routing'],
        ].map(([name, value, sub]) => (
          <div key={name as string} className={styles.tile}>
            <span className={styles.tileLabel}>{name}</span>
            <span className={styles.tileValue}>{value ?? '--'}</span>
            <span className={styles.tileSub}>{sub}</span>
          </div>
        ))}
      </div>

      <section className={styles.card}>
        <div className={styles.cardHead}>
          <h2>Next new session</h2>
        </div>
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Provider</th>
                <th>Account</th>
                <th>Why</th>
              </tr>
            </thead>
            <tbody>
              {(o?.next_up ?? []).map((n) => (
                <tr key={n.provider}>
                  <td>
                    <i
                      className={styles.swatch}
                      style={{ background: providerColor(n.provider) }}
                    />
                    {providerLabel(n.provider)}
                  </td>
                  <td className={styles.mono}>
                    {n.account || n.auth_id || (n.fallback ? 'CPA default' : '--')}
                  </td>
                  <td className={styles.muted}>{n.reason}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </section>

      <div className={styles.grid2}>
        <section className={styles.card}>
          <div className={styles.cardHead}>
            <h2>Live decisions</h2>
            <span className={styles.muted}>{(activity.data?.decisions ?? []).length} recent</span>
          </div>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>Time</th>
                  <th>Model</th>
                  <th>Client</th>
                  <th>Outcome</th>
                  <th>Account</th>
                  <th className={styles.num}>Status</th>
                </tr>
              </thead>
              <tbody>
                {(activity.data?.decisions ?? []).slice(0, 25).map((d) => (
                  <tr key={d.id} title={d.cause}>
                    <td className={styles.muted}>{when(d.at)}</td>
                    <td className={styles.mono}>{d.model}</td>
                    <td>{d.client || '--'}</td>
                    <td>
                      <span className={styles.pill} data-tone={outcomeTone(d)}>
                        {d.outcome.replace(/_/g, ' ')}
                      </span>
                    </td>
                    <td className={styles.mono}>{label(d.auth_id)}</td>
                    <td className={styles.num}>{d.result?.status ?? '--'}</td>
                  </tr>
                ))}
                {(activity.data?.decisions ?? []).length === 0 && (
                  <tr>
                    <td colSpan={6} className={styles.empty}>
                      No decisions yet.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>

        <section className={styles.card}>
          <div className={styles.cardHead}>
            <h2>Sticky sessions</h2>
            <span className={styles.muted}>TTL {sessions.data?.ttl ?? '--'}</span>
          </div>
          <div className={styles.tableWrap}>
            <table className={styles.table}>
              <thead>
                <tr>
                  <th>Session</th>
                  <th>Client</th>
                  <th>Account</th>
                  <th className={styles.num}>Requests</th>
                  <th>Last seen</th>
                </tr>
              </thead>
              <tbody>
                {(sessions.data?.sessions ?? []).slice(0, 25).map((s) => (
                  <tr key={s.session}>
                    <td className={styles.mono}>{short(s.session)}</td>
                    <td>{s.client || s.kind}</td>
                    <td className={styles.mono}>{s.account || s.auth_id}</td>
                    <td className={styles.num}>{s.requests}</td>
                    <td className={styles.muted}>{relative(s.last_seen)}</td>
                  </tr>
                ))}
                {(sessions.data?.sessions ?? []).length === 0 && (
                  <tr>
                    <td colSpan={5} className={styles.empty}>
                      No active sessions.
                    </td>
                  </tr>
                )}
              </tbody>
            </table>
          </div>
        </section>
      </div>
    </div>
  );
}
