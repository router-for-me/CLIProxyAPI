import React, { useCallback, useEffect, useState } from 'react';
import { Routes, Route, Navigate, useLocation, useNavigate } from 'react-router-dom';
import {
  getStoredToken, setStoredToken, clearStoredToken, verifyToken, ApiError,
} from './api/client.js';
import Sidebar from './components/Sidebar.jsx';
import LoginPage from './pages/LoginPage.jsx';
import ApiKeysPage from './pages/ApiKeysPage.jsx';
import ApiKeyDetailPage from './pages/ApiKeyDetailPage.jsx';
import UsageStatsPage from './pages/UsageStatsPage.jsx';
import RecentEventsPage from './pages/RecentEventsPage.jsx';
import ErrorsPage from './pages/ErrorsPage.jsx';
import InternalUsersPage from './pages/InternalUsersPage.jsx';
import InternalUserDetailPage from './pages/InternalUserDetailPage.jsx';
import ModelGroupsPage from './pages/ModelGroupsPage.jsx';
import ModelGroupDetailPage from './pages/ModelGroupDetailPage.jsx';
import ModelsCatalogPage from './pages/ModelsCatalogPage.jsx';
import ErrorMessagesPage from './pages/ErrorMessagesPage.jsx';
import SettingsPage from './pages/SettingsPage.jsx';
import BrandingPage from './pages/BrandingPage.jsx';
import ApiTokensPage from './pages/ApiTokensPage.jsx';
import ApiTokenDetailPage from './pages/ApiTokenDetailPage.jsx';
import DeveloperPage from './pages/DeveloperPage.jsx';
import UpstreamProvidersPage from './pages/UpstreamProvidersPage.jsx';
import UpstreamSyncLogPage from './pages/UpstreamSyncLogPage.jsx';
import ModelHealthPage from './pages/ModelHealthPage.jsx';
import CooldownProvidersPage from './pages/CooldownProvidersPage.jsx';
import ManageCpaLayout from './pages/manage-cpa/ManageCpaLayout.jsx';
import OverviewTab from './pages/manage-cpa/OverviewTab.jsx';
import ProvidersTab from './pages/manage-cpa/ProvidersTab.jsx';
import RawConfigTab from './pages/manage-cpa/RawConfigTab.jsx';
import { ToastProvider } from './components/Toast.jsx';

const SIDEBAR_COLLAPSED_KEY = 'nixllm.sidebar.collapsed';

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

  // Collapsible rail preference: persisted in localStorage so a collapsed
  // sidebar stays collapsed across reloads. Defaults to expanded.
  const [collapsed, setCollapsed] = useState(
    () => localStorage.getItem(SIDEBAR_COLLAPSED_KEY) === '1',
  );
  // Mobile drawer state: ephemeral, not persisted. Closed by route change.
  const [mobileOpen, setMobileOpen] = useState(false);

  const toggleCollapsed = useCallback(() => {
    setCollapsed((prev) => {
      const next = !prev;
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, next ? '1' : '0');
      return next;
    });
  }, []);

  // Close the mobile drawer whenever the route changes — operators expect a
  // tap-then-navigate-then-close flow without an extra dismiss click.
  useEffect(() => { setMobileOpen(false); }, [location.pathname]);

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
      <div className={`app-shell${collapsed ? ' app-shell--collapsed' : ''}`}>
        <div className="topbar">
          <button
            type="button"
            className="topbar__menu"
            onClick={() => setMobileOpen((v) => !v)}
            aria-label="Toggle menu"
          >
            <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
              <path d="M2 4h12M2 8h12M2 12h12" />
            </svg>
          </button>
          <span className="sidebar__brand-mark" dangerouslySetInnerHTML={{ __html: BRAND_SVG }} />
          <span className="topbar__title">NixLLM</span>
        </div>
        <Sidebar
          onLogout={handleLogout}
          onToggleCollapsed={toggleCollapsed}
          collapsed={collapsed}
          mobileOpen={mobileOpen}
          onCloseMobile={() => setMobileOpen(false)}
        />
        <main className="main">
          <Routes>
            <Route path="/login" element={<Navigate to="/" replace />} />
            <Route path="/" element={<ApiKeysPage />} />
            <Route path="/api-keys/:id" element={<ApiKeyDetailPage />} />
  <Route path="/usage" element={<UsageStatsPage />} />
  <Route path="/recent-events" element={<RecentEventsPage />} />
  <Route path="/errors" element={<ErrorsPage />} />
            <Route path="/cooldown-providers" element={<CooldownProvidersPage />} />
            <Route path="/upstream-sync-log" element={<UpstreamSyncLogPage />} />
            <Route path="/model-health" element={<ModelHealthPage />} />
            <Route path="/internal-users" element={<InternalUsersPage />} />
            <Route path="/internal-users/:id" element={<InternalUserDetailPage />} />
            <Route path="/model-groups" element={<ModelGroupsPage />} />
            <Route path="/model-groups/:id" element={<ModelGroupDetailPage />} />
            <Route path="/models" element={<ModelsCatalogPage />} />
            <Route path="/error-messages" element={<ErrorMessagesPage />} />
            <Route path="/api-tokens" element={<ApiTokensPage />} />
            <Route path="/api-tokens/:id" element={<ApiTokenDetailPage />} />
            <Route path="/developer" element={<DeveloperPage />} />
            <Route path="/upstream-providers" element={<UpstreamProvidersPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/branding" element={<BrandingPage />} />
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

// BRAND_SVG — duplicated from Sidebar.jsx for the mobile top bar, where the
// sidebar's own (hidden) brand mark is off-screen.
const BRAND_SVG = `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg">
  <path d="M16 18 L32 46 L48 18" fill="none" stroke="#5eead4" stroke-width="6"
        stroke-linecap="round" stroke-linejoin="round" />
  <circle cx="32" cy="46" r="3" fill="#5eead4" />
</svg>`;
