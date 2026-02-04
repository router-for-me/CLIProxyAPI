import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Card } from '@/components/ui/Card';
import { Button } from '@/components/ui/Button';
import { IconGithub, IconBookOpen, IconExternalLink, IconCode } from '@/components/ui/icons';
import { useAuthStore, useConfigStore, useNotificationStore, useModelsStore, useThemeStore } from '@/stores';
import { apiKeysApi } from '@/services/api/apiKeys';
import { classifyModels } from '@/utils/models';
import { STORAGE_KEY_AUTH } from '@/utils/constants';
import iconGemini from '@/assets/icons/gemini.svg';
import iconClaude from '@/assets/icons/claude.svg';
import iconOpenaiLight from '@/assets/icons/openai-light.svg';
import iconOpenaiDark from '@/assets/icons/openai-dark.svg';
import iconQwen from '@/assets/icons/qwen.svg';
import iconKimiLight from '@/assets/icons/kimi-light.svg';
import iconKimiDark from '@/assets/icons/kimi-dark.svg';
import iconGlm from '@/assets/icons/glm.svg';
import iconGrok from '@/assets/icons/grok.svg';
import iconDeepseek from '@/assets/icons/deepseek.svg';
import iconMinimax from '@/assets/icons/minimax.svg';
import styles from './SystemPage.module.scss';

const MODEL_CATEGORY_ICONS: Record<string, string | { light: string; dark: string }> = {
  gpt: { light: iconOpenaiLight, dark: iconOpenaiDark },
  claude: iconClaude,
  gemini: iconGemini,
  qwen: iconQwen,
  kimi: { light: iconKimiLight, dark: iconKimiDark },
  glm: iconGlm,
  grok: iconGrok,
  deepseek: iconDeepseek,
  minimax: iconMinimax,
};

