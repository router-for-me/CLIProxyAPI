import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { NavLink } from 'react-router-dom';
import { useTheme } from '../hooks/useTheme.jsx';

const GROUPS_KEY = 'nixllm.sidebar.groups';
const FAVORITES_KEY = 'nixllm.sidebar.favorites';

// Sidebar — primary navigation chrome for the authenticated dashboard.
//
// Features:
//   * Disambiguated labels & unique icons (Upstream Providers vs Sync Log).
//   * Quick navigation filter bar with '/' focus shortcut and Escape clear.
//   * Pinned / Favorite routes system with star toggles.
//   * Collapsible section accordions with localStorage persistence.
//   * High-end collapsed rail mode with floating rich tooltips.
//   * Footer with running version info, update indicator, and sign out button.
export default function Sidebar({
  onLogout,
  onToggleCollapsed,
  collapsed = false,
  mobileOpen = false,
  onCloseMobile,
  version = 'dev',
  latestVersion = '',
  coreVersion = '',
  updateAvailable = false,
}) {
  const closeMobile = useCallback(() => onCloseMobile?.(), [onCloseMobile]);
  const { theme, setTheme } = useTheme();

  const [query, setQuery] = useState('');
  const [collapsedGroups, setCollapsedGroups] = useState(() => {
    try {
      return JSON.parse(localStorage.getItem(GROUPS_KEY) || '{}');
    } catch {
      return {};
    }
  });

  const [favorites, setFavorites] = useState(() => {
    try {
      return JSON.parse(localStorage.getItem(FAVORITES_KEY) || '[]');
    } catch {
      return [];
    }
  });

  const [railTip, setRailTip] = useState(null);
  const searchRef = useRef(null);
  const navRef = useRef(null);
  const searchExpandRef = useRef(false);

  // Toggle favorite status for a route
  const toggleFavorite = useCallback((toRoute, e) => {
    e.preventDefault();
    e.stopPropagation();
    setFavorites((prev) => {
      const isFav = prev.includes(toRoute);
      const next = isFav ? prev.filter((r) => r !== toRoute) : [...prev, toRoute];
      localStorage.setItem(FAVORITES_KEY, JSON.stringify(next));
      return next;
    });
  }, []);

  // Toggle accordion section collapse
  const toggleGroup = useCallback((groupLabel) => {
    setCollapsedGroups((prev) => {
      const next = { ...prev, [groupLabel]: !prev[groupLabel] };
      localStorage.setItem(GROUPS_KEY, JSON.stringify(next));
      return next;
    });
  }, []);

  // Keyboard affordances (Escape to clear/close, '/' to focus search)
  useEffect(() => {
    function onKeyDown(e) {
      if (e.key === 'Escape') {
        if (query) {
          setQuery('');
          searchRef.current?.blur();
        } else if (mobileOpen) {
          closeMobile();
        }
      } else if (e.key === '/' && !collapsed) {
        const target = e.target;
        if (target && (target.tagName === 'INPUT' || target.tagName === 'TEXTAREA' || target.isContentEditable)) {
          return;
        }
        e.preventDefault();
        searchRef.current?.focus();
        searchRef.current?.select();
      }
    }
    window.addEventListener('keydown', onKeyDown);
    return () => window.removeEventListener('keydown', onKeyDown);
  }, [collapsed, mobileOpen, query, closeMobile]);

  // Clear filter when sidebar is collapsed to rail mode
  useEffect(() => {
    if (collapsed) setQuery('');
  }, [collapsed]);

  // Focus search after expanding rail via search click
  useEffect(() => {
    if (!collapsed && searchExpandRef.current) {
      searchExpandRef.current = false;
      searchRef.current?.focus();
    }
  }, [collapsed]);

  // Dismiss floating rail tooltip on scroll
  useEffect(() => {
    if (!collapsed) return;
    const navEl = navRef.current;
    if (!navEl) return;
    const handleScroll = () => setRailTip(null);
    navEl.addEventListener('scroll', handleScroll, { passive: true });
    return () => navEl.removeEventListener('scroll', handleScroll);
  }, [collapsed]);

  const handleSearchClick = useCallback(() => {
    if (collapsed) {
      searchExpandRef.current = true;
      onToggleCollapsed();
    } else {
      searchRef.current?.focus();
    }
  }, [collapsed, onToggleCollapsed]);

  const showTip = useCallback((label, category, el) => {
    if (!collapsed) return;
    const rect = el.getBoundingClientRect();
    setRailTip({
      label,
      category,
      top: rect.top + rect.height / 2,
    });
  }, [collapsed]);

  const hideTip = useCallback(() => setRailTip(null), []);

  const isSearching = query.trim().length > 0;
  const normalizedQuery = query.trim().toLowerCase();

  // All flat items for lookup
  const allNavItems = useMemo(() => {
    const items = [];
    NAV_GROUPS.forEach((g) => {
      g.items.forEach((it) => {
        items.push({ ...it, groupLabel: g.label });
      });
    });
    return items;
  }, []);

  // Filtered navigation structure including Favorites
  const displayGroups = useMemo(() => {
    let resultGroups = [];

    // Favorite items group (if any exist and not searching or if search matches favorites)
    if (favorites.length > 0) {
      const favItems = allNavItems
        .filter((it) => favorites.includes(it.to))
        .filter((it) => !isSearching || it.label.toLowerCase().includes(normalizedQuery));

      if (favItems.length > 0) {
        resultGroups.push({
          label: 'Favorites',
          isFavorites: true,
          items: favItems,
        });
      }
    }

    // Standard groups
    NAV_GROUPS.forEach((g) => {
      const matchingItems = g.items.filter(
        (it) => !isSearching || it.label.toLowerCase().includes(normalizedQuery),
      );
      if (matchingItems.length > 0) {
        resultGroups.push({
          label: g.label,
          items: matchingItems,
        });
      }
    });

    return resultGroups;
  }, [favorites, allNavItems, isSearching, normalizedQuery]);

  const versionTitle = updateAvailable
    ? `NixLLM update available: ${latestVersion}`
    : `NixLLM ${version || 'dev'}`;

  return (
    <>
      {mobileOpen && <div className="sidebar-backdrop" onClick={closeMobile} aria-hidden />}
      <aside className={`sidebar${mobileOpen ? ' sidebar--open' : ''}`}>
        {/* Header / Brand */}
        <div className="sidebar__header">
          <span className="sidebar__brand">
            <span className="sidebar__brand-mark" dangerouslySetInnerHTML={{ __html: BRAND_SVG }} />
            <span className="sidebar__brand-text">NixLLM</span>
          </span>
        </div>

        {/* Collapse floating toggle button */}
        <button
          type="button"
          className={`sidebar__collapse-btn${collapsed ? ' is-collapsed' : ''}`}
          onClick={onToggleCollapsed}
          title={collapsed ? 'Expand sidebar (⌘B)' : 'Collapse sidebar (⌘B)'}
          aria-label={collapsed ? 'Expand sidebar' : 'Collapse sidebar'}
          aria-expanded={!collapsed}
        >
          <ChevronIcon />
        </button>

        {/* Quick Filter Search Bar */}
        <div
          className="sidebar__search"
          onClick={handleSearchClick}
          role={collapsed ? 'button' : undefined}
          tabIndex={collapsed ? 0 : undefined}
          aria-label="Filter navigation"
          onMouseEnter={(e) => showTip('Filter Menu', 'Quick Search', e.currentTarget)}
          onMouseLeave={hideTip}
          onKeyDown={
            collapsed
              ? (e) => {
                  if (e.key === 'Enter' || e.key === ' ') {
                    e.preventDefault();
                    handleSearchClick();
                  }
                }
              : undefined
          }
        >
          <span className="sidebar__search-icon" aria-hidden>
            <SearchIcon />
          </span>
          <input
            ref={searchRef}
            className="sidebar__search-input"
            type="text"
            placeholder="Filter navigation..."
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onClick={(e) => e.stopPropagation()}
            aria-label="Filter navigation"
            spellCheck={false}
            autoComplete="off"
          />
          {query ? (
            <button
              type="button"
              className="sidebar__search-clear"
              onClick={(e) => {
                e.stopPropagation();
                setQuery('');
                searchRef.current?.focus();
              }}
              aria-label="Clear search"
            >
              <ClearIcon />
            </button>
          ) : (
            <kbd className="sidebar__search-kbd" aria-hidden>/</kbd>
          )}
        </div>

        {/* Navigation list */}
        <nav className="sidebar__nav" aria-label="Primary" ref={navRef}>
          {displayGroups.length === 0 ? (
            <div className="sidebar__empty">No menu matches for &ldquo;{query.trim()}&rdquo;</div>
          ) : (
            displayGroups.map((group) => {
              const groupIsCollapsed = !isSearching && !group.isFavorites && !!collapsedGroups[group.label];
              return (
                <div
                  className={`sidebar__section${groupIsCollapsed ? ' is-collapsed' : ''}${
                    group.isFavorites ? ' sidebar__section--favorites' : ''
                  }`}
                  key={group.label}
                >
                  <button
                    type="button"
                    className="sidebar__section-label"
                    onClick={() => !group.isFavorites && toggleGroup(group.label)}
                    disabled={isSearching || group.isFavorites}
                    title={group.isFavorites ? 'Pinned Favorites' : `Toggle ${group.label}`}
                  >
                    <span className="sidebar__section-title">
                      {group.isFavorites && <StarIcon fill="currentColor" className="sidebar__fav-star-header" />}
                      {group.label}
                    </span>
                    {!group.isFavorites && (
                      <svg
                        className="sidebar__section-chevron"
                        viewBox="0 0 12 12"
                        fill="none"
                        stroke="currentColor"
                        strokeWidth="1.5"
                        strokeLinecap="round"
                        strokeLinejoin="round"
                      >
                        <path d="M3 4.5L6 7.5L9 4.5" />
                      </svg>
                    )}
                  </button>

                  <div className="sidebar__section-items">
                    <div className="sidebar__section-items-inner">
                      {group.items.map((item) => {
                        const isFav = favorites.includes(item.to);
                        return (
                          <NavLink
                            key={item.to}
                            to={item.to}
                            end={item.end}
                            className={({ isActive }) => `sidebar__link${isActive ? ' active' : ''}`}
                            onClick={closeMobile}
                            title={item.label}
                            onMouseEnter={(e) => showTip(item.label, group.label, e.currentTarget)}
                            onMouseLeave={hideTip}
                          >
                            <span className="sidebar__link-icon">{ICON_MAP[item.icon]}</span>
                            <span className="sidebar__link-label">{item.label}</span>
                            <button
                              type="button"
                              className={`sidebar__fav-btn${isFav ? ' is-fav' : ''}`}
                              onClick={(e) => toggleFavorite(item.to, e)}
                              title={isFav ? 'Unpin from favorites' : 'Pin to favorites'}
                              aria-label={isFav ? `Unpin ${item.label}` : `Pin ${item.label}`}
                            >
                              <StarIcon fill={isFav ? 'currentColor' : 'none'} />
                            </button>
                          </NavLink>
                        );
                      })}
                    </div>
                  </div>
                </div>
              );
            })
          )}
        </nav>

        {/* Footer */}
        <div className="sidebar__footer">
          <span className={`sidebar__version${updateAvailable ? ' has-update' : ''}`} title={versionTitle}>
            {version || 'dev'}
            {coreVersion && coreVersion !== 'unknown' && (
              <span className="sidebar__version-core"> · core {coreVersion}</span>
            )}
          </span>
          <div className="sidebar__theme-toggle">
            <div className="seg">
              <button
                type="button"
                className={`seg__btn ${theme === 'light' ? 'seg__btn--active' : ''}`}
                onClick={() => setTheme('light')}
                title="Light Theme"
                onMouseEnter={(e) => showTip('Light Theme', 'Appearance', e.currentTarget)}
                onMouseLeave={hideTip}
              >
                <SunIcon />
              </button>
              <button
                type="button"
                className={`seg__btn ${theme === 'system' ? 'seg__btn--active' : ''}`}
                onClick={() => setTheme('system')}
                title="System Default"
                onMouseEnter={(e) => showTip('System Default', 'Appearance', e.currentTarget)}
                onMouseLeave={hideTip}
              >
                <MonitorIcon />
              </button>
              <button
                type="button"
                className={`seg__btn ${theme === 'dark' ? 'seg__btn--active' : ''}`}
                onClick={() => setTheme('dark')}
                title="Dark Theme"
                onMouseEnter={(e) => showTip('Dark Theme', 'Appearance', e.currentTarget)}
                onMouseLeave={hideTip}
              >
                <MoonIcon />
              </button>
            </div>
          </div>
          <button
            type="button"
            className="sidebar__signout"
            onClick={onLogout}
            title="Sign out"
            onMouseEnter={(e) => showTip('Sign Out', 'Account', e.currentTarget)}
            onMouseLeave={hideTip}
          >
            <span className="sidebar__link-icon">
              <LogoutIcon />
            </span>
            <span className="sidebar__link-label">Sign out</span>
          </button>
        </div>
      </aside>

      {/* Floating rich tooltip for rail (collapsed) mode */}
      {collapsed && railTip && (
        <div className="sidebar__rail-tip" style={{ top: railTip.top }}>
          <div className="sidebar__rail-tip-category">{railTip.category}</div>
          <div className="sidebar__rail-tip-label">{railTip.label}</div>
        </div>
      )}
    </>
  );
}

