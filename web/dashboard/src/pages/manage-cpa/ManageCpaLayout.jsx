import React from 'react';
import { NavLink, Outlet, useLocation } from 'react-router-dom';

// ManageCpaLayout — parent route for the /manage-cpa/* tree.
//
// Renders a single shared header (title + subtitle) and a tab strip whose
// active state is derived from the URL via NavLink's `end` / `className`
// callback. The actual tab content is rendered by the matched child route
// through <Outlet />, so each tab is a standalone page that owns its own
// data loading and state.
//
// This is the first place in the dashboard that uses nested <Routes>, so
// <Outlet /> is imported from react-router-dom. The App.jsx sidebar still
// highlights "Manage CPA" for every sub-route — the existing isActive()
// logic already handles that via startsWith('/manage-cpa/').
const TABS = [
  { to: '/manage-cpa', label: 'Overview', end: true },
  { to: '/manage-cpa/providers', label: 'AI Providers' },
  { to: '/manage-cpa/runtime-config', label: 'Runtime Config' },
  { to: '/manage-cpa/revisions', label: 'Revisions' },
  { to: '/manage-cpa/imports', label: 'Imports' },
];

export default function ManageCpaLayout() {
  const location = useLocation();
  return (
    <>
      <div className="main__header">
        <div>
          <h1 className="main__title">Manage CPA</h1>
          <div className="main__subtitle">
            Operate the running NixLLM server: inspect config, manage
            every AI provider account, and edit the runtime settings
            backed by the PG-first control plane.
          </div>
        </div>
        <div className="row gap-sm">
          <span className="dim mono" style={{ fontSize: 11 }}>
            {location.pathname}
          </span>
        </div>
      </div>

      <nav className="tabs" role="tablist" aria-label="Manage CPA sections">
        {TABS.map((t) => (
          <NavLink
            key={t.to}
            to={t.to}
            end={t.end}
            className={({ isActive }) => `tab${isActive ? ' active' : ''}`}
            role="tab"
          >
            {t.label}
          </NavLink>
        ))}
      </nav>

      <Outlet />
    </>
  );
}
