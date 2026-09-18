import React, { useCallback, useEffect, useRef, useState } from 'react';
import { Routes, Route, Navigate, useLocation, useNavigate } from 'react-router-dom';
import {
  getStoredToken, setStoredToken, clearStoredToken, verifyToken, ApiError,
  getUnreadAlertCount, getCpaLatestVersion,
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
import AutoRoutersPage from './pages/AutoRoutersPage.jsx';
import AutoRouterPage from './pages/AutoRouterPage.jsx';
import AutoRouterAnalysisPage from './pages/AutoRouterAnalysisPage.jsx';
import ModelsCatalogPage from './pages/ModelsCatalogPage.jsx';
import ErrorMessagesPage from './pages/ErrorMessagesPage.jsx';
import SettingsPage from './pages/SettingsPage.jsx';
import BrandingPage from './pages/BrandingPage.jsx';
import ApiTokensPage from './pages/ApiTokensPage.jsx';
import ApiTokenDetailPage from './pages/ApiTokenDetailPage.jsx';
import DeveloperPage from './pages/DeveloperPage.jsx';
import UpstreamProvidersPage from './pages/UpstreamProvidersPage.jsx';
import HealthPage from './pages/upstream-providers/HealthPage.jsx';
import ProxyPoolsPage from './pages/ProxyPoolsPage.jsx';
import UpstreamProviderEditorPage from './pages/upstream-provider-editor/index.jsx';
import UpstreamSyncLogPage from './pages/UpstreamSyncLogPage.jsx';
import ModelHealthPage from './pages/ModelHealthPage.jsx';
import CooldownProvidersPage from './pages/CooldownProvidersPage.jsx';
import SessionAffinityPage from './pages/SessionAffinityPage.jsx';
import AlertsPage from './pages/AlertsPage.jsx';
import DashboardPage from './pages/DashboardPage.jsx';
import ImportExportPage from './pages/ImportExportPage.jsx';
import BackupPage from './pages/BackupPage.jsx';
import LogsPage from './pages/LogsPage.jsx';
import PlaygroundPage from './pages/PlaygroundPage.jsx';
import ManageCpaLayout from './pages/manage-cpa/ManageCpaLayout.jsx';
import OverviewTab from './pages/manage-cpa/OverviewTab.jsx';
import ProvidersTab from './pages/manage-cpa/ProvidersTab.jsx';
import ConfigRevisionsPage from './pages/ConfigRevisionsPage.jsx';
import ConfigImportsPage from './pages/ConfigImportsPage.jsx';
import LiteLLMPage from './pages/litellm/LiteLLMPage.jsx';
import QuotaPage from './pages/Quota.jsx';
import { ToastProvider } from './components/Toast.jsx';
import AlertsDropdown from './components/AlertsDropdown.jsx';

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

  // Unread-alert count for the header bell badge. Polled periodically and
  // refreshed instantly when any view mutates the feed (nixllm:alerts-changed).
  const [alertsUnread, setAlertsUnread] = useState(0);
  const [alertsOpen, setAlertsOpen] = useState(false);
  const bellRef = useRef(null);

  // Version identity for the sidebar footer: the running build as a single
  // unified version string (e.g. 7.2.104-0.0.4) plus whether a newer NixLLM
  // release exists upstream. Fetched once per session from /latest-version;
  // any failure keeps the defaults so an offline server doesn't break the UI.
  const [versionInfo, setVersionInfo] = useState(null);

  useEffect(() => {
    if (!authed) return;
    let cancelled = false;
    getCpaLatestVersion()
      .then((r) => { if (!cancelled && r) setVersionInfo(r); })
      .catch(() => { /* keep default version label */ });
    return () => { cancelled = true; };
  }, [authed]);

  const runningVersion = versionInfo?.['running-version'] || 'dev';
  const nixllmUpdateAvailable =
    // Ignore update hints when we can't pin our own running version or the
    // latest NixLLM tag hasn't been resolved.
    !!versionInfo?.['nixllm-latest-version']
    && !/dev|unknown/i.test(runningVersion)
    && semverGreater(versionInfo['nixllm-latest-version'], runningVersion);

  // Poll the unread badge on an independent cadence, and refresh immediately
  // after alert mutations anywhere in the app. Errors are swallowed — a PG-less
  // or momentarily-unavailable feed simply hides the badge.
  useEffect(() => {
    if (!authed) return;
    let cancelled = false;
    const poll = () => {
      getUnreadAlertCount()
        .then((r) => { if (!cancelled) setAlertsUnread(Number(r?.unread) || 0); })
        .catch(() => { /* hide badge on transient errors */ });
    };
    poll();
    const id = setInterval(poll, 30000);
    const handler = () => poll();
    window.addEventListener('nixllm:alerts-changed', handler);
    return () => {
      cancelled = true;
      clearInterval(id);
      window.removeEventListener('nixllm:alerts-changed', handler);
    };
  }, [authed]);

  const toggleCollapsed = useCallback(() => {
    setCollapsed((prev) => {
      const next = !prev;
      localStorage.setItem(SIDEBAR_COLLAPSED_KEY, next ? '1' : '0');
      return next;
    });
  }, []);

  // Keyboard shortcut: Cmd+B or Ctrl+B to toggle sidebar collapse mode
  useEffect(() => {
    if (!authed) return;
    function onKeyDown(e) {
      if ((e.metaKey || e.ctrlKey) && e.key.toLowerCase() === 'b') {
        const target = e.target;
        if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) {
          return;
        }
        e.preventDefault();
        toggleCollapsed();
      }
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [authed, toggleCollapsed]);

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
        <header className="topbar">
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
          <span className="topbar__brand">
            <span className="sidebar__brand-mark" dangerouslySetInnerHTML={{ __html: BRAND_SVG }} />
            <span className="topbar__title">NixLLM</span>
          </span>
          <div className="topbar__spacer" />
          <div className="topbar__actions">
            <button
              ref={bellRef}
              type="button"
              className={`topbar__bell${alertsUnread > 0 ? ' has-unread' : ''}${alertsOpen ? ' is-open' : ''}`}
              onClick={() => setAlertsOpen((v) => !v)}
              aria-label="Open alerts"
              aria-haspopup="dialog"
              aria-expanded={alertsOpen}
              title={alertsUnread > 0 ? `${alertsUnread} unread alert(s)` : 'Alerts'}
            >
              <BellIcon />
              {alertsUnread > 0 && (
                <span className="topbar__bell-badge">{alertsUnread > 99 ? '99+' : alertsUnread}</span>
              )}
            </button>
          </div>
        </header>
        <Sidebar
          onLogout={handleLogout}
          onToggleCollapsed={toggleCollapsed}
          collapsed={collapsed}
          mobileOpen={mobileOpen}
          onCloseMobile={() => setMobileOpen(false)}
          version={versionInfo?.['version'] || 'dev'}
          latestVersion={versionInfo?.['nixllm-latest-version'] || ''}
          coreVersion={versionInfo?.['running-core-version'] || 'unknown'}
          updateAvailable={nixllmUpdateAvailable}
        />
        <main className="main">
          <Routes>
            <Route path="/login" element={<Navigate to="/" replace />} />
            <Route path="/" element={<DashboardPage />} />
            <Route path="/api-keys" element={<ApiKeysPage />} />
            <Route path="/api-keys/:id" element={<ApiKeyDetailPage />} />
  <Route path="/usage" element={<UsageStatsPage />} />
  <Route path="/recent-events" element={<RecentEventsPage />} />
  <Route path="/errors" element={<ErrorsPage />} />
  <Route path="/analysis/auto-routers" element={<AutoRouterAnalysisPage />} />
            <Route path="/cooldown-providers" element={<CooldownProvidersPage />} />
            <Route path="/session-affinity" element={<SessionAffinityPage />} />
            <Route path="/alerts" element={<AlertsPage />} />
            <Route path="/upstream-sync-log" element={<UpstreamSyncLogPage />} />
            <Route path="/model-health" element={<ModelHealthPage />} />
            <Route path="/internal-users" element={<InternalUsersPage />} />
            <Route path="/internal-users/:id" element={<InternalUserDetailPage />} />
            <Route path="/model-groups" element={<ModelGroupsPage />} />
            <Route path="/model-groups/:id" element={<ModelGroupDetailPage />} />
            <Route path="/auto-routers" element={<AutoRoutersPage />} />
            <Route path="/auto-routers/new" element={<AutoRouterPage />} />
            <Route path="/auto-routers/:id" element={<AutoRouterPage />} />
            <Route path="/models" element={<ModelsCatalogPage />} />
            <Route path="/error-messages" element={<ErrorMessagesPage />} />
            <Route path="/api-tokens" element={<ApiTokensPage />} />
            <Route path="/api-tokens/:id" element={<ApiTokenDetailPage />} />
            <Route path="/developer" element={<DeveloperPage />} />
            <Route path="/upstream-providers" element={<UpstreamProvidersPage />} />
            <Route path="/upstream-providers/new" element={<UpstreamProviderEditorPage />} />
            <Route path="/upstream-providers/health" element={<HealthPage />} />
            <Route path="/upstream-providers/:id" element={<Navigate to="/upstream-providers/:id/overview" replace />} />
            <Route path="/upstream-providers/:id/:tab" element={<UpstreamProviderEditorPage />} />
            <Route path="/proxy-pools" element={<ProxyPoolsPage />} />
            <Route path="/playground" element={<PlaygroundPage />} />
            <Route path="/settings" element={<SettingsPage />} />
            <Route path="/branding" element={<BrandingPage />} />
            <Route path="/import-export" element={<ImportExportPage />} />
            <Route path="/backup" element={<BackupPage />} />
            <Route path="/logs" element={<LogsPage />} />
            <Route path="/manage-cpa" element={<ManageCpaLayout />}>
              <Route index element={<OverviewTab />} />
              <Route path="providers" element={<ProvidersTab />} />
              <Route path="revisions" element={<ConfigRevisionsPage />} />
              <Route path="imports" element={<ConfigImportsPage />} />
            </Route>
            <Route path="/litellm" element={<LiteLLMPage />} />
            <Route path="/quota" element={<QuotaPage />} />
            <Route path="*" element={<Navigate to="/" replace />} />
          </Routes>
        </main>
        {alertsOpen && <AlertsDropdown bellRef={bellRef} onClose={() => setAlertsOpen(false)} />}
      </div>
    </ToastProvider>
  );
}

