import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import type { PluginListEntry, PluginListResponse } from '@/types';
import type { PluginRowStatus } from '../pluginStatus';
import { PluginRow, PluginRowSkeleton, type PluginRowUpdate } from './PluginRow';
import { PluginListReveal } from './PluginListReveal';
import styles from './PluginList.module.scss';

const SKELETON_ROWS = 3;

interface InstalledPluginsViewProps {
  data: PluginListResponse | null;
  loading: boolean;
  error: string;
  plugins: PluginListEntry[];
  query: string;
  getLogo: (plugin: PluginListEntry) => string;
  getStatus: (plugin: PluginListEntry, pluginsEnabled: boolean) => PluginRowStatus;
  getChecked: (plugin: PluginListEntry) => boolean;
  getUpdate: (plugin: PluginListEntry) => PluginRowUpdate | null;
  isRowDisabled: (plugin: PluginListEntry) => boolean;
  onToggle: (plugin: PluginListEntry, enabled: boolean) => void;
  onConfigure: (plugin: PluginListEntry) => void;
  onDelete: (plugin: PluginListEntry) => void;
  onClearSearch: () => void;
  onBrowseStore: () => void;
}

export function InstalledPluginsView({
  data,
  loading,
  error,
  plugins,
  query,
  getLogo,
  getStatus,
  getChecked,
  getUpdate,
  isRowDisabled,
  onToggle,
  onConfigure,
  onDelete,
  onClearSearch,
  onBrowseStore,
}: InstalledPluginsViewProps) {
  const { t } = useTranslation();

  const renderList = (response: PluginListResponse) => {
    if (response.plugins.length === 0) {
      return (
        <EmptyState
          title={t('plugin_management.no_plugins')}
          description={t('plugin_management.no_plugins_desc')}
          action={
            <Button variant="secondary" size="sm" onClick={onBrowseStore}>
              {t('plugin_management.browse_store')}
            </Button>
          }
        />
      );
    }

    if (plugins.length === 0) {
      return (
        <EmptyState
          title={t('plugin_management.no_matches')}
          description={t('plugin_management.no_matches_desc', { query })}
          action={
            <Button variant="secondary" size="sm" onClick={onClearSearch}>
              {t('plugin_management.clear_search')}
            </Button>
          }
        />
      );
    }

    return (
      <div className={styles.list}>
        {plugins.map((plugin) => (
          <PluginRow
            key={plugin.id}
            plugin={plugin}
            logo={getLogo(plugin)}
            status={getStatus(plugin, response.pluginsEnabled)}
            checked={getChecked(plugin)}
            disabled={isRowDisabled(plugin)}
            update={getUpdate(plugin)}
            onToggle={(enabled) => onToggle(plugin, enabled)}
            onConfigure={() => onConfigure(plugin)}
            onDelete={() => onDelete(plugin)}
          />
        ))}
      </div>
    );
  };

  return (
    <>
      {error ? (
        <div className={styles.errorBox} role="alert">
          <span>{error}</span>
        </div>
      ) : null}

      {data ? (
        <PluginListReveal>{renderList(data)}</PluginListReveal>
      ) : loading ? (
        <div className={styles.list} aria-busy="true">
          {Array.from({ length: SKELETON_ROWS }, (_, index) => (
            <PluginRowSkeleton key={index} />
          ))}
        </div>
      ) : null}
    </>
  );
}
