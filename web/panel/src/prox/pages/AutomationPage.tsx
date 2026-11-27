/** Ignition & Alerts: five-hour window ignition, notifications, and the ClickHouse sink. */

import { useState } from 'react';
import { Button } from '@/components/ui/Button';
import { proxPost, useProx } from '../api';
import { compact, providerColor, providerLabel, relative, when } from '../format';
import type { Ignition, IgnitionPlan, NotifyEntry, SinkStatus } from '../types';
import styles from './ProxPage.module.scss';

const planTone = (p: IgnitionPlan) =>
  p.status === 'active' || p.status === 'firing' || p.status === 'confirming'
    ? 'good'
    : p.status === 'waiting' || p.status === 'idle'
      ? 'warn'
      : p.status === 'paused' || p.status === 'blocked'
        ? 'bad'
        : undefined;

const GOOD_EVENTS = new Set(['fired', 'success', 'manual_fired', 'credential_restored']);
const BAD_EVENTS = new Set([
  'credential_invalid',
  'rate_limited',
  'no_window_started',
  'manual_failure',
  'paused_until_tomorrow',
]);
const eventTone = (kind: string) =>
  GOOD_EVENTS.has(kind) ? 'good' : BAD_EVENTS.has(kind) ? 'bad' : 'warn';

export function AutomationPage() {
  const ignition = useProx<Ignition>('/ignition', 15_000);
  const notify = useProx<{ entries: NotifyEntry[] | null }>('/notify/log', 30_000);
  const sink = useProx<SinkStatus>('/sink', 30_000);
  const [message, setMessage] = useState('');

  const act = async (fn: () => Promise<unknown>, ok: string) => {
    try {
      await fn();
      setMessage(ok);
    } catch (err) {
      setMessage(err instanceof Error ? err.message : String(err));
    }
    void ignition.reload();
    void notify.reload();
  };

  const plans = ignition.data?.plans ?? [];
  const events = ignition.data?.events ?? [];
  const entries = notify.data?.entries ?? [];
  const s = sink.data;

  return (
    <div className={styles.page}>
      <header className={styles.header}>
        <div>
          <h1 className={styles.title}>Ignition & Alerts</h1>
          <p className={styles.meta}>
            ignition {ignition.data?.enabled === false ? 'off' : 'on'} · {entries.length}{' '}
            notifications
          </p>
        </div>
        <div className={styles.controls}>
          {message && <span className={styles.muted}>{message}</span>}
          <Button
            variant="secondary"
            size="sm"
            onClick={() => act(() => proxPost('/notify/test'), 'Test sent')}
          >
            Send test alert
          </Button>
        </div>
      </header>

      {ignition.error && (
        <div className={styles.error}>prox is off or unreachable: {ignition.error}</div>
      )}

      <section className={styles.card}>
        <div className={styles.cardHead}>
          <h2>Window ignition</h2>
          <span className={styles.muted}>Starts the next five-hour window right after a reset</span>
        </div>
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>Account</th>
                <th>Provider</th>
                <th>Status</th>
                <th>Window resets</th>
                <th>Next ignition</th>
                <th>Model</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {plans.map((p) => (
                <tr key={p.auth_id} title={p.reason}>
                  <td className={styles.mono}>{p.display}</td>
                  <td>
                    <i
                      className={styles.swatch}
                      style={{ background: providerColor(p.provider) }}
                    />
                    {providerLabel(p.provider)}
                  </td>
                  <td>
                    <span className={styles.pill} data-tone={planTone(p)}>
                      {p.status}
                    </span>
                    {p.reason && (
                      <span className={styles.muted}> {p.reason.replace(/_/g, ' ')}</span>
                    )}
                  </td>
                  <td className={styles.muted}>
                    {p.resets_at ? `${when(p.resets_at)} · ${relative(p.resets_at)}` : '--'}
                  </td>
                  <td className={styles.muted}>{p.next_at ? when(p.next_at) : '--'}</td>
                  <td className={styles.mono}>{p.model || '--'}</td>
                  <td className={styles.num}>
                    <Button
                      variant="ghost"
                      size="sm"
                      onClick={() =>
                        act(
                          () => proxPost('/ignition/fire', { auth_id: p.auth_id }),
                          `Igniting ${p.display}`
                        )
                      }
                    >
                      Ignite now
                    </Button>
                  </td>
                </tr>
              ))}
              {plans.length === 0 && (
                <tr>
                  <td colSpan={7} className={styles.empty}>
                    Ignition is off. Set plugins.configs.prox.ignition.enabled to turn it on.
                  </td>
                </tr>
              )}
            </tbody>
          </table>
        </div>
      </section>

      <div className={styles.grid2}>
        <section className={styles.card}>
          <div className={styles.cardHead}>
            <h2>Ignition events</h2>
          </div>
          <ul className={styles.list}>
            {events.slice(0, 20).map((e, i) => (
              <li key={`${e.at}:${i}`}>
                <span className={styles.muted}>{when(e.at)}</span>
                <span>
                  <span className={styles.pill} data-tone={eventTone(e.kind)}>
                    {e.kind.replace(/_/g, ' ')}
                  </span>{' '}
                  <span className={styles.mono}>{e.auth_id}</span>
                  {e.model && <span className={styles.muted}> · {e.model}</span>}
                  {e.detail && <span className={styles.muted}> · {e.detail}</span>}
                </span>
              </li>
            ))}
            {events.length === 0 && <li className={styles.empty}>No ignition events yet.</li>}
          </ul>
        </section>

        <section className={styles.card}>
          <div className={styles.cardHead}>
            <h2>Notifications</h2>
          </div>
          <ul className={styles.list}>
            {entries.slice(0, 20).map((n, i) => (
              <li key={`${n.at}:${i}`} title={n.error || n.body}>
                <span className={styles.muted}>{when(n.at)}</span>
                <span>
                  <span
                    className={styles.pill}
                    data-tone={!n.delivered ? 'bad' : n.level === 'warning' ? 'warn' : undefined}
                  >
                    {n.delivered ? n.level : 'failed'}
                  </span>{' '}
                  {n.title}
                </span>
              </li>
            ))}
            {entries.length === 0 && <li className={styles.empty}>No notifications sent yet.</li>}
          </ul>
        </section>
      </div>

      <section className={styles.card}>
        <div className={styles.cardHead}>
          <h2>ClickHouse</h2>
          <span
            className={styles.pill}
            data-tone={!s?.enabled ? undefined : s.ready && !s.error ? 'good' : 'bad'}
          >
            {!s?.enabled ? 'off' : s.ready && !s.error ? 'connected' : 'error'}
          </span>
        </div>
        <div className={styles.tiles} style={{ border: 0, borderRadius: 0 }}>
          <div className={styles.tile}>
            <span className={styles.tileLabel}>Rows sent</span>
            <span className={styles.tileValue}>{compact(s?.sent ?? 0)}</span>
          </div>
          <div className={styles.tile}>
            <span className={styles.tileLabel}>Queued</span>
            <span className={styles.tileValue}>{compact(s?.queued ?? 0)}</span>
          </div>
          <div className={styles.tile}>
            <span className={styles.tileLabel}>Dropped</span>
            <span className={styles.tileValue}>{compact(s?.dropped ?? 0)}</span>
            {s?.error && <span className={styles.tileSub}>{s.error}</span>}
          </div>
        </div>
      </section>
    </div>
  );
}
