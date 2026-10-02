import React, { useEffect, useMemo, useState } from 'react';
import {
  getManagementLoginSettings, putManagementLoginSettings,
  getManagementLoginEvents, clearManagementLoginEvents,
  getManagementLoginBans, deleteManagementLoginBan, clearManagementLoginBans,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner } from '../components/Primitives.jsx';
import Pager from '../components/Pager.jsx';
import { useToast } from '../components/Toast.jsx';

const PAGE_SIZE = 50;

const OUTCOME_OPTIONS = [
  { value: '', label: 'All outcomes' },
  { value: 'success', label: 'Success' },
  { value: 'invalid_key', label: 'Invalid key' },
  { value: 'missing_key', label: 'Missing key' },
  { value: 'remote_disabled', label: 'Remote disabled' },
  { value: 'banned', label: 'Rejected (banned)' },
  { value: 'ban_started', label: 'Ban started' },
];

export default function ManagementLoginSecurityPage() {
  const settingsReq = useAsync(() => getManagementLoginSettings(), []);
  const bansReq = useAsync(() => getManagementLoginBans(), []);
  const [page, setPage] = useState(1);
  const [ip, setIp] = useState('');
  const [outcome, setOutcome] = useState('');
  const [clearing, setClearing] = useState(false);
  const toast = useToast();

  const listParams = useMemo(() => ({
    ip, outcome, page, page_size: PAGE_SIZE,
  }), [ip, outcome, page]);

  const list = useAsync(() => getManagementLoginEvents(listParams), [JSON.stringify(listParams)]);

  useEffect(() => { setPage(1); }, [ip, outcome]);

  if (settingsReq.error?.status === 503 || bansReq.error?.status === 503 || list.error?.status === 503) {
    return (
      <div className="card">
        <h3 className="card__title">Management Login Security</h3>
        <p className="muted">
          This page requires the PostgreSQL store. Set <code>PGSTORE_DSN</code> and restart the server.
        </p>
      </div>
    );
  }

  const events = list.data?.events || [];
  const total = list.data?.total || 0;
  const totalPages = Math.max(1, Math.ceil(total / PAGE_SIZE));
  const bans = bansReq.data?.bans || [];

  async function handleClearEvents() {
    if (!window.confirm('Clear all management login events? This cannot be undone.')) return;
    setClearing(true);
    try {
      const res = await clearManagementLoginEvents();
      toast.success(`Cleared ${Number(res?.deleted || 0).toLocaleString()} event(s)`);
      setPage(1);
      list.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to clear events');
    } finally {
      setClearing(false);
    }
  }

  async function handleLiftBan(banIp) {
    try {
      await deleteManagementLoginBan(banIp);
      toast.success(`Ban lifted for ${banIp}`);
      bansReq.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to lift ban');
    }
  }

  async function handleClearBans() {
    if (!window.confirm('Clear all active bans? This cannot be undone.')) return;
    try {
      const res = await clearManagementLoginBans();
      toast.success(`Cleared ${Number(res?.removed || 0).toLocaleString()} ban(s)`);
      bansReq.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to clear bans');
    }
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Management Login Security</h1>
          <div className="main__subtitle">
            Brute-force policy and attempt log for the management API auth path.
          </div>
        </div>
        <button onClick={() => { settingsReq.reload(); bansReq.reload(); list.reload(); }}>Refresh</button>
      </div>

      <LoginSecuritySettingsCard settingsReq={settingsReq} />

      <div className="card">
        <div className="row row--between" style={{ marginBottom: 4 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Active bans</h3>
          {bans.length > 0 && (
            <button className="btn--danger" onClick={handleClearBans}>Clear all bans</button>
          )}
        </div>
        <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
          In-memory ban state keyed by client IP. Cleared on server restart.
        </p>
        {bansReq.loading && <Spinner label="Loading bans…" />}
        {bansReq.error && <ErrorBanner error={bansReq.error} onRetry={bansReq.reload} />}
        {!bansReq.loading && bans.length === 0 && <p className="muted">No active bans.</p>}
        {bans.length > 0 && (
          <div style={{ overflowX: 'auto' }}>
            <table className="table">
              <thead>
                <tr>
                  <th>IP</th>
                  <th>Blocked until</th>
                  <th style={{ textAlign: 'right' }}>Attempts</th>
                  <th>Last activity</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {bans.map((b) => (
                  <tr key={b.ip} className="table__row">
                    <td className="mono">{b.ip}</td>
                    <td className="mono">{b.blocked_until}</td>
                    <td style={{ textAlign: 'right' }} className="mono">{b.count}</td>
                    <td className="mono">{b.last_activity}</td>
                    <td><button onClick={() => handleLiftBan(b.ip)}>Lift</button></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </div>

      <div className="card">
        <div className="row row--between" style={{ marginBottom: 16 }}>
          <h3 className="card__title" style={{ margin: 0 }}>Login attempts</h3>
          <button className="btn--danger" onClick={handleClearEvents} disabled={clearing}>
            {clearing ? 'Clearing…' : 'Clear all events'}
          </button>
        </div>
        <div className="grid grid--2" style={{ marginBottom: 16 }}>
          <label className="form__row">
            <span className="form__label">IP</span>
            <input type="text" value={ip} placeholder="exact IP" onChange={(e) => setIp(e.target.value)} />
          </label>
          <label className="form__row">
            <span className="form__label">Outcome</span>
            <select value={outcome} onChange={(e) => setOutcome(e.target.value)}>
              {OUTCOME_OPTIONS.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
            </select>
          </label>
        </div>

        <div style={{ overflowX: 'auto' }}>
          <table className="table">
            <thead>
              <tr>
                <th>Time</th>
                <th>IP</th>
                <th>Outcome</th>
                <th style={{ textAlign: 'right' }}>Attempts</th>
                <th>Local</th>
                <th>Reason</th>
                <th>User agent</th>
              </tr>
            </thead>
            <tbody>
              {list.loading && Array.from({ length: 5 }).map((_, i) => (
                <tr key={i} className="skeleton-row">
                  {Array.from({ length: 7 }).map((__, j) => <td key={j}><span className="skeleton-line" /></td>)}
                </tr>
              ))}
              {!list.loading && list.error && (
                <tr><td colSpan={7}><ErrorBanner error={list.error} onRetry={list.reload} /></td></tr>
              )}
              {!list.loading && !list.error && events.length === 0 && (
                <tr><td colSpan={7} style={{ textAlign: 'center', padding: '24px 8px' }}>No login events recorded.</td></tr>
              )}
              {!list.loading && events.map((e) => (
                <tr key={e.id} className="table__row">
                  <td className="mono" style={{ whiteSpace: 'nowrap' }}>{e.created_at}</td>
                  <td className="mono">{e.ip || '—'}</td>
                  <td><span className={`badge ${e.outcome === 'success' ? 'badge--ok' : 'badge--muted'}`}>{e.outcome}</span></td>
                  <td style={{ textAlign: 'right' }} className="mono">{e.attempt_count}</td>
                  <td>{e.local ? 'yes' : 'no'}</td>
                  <td className="mono" style={{ maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.reason || ''}>{e.reason || '—'}</td>
                  <td className="mono" style={{ maxWidth: 260, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }} title={e.user_agent || ''}>{e.user_agent || '—'}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        <Pager page={page} totalPages={totalPages} total={total} pageSize={PAGE_SIZE} onPageChange={setPage} />
      </div>
    </>
  );
}

function LoginSecuritySettingsCard({ settingsReq }) {
  const toast = useToast();
  const [draft, setDraft] = useState(null);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    if (settingsReq.data?.settings) setDraft(settingsReq.data.settings);
  }, [settingsReq.data]);

  function set(patch) { setDraft((d) => ({ ...d, ...patch })); }

  async function handleSave() {
    setSaving(true);
    try {
      await putManagementLoginSettings({
        enabled: !!draft.enabled,
        max_failed_attempts: Number(draft.max_failed_attempts) || 5,
        ban_duration_seconds: Number(draft.ban_duration_seconds) || 1800,
        failure_window_seconds: Number(draft.failure_window_seconds) || 0,
        cleanup_interval_seconds: Number(draft.cleanup_interval_seconds) || 3600,
        idle_timeout_seconds: Number(draft.idle_timeout_seconds) || 7200,
        log_successes: !!draft.log_successes,
        retention_days: Number(draft.retention_days) || 30,
      });
      toast.success('Management login settings saved');
      settingsReq.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to save settings');
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="card">
      <h3 className="card__title">Brute-force policy</h3>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        Controls IP banning and event logging for the management API login path.
      </p>
      {settingsReq.loading && !draft && <Spinner label="Loading settings…" />}
      {settingsReq.error && !draft && <ErrorBanner error={settingsReq.error} onRetry={settingsReq.reload} />}
      {draft && (
        <>
          <div className="grid grid--2">
            <FileToggleRow label="Banning enabled" checked={!!draft.enabled} onChange={(v) => set({ enabled: v })} />
            <FileToggleRow label="Log successful attempts" checked={!!draft.log_successes} onChange={(v) => set({ log_successes: v })} />
            <label className="form__row">
              <span className="form__label">Max failed attempts</span>
              <input type="number" min={1} max={100} value={draft.max_failed_attempts ?? 5} onChange={(e) => set({ max_failed_attempts: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Ban duration (seconds)</span>
              <input type="number" min={1} value={draft.ban_duration_seconds ?? 1800} onChange={(e) => set({ ban_duration_seconds: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Failure window (seconds, 0 = lifetime)</span>
              <input type="number" min={0} value={draft.failure_window_seconds ?? 0} onChange={(e) => set({ failure_window_seconds: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Cleanup interval (seconds)</span>
              <input type="number" min={60} value={draft.cleanup_interval_seconds ?? 3600} onChange={(e) => set({ cleanup_interval_seconds: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Idle timeout (seconds)</span>
              <input type="number" min={60} value={draft.idle_timeout_seconds ?? 7200} onChange={(e) => set({ idle_timeout_seconds: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Event retention (days)</span>
              <input type="number" min={1} max={365} value={draft.retention_days ?? 30} onChange={(e) => set({ retention_days: e.target.value })} />
            </label>
          </div>
          <div className="form__actions" style={{ marginTop: 16 }}>
            <button onClick={handleSave} disabled={saving}>{saving ? 'Saving…' : 'Save settings'}</button>
          </div>
        </>
      )}
    </div>
  );
}

function FileToggleRow({ label, checked, onChange }) {
  return (
    <label className="row gap-sm" style={{ cursor: 'pointer' }}>
      <input type="checkbox" checked={!!checked} onChange={(e) => onChange(e.target.checked)} style={{ width: 'auto' }} />
      <span className="form__label" style={{ margin: 0 }}>{label}</span>
    </label>
  );
}
