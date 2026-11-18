import { useId } from 'react';
import { useTranslation } from 'react-i18next';
import { AutocompleteInput } from '@/components/ui/AutocompleteInput';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { IconPlus, IconX } from '@/components/ui/icons';
import type { AuthFileModelItem } from '../constants';
import type { AliasDraftEntry, AliasRowIssue } from './draft';
import { RuleGroup } from './RuleGroup';
import type { ProviderCatalogState } from './useModelRulesData';
import styles from './ModelRules.module.scss';

type RowPatch = Partial<Pick<AliasDraftEntry, 'name' | 'alias' | 'fork'>>;

type AliasLedgerRowProps = {
  entry: AliasDraftEntry;
  index: number;
  options: { value: string; label?: string }[];
  issue?: AliasRowIssue;
  disabled: boolean;
  onChange: (patch: RowPatch) => void;
  onRemove: () => void;
};

const ISSUE_KEYS: Record<AliasRowIssue, string> = {
  incomplete: 'model_rules.row_incomplete',
  same: 'model_rules.row_same',
  duplicate: 'oauth_model_alias.duplicate_alias',
};

/**
 * 一条映射 = 一行：源模型 → 别名 · 保留原名 · 删除。
 * 列头已经说明了每一格是什么，所以行内标签只留给读屏（视觉隐藏、关联不丢）。
 */
export function AliasLedgerRow({
  entry,
  index,
  options,
  issue,
  disabled,
  onChange,
  onRemove,
}: AliasLedgerRowProps) {
  const { t } = useTranslation();
  const id = useId();
  const issueId = `${id}-issue`;
  const number = index + 1;

  return (
    <div
      className={`${styles.row} ${issue ? styles.rowInvalid : ''}`}
      role="group"
      aria-label={t('oauth_model_alias.mapping_row', { number })}
      aria-describedby={issue ? issueId : undefined}
    >
      <AutocompleteInput
        id={`${id}-source`}
        label={t('oauth_model_alias.alias_name_placeholder')}
        wrapperClassName={styles.cell}
        className={styles.cellInput}
        placeholder={t('oauth_model_alias.alias_name_placeholder')}
        value={entry.name}
        onChange={(value) => onChange({ name: value })}
        disabled={disabled}
        options={options}
      />
      <span className={styles.arrow} aria-hidden="true">
        →
      </span>
      <div className={`${styles.cell} ${styles.cellAlias}`}>
        <label htmlFor={`${id}-alias`}>{t('oauth_model_alias.alias_placeholder')}</label>
        <input
          id={`${id}-alias`}
          className={`input ${styles.cellInput}`}
          placeholder={t('oauth_model_alias.alias_placeholder')}
          value={entry.alias}
          onChange={(event) => onChange({ alias: event.target.value })}
          disabled={disabled}
          aria-invalid={issue ? true : undefined}
          autoComplete="off"
          autoCapitalize="off"
          spellCheck={false}
        />
      </div>
      <div className={styles.rowFork}>
        <ToggleSwitch
          ariaLabel={t('oauth_model_alias.alias_fork_label')}
          label={
            <span className={styles.forkLabel}>{t('oauth_model_alias.alias_fork_label')}</span>
          }
          labelPosition="left"
          checked={Boolean(entry.fork)}
          onChange={(value) => onChange({ fork: value })}
          disabled={disabled}
        />
      </div>
      <button
        type="button"
        className={styles.rowRemove}
        onClick={onRemove}
        disabled={disabled}
        title={t('oauth_model_alias.remove_mapping', { number })}
        aria-label={t('oauth_model_alias.remove_mapping', { number })}
      >
        <IconX size={14} aria-hidden="true" />
      </button>
      {issue ? (
        <p className={styles.rowIssue} id={issueId}>
          {t(ISSUE_KEYS[issue])}
        </p>
      ) : null}
    </div>
  );
}

export type AliasLedgerGroup = {
  provider: string;
  label: string;
  icon: string | null;
  entries: AliasDraftEntry[];
  /** 搜索命中的行；未搜索时等于 entries。 */
  visibleEntries: AliasDraftEntry[];
  issues: Record<string, AliasRowIssue>;
  mappingCount: number;
  dirty: boolean;
  models: AuthFileModelItem[];
  catalogState: ProviderCatalogState | undefined;
};

export type AliasLedgerProps = {
  groups: readonly AliasLedgerGroup[];
  /** 聚焦单个 provider 时显示目录提示。 */
  focused: boolean;
  disabled: boolean;
  onChangeRow: (provider: string, id: string, patch: RowPatch) => void;
  onAddRow: (provider: string) => void;
  onRemoveRow: (provider: string, id: string) => void;
  onClearProvider: (provider: string) => void;
};

export function AliasLedger({
  groups,
  focused,
  disabled,
  onChangeRow,
  onAddRow,
  onRemoveRow,
  onClearProvider,
}: AliasLedgerProps) {
  const { t } = useTranslation();

  return (
    <>
      {groups.map((group) => {
        const options = group.models.map((model) => ({
          value: model.id,
          label:
            model.display_name && model.display_name !== model.id ? model.display_name : undefined,
        }));
        const catalogHint =
          group.catalogState === 'loading'
            ? t('oauth_model_alias.model_source_loading')
            : group.catalogState === 'ready'
              ? t('oauth_model_alias.model_source_loaded', { count: group.models.length })
              : group.catalogState
                ? t('oauth_model_alias.model_source_unsupported')
                : '';

        return (
          <RuleGroup
            key={group.provider}
            provider={group.provider}
            label={group.label}
            icon={group.icon}
            summary={
              group.mappingCount > 0
                ? t('model_rules.meta_mappings', { count: group.mappingCount })
                : t('oauth_model_alias.no_models')
            }
            dirty={group.dirty}
            dirtyLabel={t('model_rules.unsaved_mark')}
            actions={
              group.mappingCount > 0 ? (
                <button
                  type="button"
                  className={styles.quietDanger}
                  onClick={() => onClearProvider(group.provider)}
                  disabled={disabled}
                >
                  {t('model_rules.clear_provider')}
                </button>
              ) : null
            }
          >
            <div className={styles.ledger}>
              {group.visibleEntries.length > 0 ? (
                <div className={styles.ledgerHead} aria-hidden="true">
                  <span>{t('oauth_model_alias.diagram_source_models')}</span>
                  <span />
                  <span>{t('oauth_model_alias.diagram_aliases')}</span>
                  <span className={styles.ledgerHeadFork}>
                    {t('oauth_model_alias.alias_fork_label')}
                  </span>
                  <span />
                </div>
              ) : null}
              {group.visibleEntries.map((entry) => (
                <AliasLedgerRow
                  key={entry.id}
                  entry={entry}
                  index={group.entries.indexOf(entry)}
                  options={options}
                  issue={group.issues[entry.id]}
                  disabled={disabled}
                  onChange={(patch) => onChangeRow(group.provider, entry.id, patch)}
                  onRemove={() => onRemoveRow(group.provider, entry.id)}
                />
              ))}
            </div>
            <footer className={styles.groupFoot}>
              <button
                type="button"
                className={styles.addRow}
                onClick={() => onAddRow(group.provider)}
                disabled={disabled}
              >
                <IconPlus size={14} aria-hidden="true" />
                {t('oauth_model_alias.add_alias')}
              </button>
              {focused && catalogHint ? <p className={styles.groupHint}>{catalogHint}</p> : null}
            </footer>
          </RuleGroup>
        );
      })}
    </>
  );
}
