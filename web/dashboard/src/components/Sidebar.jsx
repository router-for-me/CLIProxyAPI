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

// NAV_GROUPS — two logical sections. "Overview" groups the operate-the-service
// items; "System" holds settings. Order matches the prior flat list so muscle
// memory transfers. Sign out is footer-only and not duplicated here.
const NAV_GROUPS = [
  {
    label: 'Overview',
    items: [
      { to: '/', label: 'API Keys', icon: 'key', end: true },
      { to: '/usage', label: 'Usage Stats', icon: 'chart' },
      { to: '/internal-users', label: 'Internal Users', icon: 'users' },
      { to: '/models', label: 'Models Catalog', icon: 'cube' },
      { to: '/error-messages', label: 'Error Messages', icon: 'alert' },
      { to: '/api-tokens', label: 'API Management', icon: 'shield' },
      { to: '/developer', label: 'Developer', icon: 'code' },
      { to: '/manage-cpa', label: 'Manage CPA', icon: 'cpa' },
    ],
  },
  {
    label: 'System',
    items: [{ to: '/settings', label: 'Settings', icon: 'gear' }],
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
