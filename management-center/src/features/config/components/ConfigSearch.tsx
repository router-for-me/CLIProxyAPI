import { useEffect, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { Input } from '@/components/ui/Input';
import { IconSearch } from '@/components/ui/icons';
import { searchConfigFields, type ConfigFieldSearchEntry } from '../searchIndex';
import styles from './ConfigSearch.module.scss';

export type ConfigSearchProps = {
  disabled?: boolean;
  /** 用户选中结果：由页面切换 tab 并滚动/脉冲目标字段（useFieldJump）。 */
  onJump: (entry: ConfigFieldSearchEntry) => void;
};

/**
 * 全局字段搜索 combobox（标签 / YAML 键名 / 关键词），键盘可导航。
 * 跳转后保留输入文本，只收起结果列表，便于继续调整查询。
 */
export function ConfigSearch({ disabled = false, onJump }: ConfigSearchProps) {
  const { t } = useTranslation();
  const [searchQuery, setSearchQuery] = useState('');
  // Dropdown visibility is tracked separately from the query text so a jump can close the
  // results while leaving the typed text in the box for further editing.
  const [searchOpen, setSearchOpen] = useState(false);
  // Highlighted option for keyboard navigation of the results listbox (-1 = none).
  const [activeResultIndex, setActiveResultIndex] = useState(0);
  const searchListboxId = useId();
  const searchResultsRef = useRef<HTMLDivElement | null>(null);
  const searchBoxRef = useRef<HTMLDivElement | null>(null);

  const searchResults = useMemo(() => searchConfigFields(searchQuery, t), [searchQuery, t]);
  // The results popup is visible only when the box is open AND there's a (trimmed) query.
  const isResultsOpen = searchOpen && Boolean(searchQuery.trim());
  // Clamp the highlighted index to the current result set so a stale index (e.g. after the
  // query narrows the list) never points past the end or at an option that no longer exists.
  const effectiveActiveIndex =
    searchResults.length > 0
      ? Math.min(Math.max(activeResultIndex, 0), searchResults.length - 1)
      : -1;

  const handleResultJump = (entry: ConfigFieldSearchEntry) => {
    // Keep the query text so the user can tweak it; just close the results dropdown.
    setSearchOpen(false);
    onJump(entry);
  };

  // Close the results dropdown (keeping the query) when clicking outside the search box.
  useEffect(() => {
    if (!searchOpen) return;
    const handlePointerDown = (event: MouseEvent) => {
      if (searchBoxRef.current && !searchBoxRef.current.contains(event.target as Node)) {
        setSearchOpen(false);
      }
    };
    document.addEventListener('mousedown', handlePointerDown);
    return () => document.removeEventListener('mousedown', handlePointerDown);
  }, [searchOpen]);

  // Keep the highlighted option scrolled into view during keyboard navigation.
  useEffect(() => {
    if (!isResultsOpen || effectiveActiveIndex < 0) return;
    const node = searchResultsRef.current?.querySelector<HTMLElement>(
      `[data-result-index="${effectiveActiveIndex}"]`
    );
    node?.scrollIntoView({ block: 'nearest' });
  }, [effectiveActiveIndex, isResultsOpen]);

  return (
    <div className={styles.searchBox} ref={searchBoxRef}>
      <Input
        className={styles.searchControl}
        placeholder={t('config_management.visual.search.placeholder')}
        aria-label={t('config_management.visual.search.placeholder')}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={isResultsOpen}
        aria-controls={isResultsOpen ? searchListboxId : undefined}
        aria-activedescendant={
          isResultsOpen && effectiveActiveIndex >= 0
            ? `${searchListboxId}-opt-${effectiveActiveIndex}`
            : undefined
        }
        value={searchQuery}
        disabled={disabled}
        onChange={(e) => {
          setSearchQuery(e.target.value);
          setSearchOpen(true);
          setActiveResultIndex(0);
        }}
        onFocus={() => setSearchOpen(true)}
        onKeyDown={(e) => {
          // Ignore keys fired while an IME is composing (e.g. picking a Chinese
          // candidate) — otherwise candidate selection triggers navigation/jump.
          if (e.nativeEvent.isComposing) return;
          if (e.key === 'Escape') {
            setSearchOpen(false);
            return;
          }
          if (e.key === 'ArrowDown') {
            e.preventDefault();
            if (!isResultsOpen) {
              setSearchOpen(true);
              return;
            }
            if (searchResults.length === 0) return;
            setActiveResultIndex((effectiveActiveIndex + 1) % searchResults.length);
            return;
          }
          if (e.key === 'ArrowUp') {
            e.preventDefault();
            if (!isResultsOpen) {
              setSearchOpen(true);
              return;
            }
            if (searchResults.length === 0) return;
            setActiveResultIndex(
              effectiveActiveIndex <= 0 ? searchResults.length - 1 : effectiveActiveIndex - 1
            );
            return;
          }
          if (e.key === 'Enter' && isResultsOpen && searchResults.length > 0) {
            e.preventDefault();
            handleResultJump(searchResults[effectiveActiveIndex] ?? searchResults[0]);
          }
        }}
        rightElement={
          <span className={styles.searchIcon} aria-hidden="true">
            <IconSearch size={16} />
          </span>
        }
      />
      {isResultsOpen ? (
        <div
          className={styles.searchResults}
          role="listbox"
          id={searchListboxId}
          aria-label={t('config_management.visual.search.placeholder')}
          ref={searchResultsRef}
        >
          {searchResults.length > 0 ? (
            searchResults.map((entry, index) => (
              <button
                key={entry.fieldId}
                type="button"
                role="option"
                id={`${searchListboxId}-opt-${index}`}
                data-result-index={index}
                tabIndex={-1}
                aria-selected={index === effectiveActiveIndex}
                className={`${styles.searchResultItem} ${
                  index === effectiveActiveIndex ? styles.searchResultItemActive : ''
                }`}
                onMouseEnter={() => setActiveResultIndex(index)}
                onClick={() => handleResultJump(entry)}
              >
                <span className={styles.searchResultLabel}>
                  {t(entry.labelKey)}
                  {entry.qualifierKey ? (
                    <span className={styles.searchResultQualifier}>{t(entry.qualifierKey)}</span>
                  ) : null}
                </span>
                <span className={styles.searchResultSection}>
                  {t(`config_management.visual.sections.${entry.sectionId}.title`)}
                </span>
              </button>
            ))
          ) : (
            <div className={styles.searchEmpty}>
              {t('config_management.visual.search.no_results')}
            </div>
          )}
        </div>
      ) : null}
    </div>
  );
}
