import { Suspense, lazy } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Input } from '@/components/ui/Input';
import { IconChevronDown, IconChevronUp, IconSearch } from '@/components/ui/icons';
import type { UseSourceSearchResult } from '../hooks/useSourceSearch';
import styles from './SourcePanel.module.scss';

const LazyConfigSourceEditor = lazy(() => import('./ConfigSourceEditor'));

export type SourceSearchBarProps = {
  search: UseSourceSearchResult;
  disabled: boolean;
};

/** 源码模式的搜索条：占据工具栏行的搜索槽位（与可视化模式的字段搜索同位置）。 */
export function SourceSearchBar({ search, disabled }: SourceSearchBarProps) {
  const { t } = useTranslation();
  const {
    searchQuery,
    searchResults,
    lastSearchedQuery,
    handleSearchChange,
    executeSearch,
    handleSearchKeyDown,
    handlePrevMatch,
    handleNextMatch,
  } = search;

  return (
    <div className={styles.searchBar}>
      <div className={styles.searchInputWrapper}>
        <Input
          value={searchQuery}
          onChange={(e) => handleSearchChange(e.target.value)}
          onKeyDown={handleSearchKeyDown}
          placeholder={t('config_management.search_placeholder')}
          disabled={disabled}
          className={styles.searchInput}
          rightElement={
            <div className={styles.searchRight}>
              {searchQuery && lastSearchedQuery === searchQuery && (
                <span className={styles.searchCount}>
                  {searchResults.total > 0
                    ? `${searchResults.current} / ${searchResults.total}`
                    : t('config_management.search_no_results')}
                </span>
              )}
              <button
                type="button"
                className={styles.searchButton}
                onClick={() => executeSearch('next')}
                disabled={!searchQuery || disabled}
                title={t('config_management.search_button')}
              >
                <IconSearch size={16} />
              </button>
            </div>
          }
        />
      </div>

      <div className={styles.searchActions}>
        <Button
          variant="secondary"
          size="sm"
          onClick={handlePrevMatch}
          disabled={!searchQuery || lastSearchedQuery !== searchQuery || searchResults.total === 0}
          title={t('config_management.search_prev')}
        >
          <IconChevronUp size={16} />
        </Button>
        <Button
          variant="secondary"
          size="sm"
          onClick={handleNextMatch}
          disabled={!searchQuery || lastSearchedQuery !== searchQuery || searchResults.total === 0}
          title={t('config_management.search_next')}
        >
          <IconChevronDown size={16} />
        </Button>
      </div>
    </div>
  );
}

export type SourcePanelProps = {
  search: UseSourceSearchResult;
  value: string;
  onChange: (value: string) => void;
  theme: 'light' | 'dark';
  editable: boolean;
};

/** YAML 源码编辑面板：lazy CodeMirror（含语法高亮/折叠/内置搜索快捷键）。 */
export function SourcePanel({ search, value, onChange, theme, editable }: SourcePanelProps) {
  const { t } = useTranslation();

  return (
    <div className={styles.editorWrapper}>
      <Suspense fallback={null}>
        <LazyConfigSourceEditor
          editorRef={search.editorRef}
          value={value}
          onChange={onChange}
          theme={theme}
          editable={editable}
          placeholder={t('config_management.editor_placeholder')}
        />
      </Suspense>
    </div>
  );
}