// Logical navigation groups
const NAV_GROUPS = [
  {
    label: 'Overview',
    items: [
      { to: '/', label: 'Dashboard', icon: 'home', end: true },
      { to: '/api-keys', label: 'API Keys', icon: 'key' },
      { to: '/internal-users', label: 'Internal Users', icon: 'users' },
      { to: '/litellm', label: 'Manage LiteLLM', icon: 'litellm' },
      { to: '/model-groups', label: 'Model Groups', icon: 'layers' },
      { to: '/auto-routers', label: 'Auto Routers', icon: 'route' },
      { to: '/models', label: 'Models Catalog', icon: 'cube' },
      { to: '/error-messages', label: 'Error Messages', icon: 'alert' },
      { to: '/api-tokens', label: 'API Management', icon: 'shield' },
      { to: '/upstream-providers', label: 'Upstream Providers', icon: 'server' },
      { to: '/upstream-providers/health', label: 'Upstream Health', icon: 'activity' },
      { to: '/proxy-pools', label: 'Proxy Pools', icon: 'lan' },
      { to: '/playground', label: 'Playground', icon: 'chat' },
      { to: '/developer', label: 'Developer', icon: 'code' },
    ],
  },
  {
    label: 'Analysis',
    items: [
      { to: '/usage', label: 'Usage Stats', icon: 'chart' },
      { to: '/provider-performance', label: 'Provider Performance', icon: 'gauge' },
      { to: '/recent-events', label: 'Recent Events', icon: 'list' },
      { to: '/errors', label: 'Errors', icon: 'bug' },
      { to: '/cooldown-providers', label: 'Cooldown Providers', icon: 'snow' },
      { to: '/session-affinity', label: 'Session Affinity', icon: 'link' },
      { to: '/upstream-sync-log', label: 'Sync Log', icon: 'sync' },
      { to: '/model-health', label: 'Model Health', icon: 'pulse' },
      { to: '/quota', label: 'Quota', icon: 'meter' },
      { to: '/analysis/auto-routers', label: 'Auto Router', icon: 'route' },
    ],
  },
  {
    label: 'System',
    items: [
      { to: '/settings', label: 'Settings', icon: 'gear' },
      { to: '/security', label: 'Login Security', icon: 'shield' },
      { to: '/branding', label: 'Branding', icon: 'tag' },
      { to: '/import-export', label: 'Import / Export', icon: 'transfer' },
      { to: '/backup', label: 'Backups', icon: 'archive' },
      { to: '/logs', label: 'Server Logs', icon: 'terminal' },
    ],
  },
];

