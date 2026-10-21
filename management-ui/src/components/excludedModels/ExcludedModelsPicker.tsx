import { useCallback, useId, useMemo, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconAlertTriangle, IconChevronDown, IconLoader2 } from '@/components/ui/icons';
import { ExcludedModelChipRow, ExcludedModelRuleChip } from './ExcludedModelRuleChip';
import { ExcludedModelsPanel, type ExcludedModelCandidate } from './ExcludedModelsPanel';
import {
  formatExcludedRulesText,
  getModelExclusionState,
  matchedModelsByRule,
  normalizeExcludedRules,
  replaceCustomExcludedRules,
  splitExcludedRules,
  summarizeExclusion,
  toggleExcludedRule,
} from './excludedModelRules';
import styles from './ExcludedModelsPicker.module.scss';

export type { ExcludedModelCandidate };

export type ExcludedModelsCatalogState = 'ready' | 'loading' | 'unavailable' | 'error';

/** 派生 chip 的上限——超过这个数就只报总数，否则 chip 行会淹没整个字段。 */
const DERIVED_CHIP_LIMIT = 8;

export interface ExcludedModelsPickerProps {
  /** 规范的规则列表。调用方内部存文本/Set 都行，在边界上适配一次即可。 */
  value: readonly string[];
  onChange: (next: string[]) => void;

  candidates: readonly ExcludedModelCandidate[];
  catalogState?: ExcludedModelsCatalogState;
  onRetryCatalog?: () => void;

  /** 真实禁用（未连接 / 保存中）。**绝不要**因为目录为空就传 true。 */
  disabled?: boolean;

  /** picker 不得读写、也不许用户输入的规则。provider 表单传 `['*']`。 */
  reservedRules?: readonly string[];
  reservedRuleMessage?: string;

  /** 关掉通配符规则编辑器。 */
  showRuleEditor?: boolean;

  labelledBy?: string;
  className?: string;
}

