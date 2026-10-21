import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconLoader2, IconRefreshCw, IconSearch } from '@/components/ui/icons';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import type { ModelInfo } from '@/utils/models';
import styles from './sharedForm.module.scss';

interface ModelDiscoveryPanelProps {
  loading: boolean;
  error: string | null;
  models: ModelInfo[];
  hasFetched: boolean;
  existingNames: Set<string>;
  mutating?: boolean;
  onApply: (picked: ModelInfo[]) => void;
  onReload: () => void;
  onClose: () => void;
}

export function ModelDiscoveryPanel({
  loading,
  error,
  models,
  hasFetched,
  existingNames,
  mutating,
  onApply,
  onReload,
  onClose,
}: ModelDiscoveryPanelProps) {
  const { t } = useTranslation();
  const [search, setSearch] = useState('');
  const [selected, setSelected] = useState<Set<string>>(new Set());

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return models;
    return models.filter((m) => `${m.name} ${m.alias ?? ''}`.toLowerCase().includes(q));
  }, [models, search]);

  const selectable = useMemo(
    () => filtered.filter((m) => !existingNames.has(m.name)),
    [filtered, existingNames]
  );

  const allSelectableChecked =
    selectable.length > 0 && selectable.every((m) => selected.has(m.name));

  const toggle = (name: string) => {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(name)) next.delete(name);
      else next.add(name);
      return next;
    });
  };

  const toggleAll = () => {
    if (allSelectableChecked) {
      setSelected(new Set());
    } else {
      setSelected(new Set(selectable.map((m) => m.name)));
    }
  };

  const handleApply = () => {
    const picked = models.filter((m) => selected.has(m.name) && !existingNames.has(m.name));
    if (!picked.length) return;
    onApply(picked);
    setSelected(new Set());
  };

  const renderModelLabel = (model: ModelInfo) => (
    <span className={styles.discoveryNameGroup}>
      <span className={styles.discoveryName}>{model.name}</span>
      {model.alias ? <span className={styles.discoveryAlias}>{model.alias}</span> : null}
    </span>
  );

  return (
    <div className={styles.discoveryPanel}>
      <div className={styles.discoveryToolbar}>
        <div className={styles.discoverySearchWrap}>
          <span className={styles.discoverySearchIcon} aria-hidden="true">
            <IconSearch size={14} />
          </span>
          <input
            type="search"
            className={styles.discoverySearch}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('providersPage.discovery.searchPlaceholder')}
          />
        </div>
        <button
          type="button"
          className={styles.connectivityBtn}
          onClick={onReload}
          disabled={loading}
          aria-label={t('providersPage.discovery.reload')}
        >
          {loading ? (
            <span className={`${styles.statusIcon} ${styles.statusIconLoading}`}>
              <IconLoader2 size={14} />
            </span>
          ) : (
            <IconRefreshCw size={14} />
          )}
          <span>{t('providersPage.discovery.reload')}</span>
        </button>
      </div>

      {loading && !models.length ? (
        <div className={styles.discoveryEmpty}>{t('providersPage.discovery.loading')}</div>
      ) : error ? (
        <div className={styles.connectivityError}>{error}</div>
      ) : hasFetched && !models.length ? (
        <div className={styles.discoveryEmpty}>{t('providersPage.discovery.empty')}</div>
      ) : models.length ? (
        <>
          <div className={styles.discoveryBatchRow}>
            <SelectionCheckbox
              checked={allSelectableChecked}
              onChange={toggleAll}
              disabled={selectable.length === 0}
              label={
                <span className={styles.discoveryBatchLabel}>
                  {allSelectableChecked
                    ? t('providersPage.discovery.clearAll')
                    : t('providersPage.discovery.selectAll')}
                </span>
              }
            />
            <span className={styles.discoveryCount}>
              {t('providersPage.discovery.selectedCount', {
                selected: selected.size,
                total: selectable.length,
              })}
            </span>
          </div>
          <ul className={styles.discoveryList}>
            {filtered.map((m) => {
              const existing = existingNames.has(m.name);
              return (
                <li
                  key={m.name}
                  className={
                    existing
                      ? `${styles.discoveryItem} ${styles.discoveryItemExisting}`
                      : styles.discoveryItem
                  }
                >
                  {existing ? (
                    <>
                      {renderModelLabel(m)}
                      <span className={styles.discoveryAddedTag}>
                        {t('providersPage.discovery.alreadyAdded')}
                      </span>
                    </>
                  ) : (
                    <SelectionCheckbox
                      checked={selected.has(m.name)}
                      onChange={() => toggle(m.name)}
                      label={renderModelLabel(m)}
                    />
                  )}
                </li>
              );
            })}
          </ul>
        </>
      ) : (
        <div className={styles.discoveryEmpty}>{t('providersPage.discovery.notLoaded')}</div>
      )}

      <div className={styles.discoveryFooter}>
        <button
          type="button"
          className={styles.connectivityBtnGhost}
          onClick={onClose}
          disabled={mutating}
        >
          {t('providersPage.discovery.close')}
        </button>
        <button
          type="button"
          className={styles.discoveryApplyBtn}
          onClick={handleApply}
          disabled={mutating || selected.size === 0}
        >
          {t('providersPage.discovery.apply', { count: selected.size })}
        </button>
      </div>
    </div>
  );
}
