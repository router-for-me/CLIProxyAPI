import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { EmptyState } from '@/components/ui/EmptyState';
import { IconAlertTriangle, IconShield } from '@/components/ui/icons';
import type { PluginStoreEntry, PluginStoreResponse } from '@/types';
import type { StoreLoadError } from '../hooks/usePluginStore';
import type { StoreLayout } from '../pluginsView';
import { getStoreEntryKey } from '../pluginStoreEntries';
import { PluginListReveal } from './PluginListReveal';
import { StorePluginRow, StorePluginRowSkeleton, type StoreRowAction } from './StorePluginRow';
import styles from './PluginList.module.scss';

const SKELETON_ROWS = 4;

interface PluginStoreViewProps {
  data: PluginStoreResponse | null;
  error: StoreLoadError | null;
  loading: boolean;
  entries: PluginStoreEntry[];
  layout: StoreLayout;
  query: string;
  connected: boolean;
  actionBusy: boolean;
  installingKey: string;
  getLogo: (entry: PluginStoreEntry) => string;
  getAction: (entry: PluginStoreEntry) => StoreRowAction;
  onInstall: (entry: PluginStoreEntry) => void;
  onRetry: () => void;
  onClearSearch: () => void;
}

export function PluginStoreView({
  data,
  error,
  loading,
  entries,
  layout,
  query,
  connected,
  actionBusy,
  installingKey,
  getLogo,
  getAction,
  onInstall,
  onRetry,
  onClearSearch,
}: PluginStoreViewProps) {
  const { t } = useTranslation();
  const containerClass = layout === 'cards' ? styles.grid : styles.list;

  const renderList = (response: PluginStoreResponse) => {
    if (response.plugins.length === 0) {
      return (
        <EmptyState
          title={t('plugin_store.no_plugins')}
          description={t('plugin_store.no_plugins_desc')}
          action={
            <Button
              variant="secondary"
              size="sm"
              onClick={onRetry}
              disabled={!connected || loading}
            >
              {t('plugin_store.refresh')}
            </Button>
          }
        />
      );
    }

    if (entries.length === 0) {
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
      <div className={containerClass}>
        {entries.map((entry) => {
          const entryKey = getStoreEntryKey(entry);
          const installing = installingKey === entryKey;
          return (
            <StorePluginRow
              key={entryKey}
              entry={entry}
              layout={layout}
              logo={getLogo(entry)}
              action={getAction(entry)}
              installing={installing}
              disabled={!connected || actionBusy || (Boolean(installingKey) && !installing)}
              onInstall={() => onInstall(entry)}
            />
          );
        })}
      </div>
    );
  };

  return (
    <>
      <div className={styles.note} role="note">
        <IconShield size={16} />
        <div>
          <strong>{t('plugin_store.security_banner_title')}</strong>
          <p>{t('plugin_store.security_banner_text')}</p>
        </div>
      </div>

      {error ? (
        <div className={styles.errorBox} role="alert">
          <span>{error.message}</span>
          {error.kind !== 'unsupported' ? (
            <Button variant="secondary" size="sm" onClick={onRetry} disabled={loading}>
              {t('plugin_store.retry')}
            </Button>
          ) : null}
        </div>
      ) : null}

      {data?.sourceErrors.length ? (
        <div className={styles.sourceErrors}>
          <p className={styles.sourceErrorsTitle}>
            <IconAlertTriangle size={14} />
            <span>{t('plugin_store.source_errors_title')}</span>
          </p>
          <ul>
            {data.sourceErrors.map((sourceError, index) => (
              <li key={`${sourceError.sourceId}-${sourceError.sourceUrl}-${index}`}>
                <span>
                  {sourceError.sourceName || sourceError.sourceUrl || sourceError.sourceId}
                </span>
                {sourceError.message ? <small>{sourceError.message}</small> : null}
              </li>
            ))}
          </ul>
        </div>
      ) : null}

      {data ? (
        <PluginListReveal>{renderList(data)}</PluginListReveal>
      ) : loading ? (
        <div className={containerClass} aria-busy="true">
          {Array.from({ length: SKELETON_ROWS }, (_, index) => (
            <StorePluginRowSkeleton key={index} layout={layout} />
          ))}
        </div>
      ) : null}
    </>
  );
}
