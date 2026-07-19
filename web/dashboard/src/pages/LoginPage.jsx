import React, { useState, useEffect, useRef } from 'react';
import { useNavigate, useLocation } from 'react-router-dom';
import { verifyToken, setStoredToken, getHealth, ApiError } from '../api/client.js';

// LoginPage — single-step auth.
//
// The dashboard does not maintain its own user database. The user enters the
// server's management password (set via MANAGEMENT_PASSWORD env var on the
// Go server, or the auto-generated localPassword when running in TUI mode),
// which the dashboard stores in localStorage and sends as Bearer auth on
// every /v0/management request. We verify the password by issuing a probe
// GET /v0/management/config — 200 means accepted, 403 means rejected.
export default function LoginPage({ onLogin }) {
  const [password, setPassword] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [error, setError] = useState('');
  const [serverAlive, setServerAlive] = useState(null); // null = probing
  const inputRef = useRef(null);
  const navigate = useNavigate();
  const location = useLocation();

  useEffect(() => {
    inputRef.current?.focus();
  }, []);

  useEffect(() => {
    getHealth()
      .then(() => setServerAlive(true))
      .catch(() => setServerAlive(false));
  }, []);

  async function handleSubmit(e) {
    e.preventDefault();
    if (!password) return;
    setSubmitting(true);
    setError('');
    // Store the candidate password transiently so verifyToken's fetch picks
    // it up. If verification fails, clear it immediately.
    setStoredToken(password);
    try {
      await verifyToken();
      onLogin(password);
      const dest = location.state?.from || '/';
      navigate(dest, { replace: true });
    } catch (err) {
      setStoredToken('');
      if (err instanceof ApiError && err.status === 403) {
        setError('Invalid password. Try again.');
      } else {
        setError(err.message || 'Unable to reach the management API.');
      }
    } finally {
      setSubmitting(false);
    }
  }

  return (
    <div className="login-screen">
      <form className="login-card" onSubmit={handleSubmit}>
        <div className="login-card__brand">
          <span dangerouslySetInnerHTML={{ __html: BRAND_SVG }} />
          <div>
            <div className="login-card__brand-text">NixLLM</div>
            <div className="login-card__subtitle">Management Dashboard</div>
          </div>
        </div>

        {serverAlive === false && (
          <div className="error-banner" style={{ marginBottom: 16 }}>
            Cannot reach the CLIProxyAPI server. Make sure it is running and
            reachable at the configured host (default http://127.0.0.1:8317).
          </div>
        )}
        {error && <div className="error-banner">{error}</div>}

        <div className="form__row">
          <label className="form__label" htmlFor="password">Master Password</label>
          <input
            id="password"
            ref={inputRef}
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            placeholder="MANAGEMENT_PASSWORD"
            autoComplete="current-password"
            disabled={submitting}
          />
          <div className="form__hint">
            This is the value of the <code>MANAGEMENT_PASSWORD</code> env var
            configured on the CLIProxyAPI server (or the auto-printed local
            password when running in TUI mode).
          </div>
        </div>

        <div className="form__actions">
          <button type="submit" className="primary" disabled={submitting || !password}>
            {submitting ? 'Verifying…' : 'Sign in'}
          </button>
        </div>
      </form>
    </div>
  );
}

const BRAND_SVG = `<svg viewBox="0 0 64 64" width="36" height="36" xmlns="http://www.w3.org/2000/svg">
  <path d="M16 18 L32 46 L48 18" fill="none" stroke="#5eead4" stroke-width="6"
        stroke-linecap="round" stroke-linejoin="round" />
  <circle cx="32" cy="46" r="3" fill="#5eead4" />
</svg>`;
