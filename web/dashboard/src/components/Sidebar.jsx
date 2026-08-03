import React, { useCallback, useEffect } from 'react';
import { NavLink } from 'react-router-dom';

// Sidebar — the primary navigation chrome for the authenticated dashboard.
//
// Responsibilities:
//   * Render the brand, grouped nav links, and footer (version + sign out).
//   * Strong active indicator (left accent bar + accent-dim bg) via NavLink.
//   * Collapsible rail mode ("collapsed" prop) — icon-only column, persisted
//     by the caller in localStorage under `nixllm.sidebar.collapsed`.
//   * Mobile drawer ("mobileOpen" prop) — slides in as an overlay below 900px;
//     the caller owns the open/close state and renders the matching top bar.
//   * Sticky while the main content scrolls (handled in CSS).
//
// The component is pure/react-router-driven: it reads `currentPath` only for
// the escape-key + link-click close-on-mobile behavior, since NavLink computes
// active state itself.
export default function Sidebar({
  onLogout,
  onToggleCollapsed,
  collapsed = false,
  mobileOpen = false,
  onCloseMobile,
}) {
  const closeMobile = useCallback(() => onCloseMobile?.(), [onCloseMobile]);

  // Escape closes the mobile drawer. Kept here (not in App) so the sidebar
  // owns its own keyboard affordance, mirroring the Modal pattern.
  useEffect(() => {
    if (!mobileOpen) return;
    function onKey(e) {
      if (e.key === 'Escape') closeMobile();
    }
    document.addEventListener('keydown', onKey);
    return () => document.removeEventListener('keydown', onKey);
  }, [mobileOpen, closeMobile]);

  return (
    <>
      {mobileOpen && <div className="sidebar-backdrop" onClick={closeMobile} aria-hidden />}
      <aside className={`sidebar${mobileOpen ? ' sidebar--open' : ''}`}>
        <div className="sidebar__header">
          <span className="sidebar__brand">
            <span className="sidebar__brand-mark" dangerouslySetInnerHTML={{ __html: BRAND_SVG }} />
            <span className="sidebar__brand-text">NixLLM</span>
          </span>
        </div>

        <button
          type="button"
          className={`sidebar__collapse-btn${collapsed ? ' is-collapsed' : ''}`}
          onClick={onToggleCollapsed}
          title={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          aria-expanded={!collapsed}
        >
          <ChevronIcon />
        </button>

        <nav className="sidebar__nav" aria-label="Primary">
          {NAV_GROUPS.map((group) => (
            <div className="sidebar__section" key={group.label}>
              <div className="sidebar__section-label">{group.label}</div>
              {group.items.map((item) => (
                <NavLink
                  key={item.to}
                  to={item.to}
                  end={item.end}
                  className={({ isActive }) =>
                    `sidebar__link${isActive ? ' active' : ''}`
                  }
                  onClick={closeMobile}
                  title={item.label}
                >
                  <span className="sidebar__link-icon">{ICON_MAP[item.icon]}</span>
                  <span className="sidebar__link-label">{item.label}</span>
                </NavLink>
              ))}
            </div>
          ))}
        </nav>

        <div className="sidebar__footer">
          <span className="sidebar__version">v1.0.0</span>
          <button
            type="button"
            className="sidebar__signout"
            onClick={onLogout}
            title="Sign out"
          >
            <span className="sidebar__link-icon">
              <LogoutIcon />
            </span>
            <span className="sidebar__link-label">Sign out</span>
          </button>
        </div>
      </aside>
    </>
  );
}

// NAV_GROUPS — three logical sections. "Overview" groups the operate-the-service
// items; "Analysis" groups the read-only observability surfaces — Usage Stats,
// Recent Events, and Errors. Errors was split out of the Usage Stats tab into
// its own page so operators can triage failures without losing the events
// view; Recent Events was in turn split out of the Usage Stats page so the
// event log can be paged/searched independently of the aggregate KPIs and
// charts. All three sit next to each other in the sidebar so the pages share
// a context. "System" holds settings. Order matches the prior flat list so
// muscle memory transfers. Sign out is footer-only and not duplicated here.
const NAV_GROUPS = [
  {
    label: 'Overview',
    items: [
      { to: '/', label: 'Dashboard', icon: 'home', end: true },
      { to: '/api-keys', label: 'API Keys', icon: 'key' },
      { to: '/internal-users', label: 'Internal Users', icon: 'users' },
      { to: '/model-groups', label: 'Model Groups', icon: 'layers' },
      { to: '/models', label: 'Models Catalog', icon: 'cube' },
      { to: '/error-messages', label: 'Error Messages', icon: 'alert' },
      { to: '/api-tokens', label: 'API Management', icon: 'shield' },
      { to: '/upstream-providers', label: 'Upstream Providers', icon: 'layers' },
      { to: '/developer', label: 'Developer', icon: 'code' },
      { to: '/manage-cpa', label: 'Manage CPA', icon: 'cpa' },
    ],
  },
  {
    label: 'Analysis',
    items: [
      { to: '/usage', label: 'Usage Stats', icon: 'chart' },
      { to: '/recent-events', label: 'Recent Events', icon: 'list' },
      { to: '/errors', label: 'Errors', icon: 'bug' },
      { to: '/cooldown-providers', label: 'Cooldown Providers', icon: 'snow' },
      { to: '/session-affinity', label: 'Session Affinity', icon: 'link' },
      { to: '/upstream-sync-log', label: 'Upstream Providers', icon: 'layers' },
      { to: '/model-health', label: 'Model Health', icon: 'pulse' },
    ],
  },
  {
    label: 'System',
    items: [
      { to: '/settings', label: 'Settings', icon: 'gear' },
      { to: '/branding', label: 'Branding', icon: 'tag' },
      { to: '/import-export', label: 'Import / Export', icon: 'transfer' },
    ],
  },
];

// --- Icons ----------------------------------------------------------------
// Inline SVGs keep the bundle dependency-free. All use currentColor so they
// inherit the link text color (muted → accent on active).

// ChevronIcon — single icon rotated via CSS (180° when collapsed). Using one
// icon + transform keeps the motion continuous instead of swapping elements.
function ChevronIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
      <path d="M10 3L5 8l5 5" />
    </svg>
  );
}
function LogoutIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M6 2H4a1 1 0 0 0-1 1v10a1 1 0 0 0 1 1h2" />
      <path d="M10 4l3 4-3 4M13 8H6" />
    </svg>
  );
}

