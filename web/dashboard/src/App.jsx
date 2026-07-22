import React, { useCallback, useEffect, useState } from 'react';
import { Routes, Route, Navigate, useLocation, Link, useNavigate } from 'react-router-dom';
import {
  getStoredToken, setStoredToken, clearStoredToken, verifyToken, ApiError,
} from './api/client.js';
import LoginPage from './pages/LoginPage.jsx';
import ApiKeysPage from './pages/ApiKeysPage.jsx';
import ApiKeyDetailPage from './pages/ApiKeyDetailPage.jsx';
import UsageStatsPage from './pages/UsageStatsPage.jsx';
import InternalUsersPage from './pages/InternalUsersPage.jsx';
import InternalUserDetailPage from './pages/InternalUserDetailPage.jsx';
import ModelsCatalogPage from './pages/ModelsCatalogPage.jsx';
import ErrorMessagesPage from './pages/ErrorMessagesPage.jsx';
import SettingsPage from './pages/SettingsPage.jsx';
import ApiTokensPage from './pages/ApiTokensPage.jsx';
import ApiTokenDetailPage from './pages/ApiTokenDetailPage.jsx';
import DeveloperPage from './pages/DeveloperPage.jsx';
import ManageCpaLayout from './pages/manage-cpa/ManageCpaLayout.jsx';
import OverviewTab from './pages/manage-cpa/OverviewTab.jsx';
import ProvidersTab from './pages/manage-cpa/ProvidersTab.jsx';
import RawConfigTab from './pages/manage-cpa/RawConfigTab.jsx';
import { ToastProvider } from './components/Toast.jsx';

