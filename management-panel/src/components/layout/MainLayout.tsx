import {
  ReactNode,
  SVGProps,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
} from 'react';
import { NavLink, useLocation } from 'react-router-dom';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Modal } from '@/components/ui/Modal';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { PageTransition } from '@/components/common/PageTransition';
import { MainRoutes } from '@/router/MainRoutes';
import {
  IconBot,
  IconChartLine,
  IconFileText,
  IconInfo,
  IconKey,
  IconLayoutDashboard,
  IconScrollText,
  IconSettings,
  IconShield,
  IconSlidersHorizontal,
  IconTimer,
} from '@/components/ui/icons';
import { INLINE_LOGO_JPEG } from '@/assets/logoInline';
import {
  useAuthStore,
  useConfigStore,
  useLanguageStore,
  useNotificationStore,
  useThemeStore,
} from '@/stores';
import { configApi, versionApi } from '@/services/api';
import { triggerHeaderRefresh } from '@/hooks/useHeaderRefresh';

const sidebarIcons: Record<string, ReactNode> = {
  dashboard: <IconLayoutDashboard size={18} />,
  settings: <IconSlidersHorizontal size={18} />,
  apiKeys: <IconKey size={18} />,
  aiProviders: <IconBot size={18} />,
  authFiles: <IconFileText size={18} />,
  oauth: <IconShield size={18} />,
  quota: <IconTimer size={18} />,
  usage: <IconChartLine size={18} />,
  config: <IconSettings size={18} />,
  logs: <IconScrollText size={18} />,
  system: <IconInfo size={18} />,
};

// Header action icons - smaller size for header buttons
const headerIconProps: SVGProps<SVGSVGElement> = {
  width: 16,
  height: 16,
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  strokeWidth: 2,
  strokeLinecap: 'round',
  strokeLinejoin: 'round',
  'aria-hidden': 'true',
  focusable: 'false',
};

const headerIcons = {
  refresh: (
    <svg {...headerIconProps}>
      <path d="M21 12a9 9 0 1 1-9-9c2.52 0 4.93 1 6.74 2.74L21 8" />
      <path d="M21 3v5h-5" />
    </svg>
  ),
  update: (
    <svg {...headerIconProps}>
      <path d="M12 19V5" />
      <path d="m5 12 7-7 7 7" />
    </svg>
  ),
  menu: (
    <svg {...headerIconProps}>
      <path d="M4 7h16" />
      <path d="M4 12h16" />
      <path d="M4 17h16" />
    </svg>
  ),
  chevronLeft: (
    <svg {...headerIconProps}>
      <path d="m14 18-6-6 6-6" />
    </svg>
  ),
  chevronRight: (
    <svg {...headerIconProps}>
      <path d="m10 6 6 6-6 6" />
    </svg>
  ),
  language: (
    <svg {...headerIconProps}>
      <circle cx="12" cy="12" r="10" />
      <path d="M2 12h20" />
      <path d="M12 2a15.3 15.3 0 0 1 4 10 15.3 15.3 0 0 1-4 10 15.3 15.3 0 0 1-4-10 15.3 15.3 0 0 1 4-10z" />
    </svg>
  ),
  sun: (
    <svg {...headerIconProps}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2" />
      <path d="M12 20v2" />
      <path d="m4.93 4.93 1.41 1.41" />
      <path d="m17.66 17.66 1.41 1.41" />
      <path d="M2 12h2" />
      <path d="M20 12h2" />
      <path d="m6.34 17.66-1.41 1.41" />
      <path d="m19.07 4.93-1.41 1.41" />
    </svg>
  ),
  moon: (
    <svg {...headerIconProps}>
      <path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9z" />
    </svg>
  ),
  autoTheme: (
    <svg {...headerIconProps}>
      <defs>
        <clipPath id="mainLayoutAutoThemeSunLeftHalf">
          <rect x="0" y="0" width="12" height="24" />
        </clipPath>
      </defs>
      <circle cx="12" cy="12" r="4" />
      <circle cx="12" cy="12" r="4" clipPath="url(#mainLayoutAutoThemeSunLeftHalf)" fill="currentColor" />
      <path d="M12 2v2" />
      <path d="M12 20v2" />
      <path d="M4.93 4.93l1.41 1.41" />
      <path d="M17.66 17.66l1.41 1.41" />
      <path d="M2 12h2" />
      <path d="M20 12h2" />
      <path d="M6.34 17.66l-1.41 1.41" />
      <path d="M19.07 4.93l-1.41 1.41" />
    </svg>
  ),
  logout: (
    <svg {...headerIconProps}>
      <path d="M9 21H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h4" />
      <path d="m16 17 5-5-5-5" />
      <path d="M21 12H9" />
    </svg>
  ),
};

