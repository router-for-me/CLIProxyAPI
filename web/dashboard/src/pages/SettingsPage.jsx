import React, { useEffect, useState } from 'react';
import {
  getStoredToken, setStoredToken, clearStoredToken, getHealth,
  getAlertSettings, putAlertSettings,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, Stat } from '../components/Primitives.jsx';
import DynamicSettingsCard from '../components/DynamicSettingsCard.jsx';
import { useToast } from '../components/Toast.jsx';

// SettingsPage — dashboard session / connection info plus every operator
// configuration that fits under "Settings": the alert/notification sweep
// (separate form because its shape is unique) and the entire runtime_config
// snapshot rendered dynamically from its scalar keys. Branding lives on its
// own page. Server file config is intentionally not exposed here (callers
// should use /v0/management/config directly); the dashboard scope limits
// itself to the PG-backed features it manages.
export default function SettingsPage() {
  const toast = useToast();
  const { data: alive, error, loading, reload } = useAsync(() => getHealth(), []);

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Settings</h1>
          <div className="main__subtitle">Alert notifications, runtime configuration, and dashboard session info.</div>
        </div>
        <button onClick={reload}>Refresh</button>
      </div>

      <div className="grid grid--3">
        <Stat label="Server reachable" value={loading ? '…' : alive ? 'yes' : 'no'}
          delta={error ? error.message : 'GET /healthz'} />
        <Stat label="Auth storage" value="localStorage" delta="key: nixllm.dashboard.token" />
        <Stat label="Dashboard version" value="v1.0.0" />
      </div>

      <AlertSettingsCard toast={toast} />

      <div className="card">
        <DynamicSettingsCard />
      </div>

      <div className="card">
        <h3 className="card__title">Session</h3>
        <p className="muted">
          The dashboard stores your master password in <code>localStorage</code> and
          sends it as <code>Authorization: Bearer</code> on every management API call.
          Clear it to force a re-login on next visit.
        </p>
        <div className="form__row" style={{ marginTop: 16 }}>
          <label className="form__label">Current token</label>
          <div className="copyable">{maskToken(getStoredToken())}</div>
        </div>
        <div className="form__actions">
          <CopyTokenButton />
          <SignOutButton />
        </div>
      </div>

      <div className="card">
        <h3 className="card__title">Configuration</h3>
        <ul className="list-bare">
          <li><strong>Dev port:</strong> <code className="mono">9173</code></li>
          <li><strong>API base:</strong> <code className="mono">/v0/management</code> (proxied)</li>
          <li><strong>Required env vars on server:</strong>
            <ul>
              <li className="mono">MANAGEMENT_PASSWORD</li>
              <li className="mono">PGSTORE_DSN</li>
            </ul>
          </li>
        </ul>
      </div>
    </>
  );
}

