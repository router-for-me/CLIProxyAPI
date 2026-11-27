import { useMemo, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconLoader2, IconRefreshCw, IconSearch } from '@/components/ui/icons';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import type { ModelInfo } from '@/utils/models';
import {
  discoveryCatalog,
  discoverySelectionDiff,
  resolveDiscoverySelection,
  setDiscoveryChoices,
} from '../../discoverySelection';
import styles from './sharedForm.module.scss';

interface ModelDiscoveryPanelProps {
  loading: boolean;
  error: string | null;
  models: ModelInfo[];
  hasFetched: boolean;
  existingNames: Set<string>;
  mutating?: boolean;
  onApply: (picked: ModelInfo[], catalog: ModelInfo[]) => void;
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
  const [choices, setChoices] = useState<Map<string, boolean>>(new Map());
  const catalog = useMemo(() => discoveryCatalog(models), [models]);
  const selected = resolveDiscoverySelection(catalog, existingNames, choices);
  const diff = discoverySelectionDiff(catalog, existingNames, selected);
  const blocked = Boolean(mutating || loading || error || !hasFetched);
  const canApply = !blocked && diff.added + diff.removed > 0;

  const filtered = useMemo(() => {
    const q = search.trim().toLowerCase();
    if (!q) return catalog;
    return catalog.filter((m) =>
      `${m.name} ${m.alias ?? ''} ${m.displayName ?? ''}`.toLowerCase().includes(q)
    );
  }, [catalog, search]);

  const allSelectableChecked = filtered.length > 0 && filtered.every((m) => selected.has(m.name));

  const toggle = (name: string) => {
    setChoices((prev) => setDiscoveryChoices(prev, [name], !selected.has(name)));
  };

  const toggleAll = () => {
    setChoices((prev) =>
      setDiscoveryChoices(
        prev,
        filtered.map((m) => m.name),
        !allSelectableChecked
      )
    );
  };

  const handleApply = () => {
    if (!canApply) return;
    onApply(
      catalog.filter((m) => selected.has(m.name)),
      catalog
    );
    // Applied names now follow the parent again; keep choices for temporarily missing results.
    setChoices((prev) => {
      const next = new Map(prev);
      for (const { name } of catalog) next.delete(name);
      return next;
    });
  };

  const renderModelLabel = (model: ModelInfo) => {
    const label = model.alias || model.displayName;
    return (
      <span className={styles.discoveryNameGroup}>
        <span className={styles.discoveryName}>{model.name}</span>
        {label ? <span className={styles.discoveryAlias}>{label}</span> : null}
      </span>
    );
  };

  return (
    <div className={styles.discoveryPanel}>
      <div className={styles.discoveryToolbar}>
        <div className={styles.discoverySearchWrap}>
          <span className={styles.discoverySearchIcon} aria-hidden="true">
            <IconSearch size={14} />
          </span>
          <input
            type="search"
            className={`${styles.input} ${styles.discoverySearch}`}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            placeholder={t('providersPage.discovery.searchPlaceholder')}
            aria-label={t('providersPage.discovery.searchPlaceholder')}
          />
        </div>
        <button
          type="button"
          className={styles.connectivityBtnGhost}
          onClick={onReload}
          disabled={loading || mutating}
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

      <p className={styles.discoveryCount}>{t('providersPage.discovery.syncHint')}</p>

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
              disabled={blocked || filtered.length === 0}
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
                total: catalog.length,
              })}
            </span>
          </div>
          <ul className={styles.discoveryList}>
            {filtered.map((m) => (
              <li key={m.name} className={styles.discoveryItem}>
                <SelectionCheckbox
                  checked={selected.has(m.name)}
                  onChange={() => toggle(m.name)}
                  disabled={blocked}
                  label={renderModelLabel(m)}
                />
              </li>
            ))}
          </ul>
        </>
      ) : (
        <div className={styles.discoveryEmpty}>{t('providersPage.discovery.notLoaded')}</div>
      )}

      <div className={styles.discoveryFooter}>
        <button
          type="button"
          className={`${styles.footerBtn} ${styles.footerBtnGhost} ${styles.discoveryApplyBtn}`}
          onClick={onClose}
          disabled={mutating}
        >
          {t('providersPage.discovery.close')}
        </button>
        <button
          type="button"
          className={`${styles.footerBtn} ${styles.footerBtnPrimary} ${styles.discoveryApplyBtn}`}
          onClick={handleApply}
          disabled={!canApply}
        >
          {t('providersPage.discovery.applyChanges', diff)}
        </button>
      </div>
    </div>
  );
}