// App is the root component and owns the auth session.
//
// Auth model: no separate dashboard login endpoint — the dashboard reuses
// the existing /v0/management routes protected by MANAGEMENT_PASSWORD (or
// the localPassword fallback when running on the same host as the server).
// The user enters that password on the login screen; we store it in
// localStorage and send it as `Authorization: Bearer <password>` on every
// API call. A 401/403 from any route drops the token and redirects to /login.
export default function App() {
  const [authed, setAuthed] = useState(() => !!getStoredToken());
  const [verifying, setVerifying] = useState(() => !!getStoredToken());
  const location = useLocation();
  const navigate = useNavigate();

  // On first load, if a token is present in storage, verify it against the
  // server before trusting it. A stale token (e.g. server-side password
  // rotation) drops back to the login screen.
  useEffect(() => {
    let cancelled = false;
    if (!getStoredToken()) {
      setVerifying(false);
      return;
    }
    verifyToken()
      .then(() => { if (!cancelled) setAuthed(true); })
      .catch(() => {
        if (cancelled) return;
        clearStoredToken();
        setAuthed(false);
      })
      .finally(() => { if (!cancelled) setVerifying(false); });
    return () => { cancelled = true; };
  }, []);

  // Global 401/403 listener: any failed API call with those statuses logs
  // the user out and bounces to /login. This avoids wrapping every page in
  // its own error handler.
  useEffect(() => {
    const handler = (event) => {
      const err = event.detail;
      if (err instanceof ApiError && (err.status === 401 || err.status === 403)) {
        clearStoredToken();
        setAuthed(false);
        navigate('/login', { replace: true, state: { from: location.pathname } });
      }
    };
    window.addEventListener('nixllm:api-error', handler);
    return () => window.removeEventListener('nixllm:api-error', handler);
  }, [location.pathname, navigate]);

  const handleLogin = useCallback((password) => {
    setStoredToken(password);
    setAuthed(true);
  }, []);

  const handleLogout = useCallback(() => {
    clearStoredToken();
    setAuthed(false);
    navigate('/login', { replace: true });
  }, [navigate]);

  if (verifying) {
    return (
      <div className="empty-state">
        <div className="spinner" style={{ margin: '0 auto 12px' }} />
        <div className="empty-state__title">Verifying session…</div>
      </div>
    );
  }

  if (!authed) {
    return (
      <ToastProvider>
        <Routes>
          <Route path="/login" element={<LoginPage onLogin={handleLogin} />} />
          <Route path="*" element={<Navigate to="/login" replace state={{ from: location.pathname }} />} />
        </Routes>
      </ToastProvider>
    );
  }

  return (
    <ToastProvider>
      <div className="app-shell">
        <Sidebar onLogout={handleLogout} currentPath={location.pathname} />
        <main className="main">
          <Routes>
            <Route path="/login" element={<Navigate to="/" replace />} />
            <Route path="/" element={<ApiKeysPage />} />
            <Route path="/api-keys/:id" element={<ApiKeyDetailPage />} />
            <Route path="/usage" element={<UsageStatsPage />} />
            <Route path="/internal-users" element={<InternalUsersPage />} />
            <Route path="/internal-users/:id" element={<InternalUserDetailPage />} />
            <Route path="/models" element={<ModelsCatalogPage />} />
            <Route path="/error-messages" element={<ErrorMessagesPage />} />
            <Route path="/api-tokens" element={<ApiTokensPage />} />
            <Route path="/api-tokens/:id" element={<ApiTokenDetailPage />} />
            <Route path="/developer" element={<DeveloperPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/manage-cpa" element={<ManageCpaLayout />}>
              <Route index element={<OverviewTab />} />
              <Route path="providers" element={<ProvidersTab />} />
              <Route path="raw-config" element={<RawConfigTab />} />
            </Route>
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
      </div>
    </ToastProvider>
  );
}

function Sidebar({ onLogout, currentPath }) {
  const links = [
    { to: '/', label: 'API Keys', icon: 'key' },
    { to: '/usage', label: 'Usage Stats', icon: 'chart' },
    { to: '/internal-users', label: 'Internal Users', icon: 'users' },
    { to: '/models', label: 'Models Catalog', icon: 'cube' },
    { to: '/error-messages', label: 'Error Messages', icon: 'alert' },
    { to: '/api-tokens', label: 'API Management', icon: 'shield' },
    { to: '/developer', label: 'Developer', icon: 'code' },
    { to: '/manage-cpa', label: 'Manage CPA', icon: 'cpa' },
    { to: '/settings', label: 'Settings', icon: 'gear' },
  ];
  return (
    <aside className="sidebar">
      <div className="sidebar__brand">
        <span className="sidebar__brand-mark" dangerouslySetInnerHTML={{ __html: BRAND_SVG }} />
        <span>NixLLM</span>
      </div>
      <nav className="sidebar__nav">
        {links.map((l) => (
          <Link
            key={l.to}
            to={l.to}
            className={`sidebar__link ${isActive(currentPath, l.to) ? 'active' : ''}`}
          >
            <span style={{ width: 16, height: 16 }}>{ICON_MAP[l.icon]}</span>
            <span>{l.label}</span>
          </Link>
        ))}
      </nav>
      <div className="sidebar__footer">
        <div className="row row--between">
          <span>v1.0.0</span>
          <button onClick={onLogout} style={{ padding: '4px 8px', fontSize: 12 }}>
            Sign out
          </button>
        </div>
      </div>
    </aside>
  );
}

function isActive(currentPath, to) {
  if (to === '/') return currentPath === '/';
  return currentPath === to || currentPath.startsWith(`${to}/`);
}

const BRAND_SVG = `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg">
  <path d="M16 18 L32 46 L48 18" fill="none" stroke="#5eead4" stroke-width="6"
        stroke-linecap="round" stroke-linejoin="round" />
  <circle cx="32" cy="46" r="3" fill="#5eead4" />
</svg>`;

const ICON_MAP = {
  key: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><circle cx="5" cy="11" r="2.5" /><path d="M7 9l6-6M10 6l2 2" strokeLinecap="round" /></svg>,
  chart: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><path d="M2 14V2M2 14h12M5 11V7M8 11V4M11 11V8" /></svg>,
  users: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><circle cx="6" cy="5.5" r="2" /><path d="M2.5 13.5c0-2 1.5-3.5 3.5-3.5s3.5 1.5 3.5 3.5" /><circle cx="11" cy="6.5" r="1.7" /><path d="M9 13.5c0-1.6 1-3 2.5-3s2.5 1.4 2.5 3" /></svg>,
  cube: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><path d="M8 1l6 3.5v7L8 15l-6-3.5v-7L8 1zM8 1v14M2 4.5l6 3.5 6-3.5" /></svg>,
  alert: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><path d="M8 1l7 13H1L8 1zM8 6v4M8 11.5v0.5" strokeLinecap="round" /></svg>,
  shield: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><path d="M8 1l6 2v5c0 3.5-2.5 6.5-6 7.5-3.5-1-6-4-6-7.5V3l6-2z" /><path d="M5.5 8l1.8 1.8L10.5 6.5" strokeLinecap="round" /></svg>,
  code: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6 4L2 8l4 4M10 4l4 4-4 4" /></svg>,
  cpa: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><rect x="2" y="2" width="5" height="5" rx="1" /><rect x="9" y="2" width="5" height="5" rx="1" /><rect x="2" y="9" width="5" height="5" rx="1" /><rect x="9" y="9" width="5" height="5" rx="1" /></svg>,
  gear: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><circle cx="8" cy="8" r="2" /><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.5 1.5M11.5 11.5L13 13M3 13l1.5-1.5M11.5 4.5L13 3" strokeLinecap="round" /></svg>,
};