// semverGreater — true when `a` is a strictly newer semver version than `b`.
// Handles tags with or without a leading "v" and dotted numeric segments
// (including pre-release suffixes, which are ignored for the comparison).
export function semverGreater(a, b) {
  const parse = (s) => String(s || '')
    .replace(/^v/i, '')
    .split('.')
    .map((seg) => { const n = parseInt(seg, 10); return Number.isNaN(n) ? 0 : n; });
  const pa = parse(a);
  const pb = parse(b);
  const len = Math.max(pa.length, pb.length);
  for (let i = 0; i < len; i++) {
    const x = pa[i] || 0;
    const y = pb[i] || 0;
    if (x !== y) return x > y;
  }
  return false;
}

// BRAND_SVG — duplicated from Sidebar.jsx for the mobile top bar, where the
// sidebar's own (hidden) brand mark is off-screen.
const BRAND_SVG = `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg">
  <path d="M16 18 L32 46 L48 18" fill="none" stroke="#5eead4" stroke-width="6"
        stroke-linecap="round" stroke-linejoin="round" />
  <circle cx="32" cy="46" r="3" fill="#5eead4" />
</svg>`;

// BellIcon — inline SVG for the header alerts button, matching the dependency-
// free icon style used across the sidebar.
function BellIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      {/* Feather "bell" path — exactly symmetric about x=12 so the artwork is
          centered in the button box regardless of stroke weight. */}
      <path d="M18 8A6 6 0 0 0 6 8c0 7-3 9-3 9h18s-3-2-3-9" />
      <path d="M13.73 21a2 2 0 0 1-3.46 0" />
    </svg>
  );
}