const BRAND_SVG = `<svg viewBox="0 0 64 64" xmlns="http://www.w3.org/2000/svg">
  <path d="M16 18 L32 46 L48 18" fill="none" stroke="#5eead4" stroke-width="6"
        stroke-linecap="round" stroke-linejoin="round" />
  <circle cx="32" cy="46" r="3" fill="#5eead4" />
</svg>`;

const ICON_MAP = {
  home: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M2.5 7L8 2l5.5 5M4 6v7.5h8V6" /></svg>,
  key: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><circle cx="5" cy="11" r="2.5" /><path d="M7 9l6-6M10 6l2 2" strokeLinecap="round" /></svg>,
  chart: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><path d="M2 14V2M2 14h12M5 11V7M8 11V4M11 11V8" /></svg>,
  users: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><circle cx="6" cy="5.5" r="2" /><path d="M2.5 13.5c0-2 1.5-3.5 3.5-3.5s3.5 1.5 3.5 3.5" /><circle cx="11" cy="6.5" r="1.7" /><path d="M9 13.5c0-1.6 1-3 2.5-3s2.5 1.4 2.5 3" /></svg>,
  cube: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><path d="M8 1l6 3.5v7L8 15l-6-3.5v-7L8 1zM8 1v14M2 4.5l6 3.5 6-3.5" /></svg>,
  alert: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><path d="M8 1l7 13H1L8 1zM8 6v4M8 11.5v0.5" strokeLinecap="round" /></svg>,
  shield: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><path d="M8 1l6 2v5c0 3.5-2.5 6.5-6 7.5-3.5-1-6-4-6-7.5V3l6-2z" /><path d="M5.5 8l1.8 1.8L10.5 6.5" strokeLinecap="round" /></svg>,
  code: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6 4L2 8l4 4M10 4l4 4-4 4" /></svg>,
  bug: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M4 8V6a4 4 0 0 1 8 0v2" /><path d="M3 8h10" /><path d="M4 8v3a3 3 0 0 0 3 3h2a3 3 0 0 0 3-3V8" /><path d="M2 6l2 1M14 6l-2 1M2 11l2-1M14 11l-2-1M6 2.5L5 1M10 2.5L11 1" /></svg>,
  list: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M5 4h9M5 8h9M5 12h9" /><circle cx="2" cy="4" r="1" fill="currentColor" stroke="none" /><circle cx="2" cy="8" r="1" fill="currentColor" stroke="none" /><circle cx="2" cy="12" r="1" fill="currentColor" stroke="none" /></svg>,
  cpa: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><rect x="2" y="2" width="5" height="5" rx="1" /><rect x="9" y="2" width="5" height="5" rx="1" /><rect x="2" y="9" width="5" height="5" rx="1" /><rect x="9" y="9" width="5" height="5" rx="1" /></svg>,
  layers: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round"><path d="M8 1l6 3-6 3-6-3 6-3zM2 8l6 3 6-3M2 11l6 3 6-3" /></svg>,
  gear: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><circle cx="8" cy="8" r="2" /><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.5 1.5M11.5 11.5L13 13M3 13l1.5-1.5M11.5 4.5L13 3" strokeLinecap="round" /></svg>,
  tag: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M2 8V3a1 1 0 0 1 1-1h5l6 6-6 6-6-6z" /><circle cx="5" cy="5" r="1" fill="currentColor" stroke="none" /></svg>,
  snow: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><path d="M8 1v14M1 8h14M3 3l10 10M13 3L3 13M8 3L6 1M8 3l2-2M8 13l-2 2M8 13l2 2M3 8L1 6M3 8l-2 2M13 8l2-2M13 8l2 2" /></svg>,
  link: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6.5 9.5a2.5 2.5 0 0 0 3.5 0l2-2a2.5 2.5 0 0 0-3.5-3.5l-1 1" /><path d="M9.5 6.5a2.5 2.5 0 0 0-3.5 0l-2 2a2.5 2.5 0 0 0 3.5 3.5l1-1" /></svg>,
  pulse: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M1 8h3l1.5-4 3 9 2-6 1 1h3.5" /></svg>,
  transfer: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M2 5h11M9 2l3 3-3 3M14 11H3M7 8l-3 3 3 3" /></svg>,
};