// AlertSettingsCard edits the alert/notification sweep configuration. It appears
// under Settings so the header bell stays focused on viewing alerts; the sweep
// toggles/thresholds live here. Reads + writes the alert_settings singleton.
function AlertSettingsCard(_props) {
  const toast = useToast();
  const settingsReq = useAsync(() => getAlertSettings(), []);
  const [saving, setSaving] = useState(false);
  const [draft, setDraft] = useState(null);

  useEffect(() => {
    if (settingsReq.data?.settings) setDraft(settingsReq.data.settings);
  }, [settingsReq.data]);

  function set(patch) { setDraft((d) => ({ ...d, ...patch })); }

  async function handleSave() {
    setSaving(true);
    try {
      const body = {
        enabled: draft.enabled,
        interval_seconds: Number(draft.interval_seconds) || 60,
        suppression_minutes: Number(draft.suppression_minutes) || 60,
        enable_user_budget: draft.enable_user_budget,
        enable_api_key_budget: draft.enable_api_key_budget,
        enable_error_rate: draft.enable_error_rate,
        enable_provider_cooldown: draft.enable_provider_cooldown,
        error_rate_threshold: Number(draft.error_rate_threshold) || 0,
        error_window_minutes: Number(draft.error_window_minutes) || 5,
      };
      await putAlertSettings(body);
      toast.success('Alert settings saved');
      settingsReq.reload();
    } catch (err) {
      toast.error(err?.message || 'Failed to save alert settings');
    } finally {
      setSaving(false);
    }
  }

  return (
    <div className="card">
      <div className="row row--between" style={{ marginBottom: 4 }}>
        <h3 className="card__title" style={{ margin: 0 }}>Alert / Notification settings</h3>
      </div>
      <p className="muted" style={{ marginTop: 4, marginBottom: 16 }}>
        Controls the background alert sweep (Analysis → Alerts): which detectors
        run, how often, and how aggressively recurring conditions are surfaced.
      </p>

      {settingsReq.loading && !draft && <Spinner label="Loading alert settings…" />}
      {settingsReq.error && !draft && <ErrorBanner error={settingsReq.error} onRetry={settingsReq.reload} />}
      {draft && (
        <>
          <label className="row gap-sm" style={{ cursor: 'pointer', marginBottom: 16 }}>
            <input type="checkbox" checked={!!draft.enabled} onChange={(e) => set({ enabled: e.target.checked })} style={{ width: 'auto' }} />
            <span className="form__label" style={{ margin: 0 }}>Alert sweep enabled</span>
          </label>

          <div className="grid grid--2">
            <label className="form__row">
              <span className="form__label">Interval (seconds)</span>
              <input type="number" min={15} value={draft.interval_seconds ?? 60} onChange={(e) => set({ interval_seconds: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Suppression (minutes)</span>
              <input type="number" min={1} value={draft.suppression_minutes ?? 60} onChange={(e) => set({ suppression_minutes: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Error-rate threshold (errors/min)</span>
              <input type="number" step="0.1" min={0} value={draft.error_rate_threshold ?? 0.5} onChange={(e) => set({ error_rate_threshold: e.target.value })} />
            </label>
            <label className="form__row">
              <span className="form__label">Error-rate window (minutes)</span>
              <input type="number" min={1} max={60} value={draft.error_window_minutes ?? 5} onChange={(e) => set({ error_window_minutes: e.target.value })} />
            </label>
          </div>

          <div style={{ marginTop: 8 }}>
            <div className="form__label" style={{ marginBottom: 8 }}>Detectors</div>
            <div className="grid grid--2">
              <ToggleRow label="Internal user max spend" checked={!!draft.enable_user_budget} onChange={(v) => set({ enable_user_budget: v })} />
              <ToggleRow label="API key max spend" checked={!!draft.enable_api_key_budget} onChange={(v) => set({ enable_api_key_budget: v })} />
              <ToggleRow label="Error rate" checked={!!draft.enable_error_rate} onChange={(v) => set({ enable_error_rate: v })} />
              <ToggleRow label="Provider cooldown" checked={!!draft.enable_provider_cooldown} onChange={(v) => set({ enable_provider_cooldown: v })} />
            </div>
          </div>

          <div className="form__actions" style={{ marginTop: 16 }}>
            <button onClick={handleSave} disabled={saving}>{saving ? 'Saving…' : 'Save alert settings'}</button>
          </div>
        </>
      )}
    </div>
  );
}

function ToggleRow({ label, checked, onChange }) {
  return (
    <label className="row gap-sm" style={{ cursor: 'pointer' }}>
      <input type="checkbox" checked={!!checked} onChange={(e) => onChange(e.target.checked)} style={{ width: 'auto' }} />
      <span className="form__label" style={{ margin: 0 }}>{label}</span>
    </label>
  );
}

function CopyTokenButton() {
  function handleClick() {
    const token = getStoredToken();
    if (!token) return;
    navigator.clipboard?.writeText(token).then(() => {
      alert('Auth token copied to clipboard.');
    });
  }
  return <button onClick={handleClick}>Copy token</button>;
}

function SignOutButton() {
  function handleClick() {
    clearStoredToken();
    window.location.href = '/login';
  }
  return <button className="danger" onClick={handleClick}>Sign out</button>;
}

function maskToken(token) {
  if (!token) return '(no token stored)';
  if (token.length <= 8) return '•'.repeat(token.length);
  return token.slice(0, 4) + '•'.repeat(Math.min(token.length - 8, 24)) + token.slice(-4);
}