export function SystemPage() {
  const { t, i18n } = useTranslation();
  const { showNotification, showConfirmation } = useNotificationStore();
  const resolvedTheme = useThemeStore((state) => state.resolvedTheme);
  const auth = useAuthStore();
  const config = useConfigStore((state) => state.config);
  const fetchConfig = useConfigStore((state) => state.fetchConfig);

  const models = useModelsStore((state) => state.models);
  const modelsLoading = useModelsStore((state) => state.loading);
  const modelsError = useModelsStore((state) => state.error);
  const fetchModelsFromStore = useModelsStore((state) => state.fetchModels);

  const [modelStatus, setModelStatus] = useState<{ type: 'success' | 'warning' | 'error' | 'muted'; message: string }>();

  const apiKeysCache = useRef<string[]>([]);

  const otherLabel = useMemo(
    () => (i18n.language?.toLowerCase().startsWith('zh') ? '其他' : 'Other'),
    [i18n.language]
  );
  const groupedModels = useMemo(() => classifyModels(models, { otherLabel }), [models, otherLabel]);

  const getIconForCategory = (categoryId: string): string | null => {
    const iconEntry = MODEL_CATEGORY_ICONS[categoryId];
    if (!iconEntry) return null;
    if (typeof iconEntry === 'string') return iconEntry;
    return resolvedTheme === 'dark' ? iconEntry.dark : iconEntry.light;
  };

  const normalizeApiKeyList = (input: any): string[] => {
    if (!Array.isArray(input)) return [];
    const seen = new Set<string>();
    const keys: string[] = [];

    input.forEach((item) => {
      const value = typeof item === 'string' ? item : item?.['api-key'] ?? item?.apiKey ?? '';
      const trimmed = String(value || '').trim();
      if (!trimmed || seen.has(trimmed)) return;
      seen.add(trimmed);
      keys.push(trimmed);
    });

    return keys;
  };

  const resolveApiKeysForModels = useCallback(async () => {
    if (apiKeysCache.current.length) {
      return apiKeysCache.current;
    }

    const configKeys = normalizeApiKeyList(config?.apiKeys);
    if (configKeys.length) {
      apiKeysCache.current = configKeys;
      return configKeys;
    }

    try {
      const list = await apiKeysApi.list();
      const normalized = normalizeApiKeyList(list);
      if (normalized.length) {
        apiKeysCache.current = normalized;
      }
      return normalized;
    } catch (err) {
      console.warn('Auto loading API keys for models failed:', err);
      return [];
    }
  }, [config?.apiKeys]);

  const fetchModels = async ({ forceRefresh = false }: { forceRefresh?: boolean } = {}) => {
    if (auth.connectionStatus !== 'connected') {
      setModelStatus({
        type: 'warning',
        message: t('notification.connection_required')
      });
      return;
    }

    if (!auth.apiBase) {
      showNotification(t('notification.connection_required'), 'warning');
      return;
    }

    if (forceRefresh) {
      apiKeysCache.current = [];
    }

    setModelStatus({ type: 'muted', message: t('system_info.models_loading') });
    try {
      const apiKeys = await resolveApiKeysForModels();
      const primaryKey = apiKeys[0];
      const list = await fetchModelsFromStore(auth.apiBase, primaryKey, forceRefresh);
      const hasModels = list.length > 0;
      setModelStatus({
        type: hasModels ? 'success' : 'warning',
        message: hasModels ? t('system_info.models_count', { count: list.length }) : t('system_info.models_empty')
      });
    } catch (err: any) {
      const message = `${t('system_info.models_error')}: ${err?.message || ''}`;
      setModelStatus({ type: 'error', message });
    }
  };

  const handleClearLoginStorage = () => {
    showConfirmation({
      title: t('system_info.clear_login_title', { defaultValue: 'Clear Login Storage' }),
      message: t('system_info.clear_login_confirm'),
      variant: 'danger',
      confirmText: t('common.confirm'),
      onConfirm: () => {
        auth.logout();
        if (typeof localStorage === 'undefined') return;
        const keysToRemove = [STORAGE_KEY_AUTH, 'isLoggedIn', 'apiBase', 'apiUrl', 'managementKey'];
        keysToRemove.forEach((key) => localStorage.removeItem(key));
        showNotification(t('notification.login_storage_cleared'), 'success');
      },
    });
  };

  useEffect(() => {
    fetchConfig().catch(() => {
      // ignore
    });
  }, [fetchConfig]);

  useEffect(() => {
    fetchModels();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [auth.connectionStatus, auth.apiBase]);

  return (
    <div className={styles.container}>
      <h1 className={styles.pageTitle}>{t('system_info.title')}</h1>
      <div className={styles.content}>
      <Card
        title={t('system_info.connection_status_title')}
        extra={
          <Button variant="secondary" size="sm" onClick={() => fetchConfig(undefined, true)}>
            {t('common.refresh')}
          </Button>
        }
      >
        <div className="grid cols-2">
          <div className="stat-card">
            <div className="stat-label">{t('connection.server_address')}</div>
            <div className="stat-value">{auth.apiBase || '-'}</div>
          </div>
          <div className="stat-card">
            <div className="stat-label">{t('footer.api_version')}</div>
            <div className="stat-value">{auth.serverVersion || t('system_info.version_unknown')}</div>
          </div>
          <div className="stat-card">
            <div className="stat-label">{t('footer.build_date')}</div>
            <div className="stat-value">
              {auth.serverBuildDate ? new Date(auth.serverBuildDate).toLocaleString() : t('system_info.version_unknown')}
            </div>
          </div>
          <div className="stat-card">
            <div className="stat-label">{t('connection.status')}</div>
            <div className="stat-value">{t(`common.${auth.connectionStatus}_status` as any)}</div>
          </div>
        </div>
      </Card>

      <Card title={t('system_info.quick_links_title')}>
        <p className={styles.sectionDescription}>{t('system_info.quick_links_desc')}</p>
        <div className={styles.quickLinks}>
          <a
            href="https://github.com/router-for-me/CLIProxyAPI"
            target="_blank"
            rel="noopener noreferrer"
            className={styles.linkCard}
          >
            <div className={`${styles.linkIcon} ${styles.github}`}>
              <IconGithub size={22} />
            </div>
            <div className={styles.linkContent}>
              <div className={styles.linkTitle}>
                {t('system_info.link_main_repo')}
                <IconExternalLink size={14} />
              </div>
              <div className={styles.linkDesc}>{t('system_info.link_main_repo_desc')}</div>
            </div>
          </a>

          <a
            href="https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
            target="_blank"
            rel="noopener noreferrer"
            className={styles.linkCard}
          >
            <div className={`${styles.linkIcon} ${styles.github}`}>
              <IconCode size={22} />
            </div>
            <div className={styles.linkContent}>
              <div className={styles.linkTitle}>
                {t('system_info.link_webui_repo')}
                <IconExternalLink size={14} />
              </div>
              <div className={styles.linkDesc}>{t('system_info.link_webui_repo_desc')}</div>
            </div>
          </a>

          <a
            href="https://help.router-for.me/"
            target="_blank"
            rel="noopener noreferrer"
            className={styles.linkCard}
          >
            <div className={`${styles.linkIcon} ${styles.docs}`}>
              <IconBookOpen size={22} />
            </div>
            <div className={styles.linkContent}>
              <div className={styles.linkTitle}>
                {t('system_info.link_docs')}
                <IconExternalLink size={14} />
              </div>
              <div className={styles.linkDesc}>{t('system_info.link_docs_desc')}</div>
            </div>
          </a>
        </div>
      </Card>

      <Card
        title={t('system_info.models_title')}
        extra={
          <Button variant="secondary" size="sm" onClick={() => fetchModels({ forceRefresh: true })} loading={modelsLoading}>
            {t('common.refresh')}
          </Button>
        }
      >
        <p className={styles.sectionDescription}>{t('system_info.models_desc')}</p>
        {modelStatus && <div className={`status-badge ${modelStatus.type}`}>{modelStatus.message}</div>}
        {modelsError && <div className="error-box">{modelsError}</div>}
        {modelsLoading ? (
          <div className="hint">{t('common.loading')}</div>
        ) : models.length === 0 ? (
          <div className="hint">{t('system_info.models_empty')}</div>
        ) : (
          <div className="item-list">
            {groupedModels.map((group) => {
              const iconSrc = getIconForCategory(group.id);
              return (
                <div key={group.id} className="item-row">
                  <div className="item-meta">
                    <div className={styles.groupTitle}>
                      {iconSrc && <img src={iconSrc} alt="" className={styles.groupIcon} />}
                      <span className="item-title">{group.label}</span>
                    </div>
                    <div className="item-subtitle">{t('system_info.models_count', { count: group.items.length })}</div>
                  </div>
                  <div className={styles.modelTags}>
                    {group.items.map((model) => (
                      <span
                        key={`${model.name}-${model.alias ?? 'default'}`}
                        className={styles.modelTag}
                        title={model.description || ''}
                      >
                        <span className={styles.modelName}>{model.name}</span>
                        {model.alias && <span className={styles.modelAlias}>{model.alias}</span>}
                      </span>
                    ))}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </Card>

      <Card title={t('system_info.clear_login_title')}>
        <p className={styles.sectionDescription}>{t('system_info.clear_login_desc')}</p>
        <div className={styles.clearLoginActions}>
          <Button variant="danger" onClick={handleClearLoginStorage}>
            {t('system_info.clear_login_button')}
          </Button>
        </div>
      </Card>
      </div>
    </div>
  );
}
