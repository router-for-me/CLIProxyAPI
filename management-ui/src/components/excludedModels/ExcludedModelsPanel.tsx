import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconCheck, IconSearch } from '@/components/ui/icons';
import {
  getModelExclusionState,
  type ExclusionStats,
  type ModelExclusionState,
} from './excludedModelRules';
import styles from './ExcludedModelsPicker.module.scss';

export interface ExcludedModelCandidate {
  id: string;
  displayName?: string;
}

interface ExcludedModelsPanelProps {
  rules: readonly string[];
  candidates: readonly ExcludedModelCandidate[];
  /** 由 Picker 算好传下来，避免在 footer 里把整个目录再扫一遍。 */
  stats: ExclusionStats;
  onToggle: (modelId: string, excluded: boolean) => void;
  onSelectAll: () => void;
  onClear: () => void;
  disabled: boolean;
  listboxId: string;
  /** 展开后是否把焦点送进搜索框（键盘展开时为 true，鼠标点开时也为 true）。 */
  autoFocus: boolean;
  /** 收起面板并把焦点还给 trigger。 */
  onDismiss: () => void;
}

const matchesQuery = (candidate: ExcludedModelCandidate, query: string): boolean =>
  candidate.id.toLowerCase().includes(query) ||
  (candidate.displayName ?? '').toLowerCase().includes(query);

export function ExcludedModelsPanel({
  rules,
  candidates,
  stats,
  onToggle,
  onSelectAll,
  onClear,
  disabled,
  listboxId,
  autoFocus,
  onDismiss,
}: ExcludedModelsPanelProps) {
  const { t } = useTranslation();
  const [query, setQuery] = useState('');
  const [highlight, setHighlight] = useState(0);
  const inputRef = useRef<HTMLInputElement | null>(null);

  const visible = useMemo(() => {
    const normalized = query.trim().toLowerCase();
    if (!normalized) return candidates;
    return candidates.filter((candidate) => matchesQuery(candidate, normalized));
  }, [candidates, query]);

  // 高亮永远钳在可见范围内：过滤后列表变短，旧索引会指向不存在的行。
  const activeIndex = visible.length === 0 ? -1 : Math.min(highlight, visible.length - 1);
  const activeId = activeIndex >= 0 ? `${listboxId}-opt-${activeIndex}` : undefined;

  useLayoutEffect(() => {
    if (!autoFocus) return;
    // preventScroll：裸 focus() 会把外层 Sheet 的滚动猛拽过来，动画中途还会把面板顶出视野。
    inputRef.current?.focus({ preventScroll: true });
  }, [autoFocus]);

  useEffect(() => {
    if (!autoFocus || activeIndex < 0) return;
    document
      .getElementById(`${listboxId}-opt-${activeIndex}`)
      ?.scrollIntoView({ block: 'nearest' });
  }, [activeIndex, autoFocus, listboxId]);

  const toggleAt = (index: number) => {
    const candidate = visible[index];
    if (!candidate || disabled) return;
    const current = getModelExclusionState(rules, candidate.id);
    // 纯通配符命中的行不可直接切换——它的排除权属于那条规则。行内副文本常驻解释原因。
    if (current.state === 'excluded' && current.by === 'wildcard') return;
    onToggle(candidate.id, current.state !== 'excluded');
  };

  const handleKeyDown = (event: React.KeyboardEvent<HTMLInputElement>) => {
    switch (event.key) {
      case 'ArrowDown':
        event.preventDefault();
        setHighlight((prev) => Math.min(prev + 1, visible.length - 1));
        return;
      case 'ArrowUp':
        event.preventDefault();
        setHighlight((prev) => Math.max(prev - 1, 0));
        return;
      case 'Home':
        if (visible.length === 0) return;
        event.preventDefault();
        setHighlight(0);
        return;
      case 'End':
        if (visible.length === 0) return;
        event.preventDefault();
        setHighlight(visible.length - 1);
        return;
      case 'Enter':
        event.preventDefault();
        if (activeIndex >= 0) toggleAt(activeIndex);
        return;
      case 'Escape':
        // 外层 Sheet 在 document 上、OAuth 页在 window 上都听 Escape。
        // 不拦住就会「关面板 = 关 Sheet / 离开页面 + 触发未保存弹窗」。
        event.preventDefault();
        event.stopPropagation();
        if (query) {
          setQuery('');
          setHighlight(0);
          return;
        }
        onDismiss();
        return;
      default:
    }
  };

  return (
    <div className={styles.panel}>
      <div className={styles.searchRow}>
        <IconSearch size={14} className={styles.searchIcon} aria-hidden="true" />
        <input
          ref={inputRef}
          type="text"
          className={styles.search}
          value={query}
          onChange={(event) => {
            setQuery(event.target.value);
            setHighlight(0);
          }}
          onKeyDown={handleKeyDown}
          placeholder={t('excluded_models.search_placeholder')}
          aria-label={t('excluded_models.search_aria')}
          aria-controls={listboxId}
          aria-activedescendant={activeId}
          disabled={disabled}
          autoComplete="off"
          spellCheck={false}
        />
      </div>

      <div
        id={listboxId}
        className={styles.list}
        role="listbox"
        aria-multiselectable="true"
        aria-label={t('excluded_models.list_aria')}
      >
        {visible.length === 0 ? (
          <p className={styles.noResults}>
            {query.trim()
              ? t('excluded_models.no_results', { query: query.trim() })
              : t('excluded_models.catalog_empty')}
          </p>
        ) : (
          visible.map((candidate, index) => (
            <ExcludedModelRow
              key={candidate.id.toLowerCase()}
              id={`${listboxId}-opt-${index}`}
              candidate={candidate}
              state={getModelExclusionState(rules, candidate.id)}
              highlighted={index === activeIndex}
              onHover={() => setHighlight(index)}
              onToggle={() => toggleAt(index)}
            />
          ))
        )}
      </div>

      <div className={styles.footer}>
        <span className={styles.footerCount}>
          {t('excluded_models.footer_count', { excluded: stats.excluded, total: stats.total })}
        </span>
        <span className={styles.footerActions}>
          <button
            type="button"
            className={styles.footerButton}
            onClick={onSelectAll}
            disabled={disabled || candidates.length === 0}
          >
            {t('excluded_models.select_all')}
          </button>
          <button
            type="button"
            className={styles.footerButton}
            onClick={onClear}
            disabled={disabled}
            aria-label={t('excluded_models.clear_aria')}
          >
            {t('excluded_models.clear')}
          </button>
        </span>
      </div>
    </div>
  );
}

