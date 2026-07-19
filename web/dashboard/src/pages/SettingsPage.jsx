import React from 'react';
import { getStoredToken, setStoredToken, clearStoredToken, getHealth } from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { Spinner, ErrorBanner, Stat } from '../components/Primitives.jsx';

// SettingsPage — read-only session info + token/sign-out helpers.
// We don't expose server-side config here (callers should use /v0/management/config
// directly); the dashboard scope limits itself to PG-backed features.
export default function SettingsPage() {
  const { data: alive, error, loading, reload } = useAsync(() => getHealth(), []);
  const token = getStoredToken();

  function copyCurrentToken() {
    if (!token) return;
    navigator.clipboard?.writeText(token).then(() => {
      alert('Auth token copied to clipboard.');
    });
  }

  function signOut() {
    clearStoredToken();
    window.location.href = '/login';
  }

  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Settings</h1>
          <div className="main__subtitle">Dashboard session and server connection info.</div>
        </div>
        <button onClick={reload}>Refresh</button>
      </div>

      <div className="grid grid--3">
        <Stat label="Server reachable" value={loading ? '…' : alive ? 'yes' : 'no'}
          delta={error ? error.message : 'GET /healthz'} />
        <Stat label="Auth storage" value="localStorage" delta="key: nixllm.dashboard.token" />
        <Stat label="Dashboard version" value="v1.0.0" />
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
          <div className="copyable">{maskToken(token)}</div>
        </div>
        <div className="form__actions">
          <button onClick={copyCurrentToken}>Copy token</button>
          <button className="danger" onClick={signOut}>Sign out</button>
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

function maskToken(token) {
  if (!token) return '(no token stored)';
  if (token.length <= 8) return '•'.repeat(token.length);
  return token.slice(0, 4) + '•'.repeat(Math.min(token.length - 8, 24)) + token.slice(-4);
}