const parseVersionSegments = (version?: string | null) => {
  if (!version) return null;
  const cleaned = version.trim().replace(/^v/i, '');
  if (!cleaned) return null;
  const parts = cleaned
    .split(/[^0-9]+/)
    .filter(Boolean)
    .map((segment) => Number.parseInt(segment, 10))
    .filter(Number.isFinite);
  return parts.length ? parts : null;
};

const compareVersions = (latest?: string | null, current?: string | null) => {
  const latestParts = parseVersionSegments(latest);
  const currentParts = parseVersionSegments(current);
  if (!latestParts || !currentParts) return null;
  const length = Math.max(latestParts.length, currentParts.length);
  for (let i = 0; i < length; i++) {
    const l = latestParts[i] || 0;
    const c = currentParts[i] || 0;
    if (l > c) return 1;
    if (l < c) return -1;
  }
  return 0;
};

export function MainLayout() {
  const { t, i18n } = useTranslation();
  const { showNotification } = useNotificationStore();
  const location = useLocation();

  const apiBase = useAuthStore((state) => state.apiBase);
  const serverVersion = useAuthStore((state) => state.serverVersion);
  const serverBuildDate = useAuthStore((state) => state.serverBuildDate);
  const connectionStatus = useAuthStore((state) => state.connectionStatus);
  const logout = useAuthStore((state) => state.logout);

  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);
  const clearCache = useConfigStore((state) => state.clearCache);
  const updateConfigValue = useConfigStore((state) => state.updateConfigValue);

  const theme = useThemeStore((state) => state.theme);
  const cycleTheme = useThemeStore((state) => state.cycleTheme);
  const toggleLanguage = useLanguageStore((state) => state.toggleLanguage);

  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const [checkingVersion, setCheckingVersion] = useState(false);
  const [brandExpanded, setBrandExpanded] = useState(true);
  const [requestLogModalOpen, setRequestLogModalOpen] = useState(false);
  const [requestLogDraft, setRequestLogDraft] = useState(false);
  const [requestLogTouched, setRequestLogTouched] = useState(false);
  const [requestLogSaving, setRequestLogSaving] = useState(false);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const brandCollapseTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const headerRef = useRef<HTMLElement | null>(null);
  const versionTapCount = useRef(0);
  const versionTapTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const fullBrandName = 'CLI Proxy API Management Center';
  const abbrBrandName = t('title.abbr');
  const requestLogEnabled = config?.requestLog ?? false;
  const requestLogDirty = requestLogDraft !== requestLogEnabled;
  const canEditRequestLog = connectionStatus === 'connected' && Boolean(config);
  const isLogsPage = location.pathname.startsWith('/logs');

  // 将顶栏高度写入 CSS 变量，确保侧栏/内容区计算一致，防止滚动时抖动
  useLayoutEffect(() => {
    const updateHeaderHeight = () => {
      const height = headerRef.current?.offsetHeight;
      if (height) {
        document.documentElement.style.setProperty('--header-height', `${height}px`);
      }
    };

    updateHeaderHeight();

    const resizeObserver =
      typeof ResizeObserver !== 'undefined' && headerRef.current
        ? new ResizeObserver(updateHeaderHeight)
        : null;
    if (resizeObserver && headerRef.current) {
      resizeObserver.observe(headerRef.current);
    }

    window.addEventListener('resize', updateHeaderHeight);

    return () => {
      if (resizeObserver) {
        resizeObserver.disconnect();
      }
      window.removeEventListener('resize', updateHeaderHeight);
    };
  }, []);

  // 5秒后自动收起品牌名称
  useEffect(() => {
    brandCollapseTimer.current = setTimeout(() => {
      setBrandExpanded(false);
    }, 5000);

    return () => {
      if (brandCollapseTimer.current) {
        clearTimeout(brandCollapseTimer.current);
      }
    };
  }, []);

  useEffect(() => {
    if (requestLogModalOpen && !requestLogTouched) {
      setRequestLogDraft(requestLogEnabled);
    }
  }, [requestLogModalOpen, requestLogTouched, requestLogEnabled]);

  useEffect(() => {
    return () => {
      if (versionTapTimer.current) {
        clearTimeout(versionTapTimer.current);
      }
    };
  }, []);

  const handleBrandClick = useCallback(() => {
    if (!brandExpanded) {
      setBrandExpanded(true);
      // 点击展开后，5秒后再次收起
      if (brandCollapseTimer.current) {
        clearTimeout(brandCollapseTimer.current);
      }
      brandCollapseTimer.current = setTimeout(() => {
        setBrandExpanded(false);
      }, 5000);
    }
  }, [brandExpanded]);

  const openRequestLogModal = useCallback(() => {
    setRequestLogTouched(false);
    setRequestLogDraft(requestLogEnabled);
    setRequestLogModalOpen(true);
  }, [requestLogEnabled]);

  const handleRequestLogClose = useCallback(() => {
    setRequestLogModalOpen(false);
    setRequestLogTouched(false);
  }, []);

  const handleVersionTap = useCallback(() => {
    versionTapCount.current += 1;
    if (versionTapTimer.current) {
      clearTimeout(versionTapTimer.current);
    }
    versionTapTimer.current = setTimeout(() => {
      versionTapCount.current = 0;
    }, 1500);

    if (versionTapCount.current >= 7) {
      versionTapCount.current = 0;
      if (versionTapTimer.current) {
        clearTimeout(versionTapTimer.current);
        versionTapTimer.current = null;
      }
      openRequestLogModal();
    }
  }, [openRequestLogModal]);

  const handleRequestLogSave = async () => {
    if (!canEditRequestLog) return;
    if (!requestLogDirty) {
      setRequestLogModalOpen(false);
      return;
    }

    const previous = requestLogEnabled;
    setRequestLogSaving(true);
    updateConfigValue('request-log', requestLogDraft);

    try {
      await configApi.updateRequestLog(requestLogDraft);
      clearCache('request-log');
      showNotification(t('notification.request_log_updated'), 'success');
      setRequestLogModalOpen(false);
    } catch (error: any) {
      updateConfigValue('request-log', previous);
      showNotification(`${t('notification.update_failed')}: ${error?.message || ''}`, 'error');
    } finally {
      setRequestLogSaving(false);
    }
  };

  useEffect(() => {
    fetchConfig().catch(() => {
      // ignore initial failure; login flow会提示
    });
  }, [fetchConfig]);


  const statusClass =
    connectionStatus === 'connected'
      ? 'success'
      : connectionStatus === 'connecting'
        ? 'warning'
        : connectionStatus === 'error'
          ? 'error'
          : 'muted';

  const navItems = [
    { path: '/', label: t('nav.dashboard'), icon: sidebarIcons.dashboard },
    { path: '/settings', label: t('nav.basic_settings'), icon: sidebarIcons.settings },
    { path: '/api-keys', label: t('nav.api_keys'), icon: sidebarIcons.apiKeys },
    { path: '/ai-providers', label: t('nav.ai_providers'), icon: sidebarIcons.aiProviders },
    { path: '/auth-files', label: t('nav.auth_files'), icon: sidebarIcons.authFiles },
    { path: '/oauth', label: t('nav.oauth', { defaultValue: 'OAuth' }), icon: sidebarIcons.oauth },
    { path: '/quota', label: t('nav.quota_management'), icon: sidebarIcons.quota },
    { path: '/usage', label: t('nav.usage_stats'), icon: sidebarIcons.usage },
    { path: '/config', label: t('nav.config_management'), icon: sidebarIcons.config },
    ...(config?.loggingToFile
      ? [{ path: '/logs', label: t('nav.logs'), icon: sidebarIcons.logs }]
      : []),
    { path: '/system', label: t('nav.system_info'), icon: sidebarIcons.system },
  ];
  const navOrder = navItems.map((item) => item.path);
  const getRouteOrder = (pathname: string) => {
    const trimmedPath =
      pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname;
    const normalizedPath = trimmedPath === '/dashboard' ? '/' : trimmedPath;

    const aiProvidersIndex = navOrder.indexOf('/ai-providers');
    if (aiProvidersIndex !== -1) {
      if (normalizedPath === '/ai-providers') return aiProvidersIndex;
      if (normalizedPath.startsWith('/ai-providers/')) {
        if (normalizedPath.startsWith('/ai-providers/gemini')) return aiProvidersIndex + 0.1;
        if (normalizedPath.startsWith('/ai-providers/codex')) return aiProvidersIndex + 0.2;
        if (normalizedPath.startsWith('/ai-providers/claude')) return aiProvidersIndex + 0.3;
        if (normalizedPath.startsWith('/ai-providers/vertex')) return aiProvidersIndex + 0.4;
        if (normalizedPath.startsWith('/ai-providers/ampcode')) return aiProvidersIndex + 0.5;
        if (normalizedPath.startsWith('/ai-providers/openai')) return aiProvidersIndex + 0.6;
        return aiProvidersIndex + 0.05;
      }
    }

    const authFilesIndex = navOrder.indexOf('/auth-files');
    if (authFilesIndex !== -1) {
      if (normalizedPath === '/auth-files') return authFilesIndex;
      if (normalizedPath.startsWith('/auth-files/')) {
        if (normalizedPath.startsWith('/auth-files/oauth-excluded')) return authFilesIndex + 0.1;
        if (normalizedPath.startsWith('/auth-files/oauth-model-alias')) return authFilesIndex + 0.2;
        return authFilesIndex + 0.05;
      }
    }

    const exactIndex = navOrder.indexOf(normalizedPath);
    if (exactIndex !== -1) return exactIndex;
    const nestedIndex = navOrder.findIndex(
      (path) => path !== '/' && normalizedPath.startsWith(`${path}/`)
    );
    return nestedIndex === -1 ? null : nestedIndex;
  };

  const getTransitionVariant = useCallback((fromPathname: string, toPathname: string) => {
    const normalize = (pathname: string) => {
      const trimmed =
        pathname.length > 1 && pathname.endsWith('/') ? pathname.slice(0, -1) : pathname;
      return trimmed === '/dashboard' ? '/' : trimmed;
    };

    const from = normalize(fromPathname);
    const to = normalize(toPathname);
    const isAuthFiles = (pathname: string) =>
      pathname === '/auth-files' || pathname.startsWith('/auth-files/');
    const isAiProviders = (pathname: string) =>
      pathname === '/ai-providers' || pathname.startsWith('/ai-providers/');
    if (isAuthFiles(from) && isAuthFiles(to)) return 'ios';
    if (isAiProviders(from) && isAiProviders(to)) return 'ios';
    return 'vertical';
  }, []);

  const handleRefreshAll = async () => {
    clearCache();
    const results = await Promise.allSettled([
      fetchConfig(undefined, true),
      triggerHeaderRefresh()
    ]);
    const rejected = results.find((result) => result.status === 'rejected');
    if (rejected && rejected.status === 'rejected') {
      const reason = rejected.reason;
      const message =
        typeof reason === 'string' ? reason : reason instanceof Error ? reason.message : '';
      showNotification(
        `${t('notification.refresh_failed')}${message ? `: ${message}` : ''}`,
        'error'
      );
      return;
    }
    showNotification(t('notification.data_refreshed'), 'success');
  };

  const handleVersionCheck = async () => {
    setCheckingVersion(true);
    try {
      const data = await versionApi.checkLatest();
      const latest = data?.['latest-version'] ?? data?.latest_version ?? data?.latest ?? '';
      const comparison = compareVersions(latest, serverVersion);

      if (!latest) {
        showNotification(t('system_info.version_check_error'), 'error');
        return;
      }

      if (comparison === null) {
        showNotification(t('system_info.version_current_missing'), 'warning');
        return;
      }

      if (comparison > 0) {
        showNotification(t('system_info.version_update_available', { version: latest }), 'warning');
      } else {
        showNotification(t('system_info.version_is_latest'), 'success');
      }
    } catch (error: any) {
      showNotification(`${t('system_info.version_check_error')}: ${error?.message || ''}`, 'error');
    } finally {
      setCheckingVersion(false);
    }
  };

  return (
    <div className="app-shell">
      <header className="main-header" ref={headerRef}>
        <div className="left">
          <button
            className="sidebar-toggle-header"
            onClick={() => setSidebarCollapsed((prev) => !prev)}
            title={
              sidebarCollapsed
                ? t('sidebar.expand', { defaultValue: '展开' })
                : t('sidebar.collapse', { defaultValue: '收起' })
            }
          >
            {sidebarCollapsed ? headerIcons.chevronRight : headerIcons.chevronLeft}
          </button>
          <img src={INLINE_LOGO_JPEG} alt="CPAMC logo" className="brand-logo" />
          <div
            className={`brand-header ${brandExpanded ? 'expanded' : 'collapsed'}`}
            onClick={handleBrandClick}
            title={brandExpanded ? undefined : fullBrandName}
          >
            <span className="brand-full">{fullBrandName}</span>
            <span className="brand-abbr">{abbrBrandName}</span>
          </div>
        </div>

        <div className="right">
          <div className="connection">
            <span className={`status-badge ${statusClass}`}>
              {t(
                connectionStatus === 'connected'
                  ? 'common.connected_status'
                  : connectionStatus === 'connecting'
                    ? 'common.connecting_status'
                    : 'common.disconnected_status'
              )}
            </span>
            <span className="base">{apiBase || '-'}</span>
          </div>

          <div className="header-actions">
            <Button
              className="mobile-menu-btn"
              variant="ghost"
              size="sm"
              onClick={() => setSidebarOpen((prev) => !prev)}
            >
              {headerIcons.menu}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={handleRefreshAll}
              title={t('header.refresh_all')}
            >
              {headerIcons.refresh}
            </Button>
            <Button
              variant="ghost"
              size="sm"
              onClick={handleVersionCheck}
              loading={checkingVersion}
              title={t('system_info.version_check_button')}
            >
              {headerIcons.update}
            </Button>
            <Button variant="ghost" size="sm" onClick={toggleLanguage} title={t('language.switch')}>
              {headerIcons.language}
            </Button>
            <Button variant="ghost" size="sm" onClick={cycleTheme} title={t('theme.switch')}>
              {theme === 'auto'
                ? headerIcons.autoTheme
                : theme === 'dark'
                  ? headerIcons.moon
                  : headerIcons.sun}
            </Button>
            <Button variant="ghost" size="sm" onClick={logout} title={t('header.logout')}>
              {headerIcons.logout}
            </Button>
          </div>
        </div>
      </header>

      <div className="main-body">
        <aside
          className={`sidebar ${sidebarOpen ? 'open' : ''} ${sidebarCollapsed ? 'collapsed' : ''}`}
        >
          <div className="nav-section">
            {navItems.map((item) => (
              <NavLink
                key={item.path}
                to={item.path}
                className={({ isActive }) => `nav-item ${isActive ? 'active' : ''}`}
                onClick={() => setSidebarOpen(false)}
                title={sidebarCollapsed ? item.label : undefined}
              >
                <span className="nav-icon">{item.icon}</span>
                {!sidebarCollapsed && <span className="nav-label">{item.label}</span>}
              </NavLink>
            ))}
          </div>
        </aside>

        <div className={`content${isLogsPage ? ' content-logs' : ''}`} ref={contentRef}>
          <main className={`main-content${isLogsPage ? ' main-content-logs' : ''}`}>
            <PageTransition
              render={(location) => <MainRoutes location={location} />}
              getRouteOrder={getRouteOrder}
              getTransitionVariant={getTransitionVariant}
              scrollContainerRef={contentRef}
            />
          </main>

          <footer className="footer">
            <span>
              {t('footer.api_version')}: {serverVersion || t('system_info.version_unknown')}
            </span>
            <span className="footer-version" onClick={handleVersionTap}>
              {t('footer.version')}: {__APP_VERSION__ || t('system_info.version_unknown')}
            </span>
            <span>
              {t('footer.build_date')}:{' '}
              {serverBuildDate
                ? new Date(serverBuildDate).toLocaleString(i18n.language)
                : t('system_info.version_unknown')}
            </span>
          </footer>
        </div>
      </div>

      <Modal
        open={requestLogModalOpen}
        onClose={handleRequestLogClose}
        title={t('basic_settings.request_log_title')}
        footer={
          <>
            <Button variant="secondary" onClick={handleRequestLogClose} disabled={requestLogSaving}>
              {t('common.cancel')}
            </Button>
            <Button
              onClick={handleRequestLogSave}
              loading={requestLogSaving}
              disabled={!canEditRequestLog || !requestLogDirty}
            >
              {t('common.save')}
            </Button>
          </>
        }
      >
        <div className="request-log-modal">
          <div className="status-badge warning">{t('basic_settings.request_log_warning')}</div>
          <ToggleSwitch
            label={t('basic_settings.request_log_enable')}
            labelPosition="left"
            checked={requestLogDraft}
            disabled={!canEditRequestLog || requestLogSaving}
            onChange={(value) => {
              setRequestLogDraft(value);
              setRequestLogTouched(true);
            }}
          />
        </div>
      </Modal>
    </div>
  );
}