interface ExcludedModelRowProps {
  id: string;
  candidate: ExcludedModelCandidate;
  state: ModelExclusionState;
  highlighted: boolean;
  onHover: () => void;
  onToggle: () => void;
}

function ExcludedModelRow({
  id,
  candidate,
  state,
  highlighted,
  onHover,
  onToggle,
}: ExcludedModelRowProps) {
  const { t } = useTranslation();
  const excluded = state.state === 'excluded';
  const lockedByRule = state.state === 'excluded' && state.by === 'wildcard';
  // 把「哪条规则、用哪句话解释」在一处收敛好，下面的 JSX 就不必再做类型收窄。
  const wildcardReason =
    state.state === 'excluded' && (state.by === 'wildcard' || state.by === 'both')
      ? {
          rule: state.rule,
          text:
            state.by === 'wildcard'
              ? t('excluded_models.wildcard_locked', { rule: state.rule })
              : t('excluded_models.also_wildcard', { rule: state.rule }),
          muted: state.by === 'both',
        }
      : null;

  const rowClass = [
    styles.row,
    excluded ? styles.rowExcluded : '',
    lockedByRule ? styles.rowLocked : '',
    highlighted ? styles.rowHighlighted : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <div
      id={id}
      role="option"
      // 行永不进 tab 序：外层 Sheet 的焦点陷阱每次 Tab 都枚举全部可聚焦元素，
      // 几十个可聚焦的行会把它拖垮。漫游全靠 aria-activedescendant。
      tabIndex={-1}
      aria-selected={excluded}
      aria-disabled={lockedByRule || undefined}
      className={rowClass}
      onMouseEnter={onHover}
      onClick={onToggle}
    >
      <span className={styles.checkbox} aria-hidden="true">
        {excluded ? <IconCheck size={12} /> : null}
      </span>
      <span className={styles.rowText}>
        <span className={styles.rowId}>{candidate.id}</span>
        {candidate.displayName && candidate.displayName !== candidate.id ? (
          <span className={styles.rowDisplayName}>{candidate.displayName}</span>
        ) : null}
        {wildcardReason ? <span className={styles.rowReason}>{wildcardReason.text}</span> : null}
      </span>
      {wildcardReason ? (
        <span className={`${styles.badge} ${wildcardReason.muted ? styles.badgeMuted : ''}`.trim()}>
          {t('excluded_models.badge_wildcard')}
        </span>
      ) : null}
    </div>
  );
}