export function ExcludedModelsPicker({
  value,
  onChange,
  candidates,
  catalogState = 'ready',
  onRetryCatalog,
  disabled = false,
  reservedRules,
  reservedRuleMessage,
  showRuleEditor = true,
  labelledBy,
  className,
}: ExcludedModelsPickerProps) {
  const { t } = useTranslation();
  const baseId = useId();
  const panelId = `${baseId}-panel`;
  const listboxId = `${baseId}-listbox`;
  const [open, setOpen] = useState(false);
  const [reservedHit, setReservedHit] = useState(false);
  const triggerRef = useRef<HTMLButtonElement | null>(null);

  const reservedKeys = useMemo(
    () => new Set((reservedRules ?? []).map((rule) => rule.trim().toLowerCase())),
    [reservedRules]
  );

  /**
   * 保留规则在**入口**就被剥掉，因此 picker 内部从不见到它，也就不可能把它写回去。
   * provider 表单的 `'*'`（= 已停用）由 disabled 开关独占，排除面无权触碰。
   */
  const rules = useMemo(
    () =>
      normalizeExcludedRules(value).filter((rule) => !reservedKeys.has(rule.trim().toLowerCase())),
    [reservedKeys, value]
  );

  const candidateIds = useMemo(() => candidates.map((c) => c.id), [candidates]);
  const stats = useMemo(() => summarizeExclusion(rules, candidateIds), [candidateIds, rules]);
  const { exactRules, unknownRules, customRules } = useMemo(
    () => splitExcludedRules(rules, candidateIds),
    [candidateIds, rules]
  );

  const commit = useCallback(
    (next: readonly string[]) => {
      // 出口再滤一次保留规则：纵深防御，规则编辑器里手打的 `*` 到不了调用方。
      onChange(next.filter((rule) => !reservedKeys.has(rule.trim().toLowerCase())));
    },
    [onChange, reservedKeys]
  );

  const hasCatalog = catalogState === 'ready' && candidates.length > 0;

  /** 通配符派生出的模型（排除掉已显式勾选的，那些走实线 chip）。 */
  const derivedModels = useMemo(() => {
    if (!hasCatalog) return [];
    const out: Array<{ id: string; rule: string }> = [];
    candidateIds.forEach((id) => {
      const state = getModelExclusionState(rules, id);
      if (state.state === 'excluded' && state.by === 'wildcard') out.push({ id, rule: state.rule });
    });
    return out;
  }, [candidateIds, hasCatalog, rules]);

  const ruleSummaries = useMemo(
    () => (hasCatalog ? matchedModelsByRule(customRules, candidateIds) : []),
    [candidateIds, customRules, hasCatalog]
  );

  const handleToggle = (modelId: string, excluded: boolean) =>
    commit(toggleExcludedRule(rules, modelId, excluded));

  const handleSelectAll = () => commit(normalizeExcludedRules([...rules, ...candidateIds]));

  /** 只清精确勾选，通配符规则留给它自己的编辑器——否则一次点击会抹掉用户手写的规则。 */
  const handleClear = () => commit(customRules);

  const handleRuleEditorChange = (text: string) => {
    const typedReserved = text
      .split(/\r?\n/)
      .some((line) => reservedKeys.has(line.trim().toLowerCase()));
    setReservedHit(typedReserved);
    commit(replaceCustomExcludedRules(rules, candidateIds, text));
  };

  const dismissPanel = useCallback(() => {
    setOpen(false);
    triggerRef.current?.focus({ preventScroll: true });
  }, []);

  const summaryText = () => {
    if (catalogState === 'loading') return t('excluded_models.catalog_loading');
    if (hasCatalog) {
      if (stats.excluded === 0 && rules.length === 0) return t('excluded_models.trigger_empty');
      return t('excluded_models.trigger_summary', {
        excluded: stats.excluded,
        available: stats.available,
      });
    }
    // 无目录：只能诚实地报规则条数，不能假装知道「还剩几个可用」。
    if (rules.length === 0) return t('excluded_models.trigger_empty');
    return t('excluded_models.trigger_summary_rules', { n: rules.length });
  };

  return (
    <div className={`${styles.root} ${className ?? ''}`.trim()}>
      <button
        ref={triggerRef}
        type="button"
        className={`${styles.trigger} ${open ? styles.triggerOpen : ''}`.trim()}
        onClick={() => setOpen((prev) => !prev)}
        onKeyDown={(event) => {
          if (event.key === 'ArrowDown' && !open) {
            event.preventDefault();
            setOpen(true);
          } else if (event.key === 'Escape' && open) {
            event.preventDefault();
            event.stopPropagation();
            setOpen(false);
          }
        }}
        aria-expanded={open}
        aria-controls={open ? panelId : undefined}
        aria-labelledby={labelledBy}
        disabled={disabled}
      >
        <span className={styles.triggerText}>
          {catalogState === 'loading' ? (
            <IconLoader2 size={13} className={styles.triggerSpinner} aria-hidden="true" />
          ) : null}
          {summaryText()}
        </span>
        <IconChevronDown size={14} className={styles.chevron} aria-hidden="true" />
        {hasCatalog ? (
          <span
            className={styles.meter}
            role="img"
            aria-label={t('excluded_models.meter_aria', {
              excluded: stats.excluded,
              total: stats.total,
            })}
          >
            <span
              className={styles.meterFill}
              style={{ width: `${stats.total ? (stats.excluded / stats.total) * 100 : 0}%` }}
            />
          </span>
        ) : null}
      </button>

      <div
        id={panelId}
        className={`${styles.disclosure} ${open ? styles.disclosureOpen : ''}`.trim()}
      >
        <div className={styles.disclosureInner} inert={!open}>
          {catalogState === 'ready' || candidates.length > 0 ? (
            <ExcludedModelsPanel
              rules={rules}
              candidates={candidates}
              stats={stats}
              onToggle={handleToggle}
              onSelectAll={handleSelectAll}
              onClear={handleClear}
              disabled={disabled}
              listboxId={listboxId}
              autoFocus={open}
              onDismiss={dismissPanel}
            />
          ) : (
            <div className={styles.catalogNotice}>
              <span>
                {catalogState === 'loading'
                  ? t('excluded_models.catalog_loading')
                  : catalogState === 'error'
                    ? t('excluded_models.catalog_error')
                    : t('excluded_models.catalog_unavailable')}
              </span>
              {onRetryCatalog && catalogState !== 'loading' ? (
                <button type="button" className={styles.retryButton} onClick={onRetryCatalog}>
                  {t('excluded_models.catalog_retry')}
                </button>
              ) : null}
            </div>
          )}
        </div>
      </div>

      {exactRules.length > 0 || derivedModels.length > 0 || unknownRules.length > 0 ? (
        <ExcludedModelChipRow>
          {exactRules.map((rule) => (
            <ExcludedModelRuleChip
              key={`exact-${rule.toLowerCase()}`}
              label={rule}
              variant="exact"
              onRemove={() => commit(toggleExcludedRule(rules, rule, false))}
              removeAriaLabel={t('excluded_models.chip_remove', { rule })}
              disabled={disabled}
            />
          ))}
          {derivedModels.slice(0, DERIVED_CHIP_LIMIT).map((item) => (
            <ExcludedModelRuleChip
              key={`derived-${item.id.toLowerCase()}`}
              label={item.id}
              variant="wildcard"
              detail={item.rule}
              title={t('excluded_models.wildcard_reason', { rule: item.rule })}
            />
          ))}
          {derivedModels.length > DERIVED_CHIP_LIMIT ? (
            <span className={styles.chipsMore}>
              {t('excluded_models.chips_more', { n: derivedModels.length - DERIVED_CHIP_LIMIT })}
            </span>
          ) : null}
          {unknownRules.map((rule) => (
            <ExcludedModelRuleChip
              key={`unknown-${rule.toLowerCase()}`}
              label={rule}
              variant="unknown"
              detail={t('excluded_models.badge_unknown')}
              onRemove={() => commit(toggleExcludedRule(rules, rule, false))}
              removeAriaLabel={t('excluded_models.chip_remove', { rule })}
              disabled={disabled}
            />
          ))}
        </ExcludedModelChipRow>
      ) : null}

      {showRuleEditor ? (
        <div className={styles.ruleEditor}>
          <label className={styles.ruleLabel} htmlFor={`${baseId}-rules`}>
            {t('excluded_models.rules_label')}
          </label>
          <textarea
            id={`${baseId}-rules`}
            className="input"
            value={formatExcludedRulesText(customRules)}
            placeholder={t('excluded_models.rules_placeholder')}
            rows={3}
            disabled={disabled}
            spellCheck={false}
            onChange={(event) => handleRuleEditorChange(event.target.value)}
          />

          {reservedHit ? (
            <p className={styles.ruleWarning}>
              <IconAlertTriangle size={12} aria-hidden="true" />
              {reservedRuleMessage ?? t('excluded_models.rules_reserved')}
            </p>
          ) : null}

          {hasCatalog ? (
            <ul className={styles.ruleMatches}>
              {ruleSummaries.map((summary) => (
                <li
                  key={summary.rule.toLowerCase()}
                  className={summary.matchCount === 0 ? styles.ruleMatchNone : undefined}
                >
                  <code>{summary.rule}</code>
                  {summary.matchCount === 0
                    ? t('excluded_models.rules_match_none')
                    : t('excluded_models.rules_match_count', { n: summary.matchCount })}
                </li>
              ))}
            </ul>
          ) : null}

          <p className="hint">{t('excluded_models.rules_hint')}</p>
        </div>
      ) : null}
    </div>
  );
}