// --- Icons ----------------------------------------------------------------

function ChevronIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.75" strokeLinecap="round" strokeLinejoin="round">
      <path d="M10 3L5 8l5 5" />
    </svg>
  );
}

function SearchIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="7" cy="7" r="4.5" />
      <path d="M10.5 10.5L14 14" />
    </svg>
  );
}

function ClearIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round">
      <path d="M4 4l8 8M12 4l-8 8" />
    </svg>
  );
}

function StarIcon({ fill = 'none', className = '' }) {
  return (
    <svg className={className} viewBox="0 0 16 16" fill={fill} stroke="currentColor" strokeWidth="1.3" strokeLinecap="round" strokeLinejoin="round">
      <polygon points="8,1.5 10.2,5.8 15,6.5 11.5,9.9 12.3,14.6 8,12.3 3.7,14.6 4.5,9.9 1,6.5 5.8,5.8" />
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

function SunIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <circle cx="8" cy="8" r="3.5" />
      <path d="M8 2V1M8 15v-1M2 8H1M15 8h-1M3.8 3.8L3 3M13 13l-.8-.8M3.8 12.2L3 13M13 3l-.8.8" />
    </svg>
  );
}

function MoonIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <path d="M13.5 10.5A5.5 5.5 0 0 1 5.5 2.5 6 6 0 1 0 14 11a5.5 5.5 0 0 1-.5-.5z" />
    </svg>
  );
}

function MonitorIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round">
      <rect x="2" y="3" width="12" height="7" rx="1.5" />
      <path d="M5.5 13h5M8 10v3" />
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
  server: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><rect x="2" y="2.5" width="12" height="4" rx="1" /><rect x="2" y="9.5" width="12" height="4" rx="1" /><circle cx="4.8" cy="4.5" r="0.7" fill="currentColor" stroke="none" /><circle cx="4.8" cy="11.5" r="0.7" fill="currentColor" stroke="none" /></svg>,
  lan: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><rect x="6" y="1.5" width="4" height="3.5" rx="0.8" /><rect x="1.5" y="11" width="4" height="3.5" rx="0.8" /><rect x="10.5" y="11" width="4" height="3.5" rx="0.8" /><path d="M8 5v3M8 8H3.5v3M8 8h4.5v3" /></svg>,
  sync: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M13 6A5 5 0 0 0 3.5 5" /><path d="M3 2.5V5h2.5" /><path d="M3 10A5 5 0 0 0 12.5 11" /><path d="M13 13.5V11h-2.5" /></svg>,
  gear: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5"><circle cx="8" cy="8" r="2" /><path d="M8 1v2M8 13v2M1 8h2M13 8h2M3 3l1.5 1.5M11.5 11.5L13 13M3 13l1.5-1.5M11.5 4.5L13 3" strokeLinecap="round" /></svg>,
  tag: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M2 8V3a1 1 0 0 1 1-1h5l6 6-6 6-6-6z" /><circle cx="5" cy="5" r="1" fill="currentColor" stroke="none" /></svg>,
  snow: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><path d="M8 1v14M1 8h14M3 3l10 10M13 3L3 13M8 3L6 1M8 3l2-2M8 13l-2 2M8 13l2 2M3 8L1 6M3 8l-2 2M13 8l2-2M13 8l2 2" /></svg>,
  link: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6.5 9.5a2.5 2.5 0 0 0 3.5 0l2-2a2.5 2.5 0 0 0-3.5-3.5l-1 1" /><path d="M9.5 6.5a2.5 2.5 0 0 0-3.5 0l-2 2a2.5 2.5 0 0 0 3.5 3.5l1-1" /></svg>,
  pulse: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M1 8h3l1.5-4 3 9 2-6 1 1h3.5" /></svg>,
  activity: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M6 1v4l-2 1v9M10 15v-4l2-1V1" /></svg>,
  transfer: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M2 5h11M9 2l3 3-3 3M14 11H3M7 8l-3 3 3 3" /></svg>,
  route: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><circle cx="3" cy="8" r="1.6" /><circle cx="13" cy="8" r="1.6" /><path d="M4.6 8h6.8" /><path d="M5 6c0-1.5 1-2.5 3-2.5S11 4.5 11 6v.5" /><path d="M5 10c0 1.5 1 2.5 3 2.5s3-1 3-2.5V9.5" /></svg>,
  litellm: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round"><ellipse cx="8" cy="3.5" rx="6" ry="2.2" /><path d="M2 3.5v9c0 1.2 2.7 2.2 6 2.2s6-1 6-2.2v-9" /><path d="M2 8c0 1.2 2.7 2.2 6 2.2s6-1 6-2.2" /></svg>,
  archive: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><rect x="2" y="3" width="12" height="3" rx="0.5" /><path d="M3 6v6.5a1 1 0 0 0 1 1h8a1 1 0 0 0 1-1V6" /><path d="M6.5 9h3" /></svg>,
  chat: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><path d="M2.5 4.5A1.5 1.5 0 0 1 4 3h8a1.5 1.5 0 0 1 1.5 1.5v5A1.5 1.5 0 0 1 12 11H6.5L4 13.5V11H4a1.5 1.5 0 0 1-1.5-1.5v-5z" /><path d="M5.5 6.5h5M5.5 8.5h3" /></svg>,
  terminal: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round"><rect x="1.5" y="2.5" width="13" height="11" rx="1.2" /><path d="M4 6l2.5 2.5L4 11M8.5 11h3.5" /></svg>,
  meter: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><path d="M2.5 13A5.5 5.5 0 0 1 13.5 13" /><path d="M8 13L10.5 7" strokeLinejoin="round" /><circle cx="8" cy="13" r="0.8" fill="currentColor" stroke="none" /></svg>,
  gauge: <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round"><path d="M2 12.5a6 6 0 1 1 12 0" /><path d="M8 12.5l3.5-4" strokeLinejoin="round" /><circle cx="8" cy="12.5" r="0.9" fill="currentColor" stroke="none" /><path d="M2.2 9.5h1.4M12.4 9.5h1.4M4.4 5.6l1 1M11.6 5.6l-1 1" /></svg>,
};
