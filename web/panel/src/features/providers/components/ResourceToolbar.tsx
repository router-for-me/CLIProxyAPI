import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import {
  IconArrowDownWideNarrow,
  IconArrowUpNarrowWide,
  IconChevronDown,
  IconSearch,
  IconSlidersHorizontal,
  IconX,
} from '@/components/ui/icons';
import { Select } from '@/components/ui/Select';
import { SelectionCheckbox } from '@/components/ui/SelectionCheckbox';
import { getModelPickerView } from '../resourceView';
import type { ProviderSortBy, SortDir } from '../types';
import styles from './ResourceToolbar.module.scss';

interface ResourceToolbarProps {
  searchLabel?: string;
  filter: string;
  onFilterChange: (value: string) => void;
  sortBy: ProviderSortBy;
  sortDir: SortDir;
  onSortBy: (value: ProviderSortBy) => void;
  onSortDir: (value: SortDir) => void;
  availableModels: ReadonlyArray<string>;
  selectedModels: ReadonlySet<string>;
  onSelectedModelsChange: (next: Set<string>) => void;
}

/** High-frequency controls: no entrance motion, feedback is immediate. */
export function ResourceToolbar({
  searchLabel,
  filter,
  onFilterChange,
  sortBy,
  sortDir,
  onSortBy,
  onSortDir,
  availableModels,
  selectedModels,
  onSelectedModelsChange,
}: ResourceToolbarProps) {
  const { t } = useTranslation();
  const popoverId = useId();
  const [modelsOpen, setModelsOpen] = useState(false);
  const [modelQuery, setModelQuery] = useState('');
  const modelsRef = useRef<HTMLDivElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);

  const sortOptions = useMemo(
    () => [
      { value: 'name', label: t('providersPage.toolbar.sort.name') },
      { value: 'priority', label: t('providersPage.toolbar.sort.priority') },
      { value: 'recent-success', label: t('providersPage.toolbar.sort.recentSuccess') },
    ],
    [t]
  );

  useEffect(() => {
    if (!modelsOpen) return;
    const onPointerDown = (event: PointerEvent) => {
      if (!modelsRef.current?.contains(event.target as Node)) setModelsOpen(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key !== 'Escape') return;
      setModelsOpen(false);
      triggerRef.current?.focus();
    };
    document.addEventListener('pointerdown', onPointerDown);
    document.addEventListener('keydown', onKeyDown);
    return () => {
      document.removeEventListener('pointerdown', onPointerDown);
      document.removeEventListener('keydown', onKeyDown);
    };
  }, [modelsOpen]);

  const { showSearch, visibleModels } = useMemo(
    () => getModelPickerView(availableModels, modelQuery),
    [availableModels, modelQuery]
  );

  const toggleModel = (name: string) => {
    const next = new Set(selectedModels);
    if (next.has(name)) next.delete(name);
    else next.add(name);
    onSelectedModelsChange(next);
  };

  const hasModelFilter = selectedModels.size > 0;
  const modelsLabel = hasModelFilter
    ? t('providersPage.toolbar.filter.selectedModels', {
        selected: selectedModels.size,
        total: availableModels.length,
      })
    : t('providersPage.toolbar.filter.allModels');
  const dirLabel =
    sortDir === 'asc'
      ? t('providersPage.toolbar.sort.directionAsc')
      : t('providersPage.toolbar.sort.directionDesc');

  return (
    <div className={styles.toolbar}>
      <label className={styles.search}>
        <IconSearch size={15} className={styles.searchIcon} aria-hidden="true" />
        <input
          type="search"
          className={styles.searchInput}
          value={filter}
          onChange={(event) => onFilterChange(event.target.value)}
          placeholder={searchLabel ?? t('providersPage.table.filterPlaceholder')}
          aria-label={searchLabel ?? t('providersPage.table.filterPlaceholder')}
        />
        {filter ? (
          <button
            type="button"
            className={styles.searchClear}
            onClick={() => onFilterChange('')}
            aria-label={t('providersPage.toolbar.clearFilters')}
            title={t('providersPage.toolbar.clearFilters')}
          >
            <IconX size={13} />
          </button>
        ) : null}
      </label>

      <div className={styles.sort}>
        <Select
          value={sortBy}
          options={sortOptions}
          onChange={(value) => onSortBy(value as ProviderSortBy)}
          ariaLabel={t('providersPage.toolbar.sortBy')}
          size="sm"
        />
        <button
          type="button"
          className={styles.iconToggle}
          onClick={() => onSortDir(sortDir === 'asc' ? 'desc' : 'asc')}
          aria-label={dirLabel}
          title={dirLabel}
        >
          {sortDir === 'asc' ? (
            <IconArrowUpNarrowWide size={15} />
          ) : (
            <IconArrowDownWideNarrow size={15} />
          )}
        </button>
      </div>

      <div className={styles.models} ref={modelsRef}>
        <button
          ref={triggerRef}
          type="button"
          className={`${styles.pill} ${hasModelFilter ? styles.pillActive : ''}`}
          onClick={() => setModelsOpen((open) => !open)}
          disabled={availableModels.length === 0}
          aria-expanded={modelsOpen}
          aria-controls={modelsOpen ? popoverId : undefined}
          aria-haspopup="dialog"
        >
          <IconSlidersHorizontal size={14} />
          <span>{modelsLabel}</span>
          <IconChevronDown size={12} className={styles.pillChevron} />
        </button>
        {modelsOpen ? (
          <div
            id={popoverId}
            className={styles.popover}
            role="dialog"
            aria-label={t('providersPage.toolbar.filter.allModels')}
          >
            {showSearch ? (
              <input
                type="search"
                className={styles.popoverSearch}
                value={modelQuery}
                onChange={(event) => setModelQuery(event.target.value)}
                placeholder={t('providersPage.discovery.searchPlaceholder')}
                aria-label={t('providersPage.discovery.searchPlaceholder')}
                autoFocus
              />
            ) : null}
            {visibleModels.length === 0 ? (
              <div className={styles.popoverEmpty}>{t('providersPage.toolbar.filter.empty')}</div>
            ) : (
              <ul className={styles.popoverList}>
                {visibleModels.map((name) => (
                  <li key={name}>
                    <SelectionCheckbox
                      checked={selectedModels.has(name)}
                      onChange={() => toggleModel(name)}
                      className={styles.popoverItem}
                      label={<span className={styles.popoverItemLabel}>{name}</span>}
                    />
                  </li>
                ))}
              </ul>
            )}
            <div className={styles.popoverFooter}>
              <button
                type="button"
                className={styles.popoverAction}
                onClick={() => onSelectedModelsChange(new Set(availableModels))}
              >
                {t('providersPage.toolbar.filter.selectAll')}
              </button>
              <button
                type="button"
                className={styles.popoverAction}
                onClick={() => onSelectedModelsChange(new Set())}
                disabled={!hasModelFilter}
              >
                {t('providersPage.toolbar.filter.clear')}
              </button>
            </div>
          </div>
        ) : null}
      </div>
    </div>
  );
}
