import { memo, useEffect, useId, useRef } from 'react';
import { useTranslation } from 'react-i18next';
import { Button } from '@/components/ui/Button';
import { Select } from '@/components/ui/Select';
import {
  IconAlertTriangle,
  IconChevronDown,
  IconChevronUp,
  IconPlus,
  IconTrash2,
  IconX,
} from '@/components/ui/icons';
import { getPayloadReplaceValidationError } from '@/hooks/useVisualConfig';
import {
  isPayloadReplaceRole,
  makeClientId,
  PAYLOAD_REPLACE_ROLES,
  type PayloadReplaceRule,
} from '@/types/visualConfig';
import { PayloadModelsEditor } from './PayloadModelsEditor';
import { StringListEditor } from './StringListEditor';
import blockStyles from './Blocks.module.scss';
import styles from './PayloadReplaceRulesEditor.module.scss';

type Direction = -1 | 1;

function newRule(id = makeClientId()): PayloadReplaceRule {
  return {
    id,
    models: [],
    target: { role: 'user', all: false, positions: '1' },
    replacements: [{ id: makeClientId(), from: '', to: '' }],
  };
}

function move<T>(items: T[], index: number, direction: Direction): T[] {
  const next = [...items];
  const destination = index + direction;
  if (destination < 0 || destination >= items.length) return next;
  [next[index], next[destination]] = [next[destination], next[index]];
  return next;
}

/** Order + remove cluster. Icon-only at rest so the content, not the chrome, carries the row. */
function RowTools({
  id,
  index,
  length,
  moveDisabled,
  removeDisabled,
  labels,
  removeIcon,
  onMove,
  onRemove,
}: {
  id: string;
  index: number;
  length: number;
  moveDisabled?: boolean;
  removeDisabled?: boolean;
  labels: { up: string; down: string; remove: string };
  removeIcon: 'trash' | 'x';
  onMove: (direction: Direction) => void;
  onRemove: () => void;
}) {
  const RemoveIcon = removeIcon === 'trash' ? IconTrash2 : IconX;
  return (
    <div className={styles.tools}>
      <button
        type="button"
        className={styles.tool}
        data-move={`${id}:-1`}
        aria-label={labels.up}
        title={labels.up}
        disabled={moveDisabled || index === 0}
        onClick={() => onMove(-1)}
      >
        <IconChevronUp size={15} aria-hidden="true" />
      </button>
      <button
        type="button"
        className={styles.tool}
        data-move={`${id}:1`}
        aria-label={labels.down}
        title={labels.down}
        disabled={moveDisabled || index === length - 1}
        onClick={() => onMove(1)}
      >
        <IconChevronDown size={15} aria-hidden="true" />
      </button>
      <button
        type="button"
        className={`${styles.tool} ${styles.toolRemove}`}
        aria-label={labels.remove}
        title={labels.remove}
        disabled={removeDisabled}
        onClick={onRemove}
      >
        <RemoveIcon size={15} aria-hidden="true" />
      </button>
    </div>
  );
}

export const PayloadReplaceRulesEditor = memo(function PayloadReplaceRulesEditor({
  value,
  disabled,
  onChange,
}: {
  value: PayloadReplaceRule[];
  disabled?: boolean;
  onChange: (next: PayloadReplaceRule[]) => void;
}) {
  const { t } = useTranslation();
  const groupName = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const pendingFocus = useRef<{ id: string; direction: Direction } | null>(null);
  const copy = (key: string) => t(`config_management.visual.payload_replace.${key}`);
  const toolLabels = {
    up: copy('move_up'),
    down: copy('move_down'),
    remove: t('config_management.visual.common.delete'),
  };
  const targetOptions = [
    ...PAYLOAD_REPLACE_ROLES.map((role) => ({ value: role, label: role })),
    { value: 'paths', label: copy('target_paths') },
  ];
  const replacementTypes = [
    { value: 'literal', label: copy('literal') },
    { value: 'regex', label: copy('regex') },
  ];
  const update = (index: number, patch: Partial<PayloadReplaceRule>) =>
    onChange(value.map((rule, i) => (i === index ? { ...rule, ...patch } : rule)));

  // Reordering moves the DOM node and drops focus. Put it back on the same control (or its
  // opposite once the item hits an edge) so repeated keyboard moves need no re-navigation.
  useEffect(() => {
    const pending = pendingFocus.current;
    if (!pending) return;
    pendingFocus.current = null;
    const buttons = Array.from(
      rootRef.current?.querySelectorAll<HTMLButtonElement>('[data-move]') ?? []
    );
    const find = (direction: Direction) =>
      buttons.find((button) => button.dataset.move === `${pending.id}:${direction}`);
    const preferred = find(pending.direction);
    const target =
      preferred && !preferred.disabled ? preferred : find(-pending.direction as Direction);
    if (target && !target.disabled) target.focus();
  });

  return (
    <div ref={rootRef} className={styles.editor}>
      <p className={styles.lede}>{copy('description')}</p>

      {value.length === 0 ? (
        <div className={blockStyles.emptyState}>
          {t('config_management.visual.payload_rules.no_rules')}
        </div>
      ) : (
        <ol className={styles.rules}>
          {value.map((rule, ruleIndex) => {
            const error = getPayloadReplaceValidationError(rule);
            // Malformed source may contain data the visual draft cannot represent. Only an
            // explicit reset may discard it; unrelated edits must retain the invalid marker.
            const pathMode = rule.target.paths !== undefined;
            const needsReset =
              Boolean(rule.invalid) || (!pathMode && !isPayloadReplaceRole(rule.target.role));
            const locked = disabled || needsReset;
            const radioName = `${groupName}-${rule.id}`;
            const setAll = (all: boolean) =>
              update(ruleIndex, { target: { ...rule.target, all, positions: all ? '' : '1' } });

            return (
              <li key={rule.id} className={styles.rule} data-invalid={error ? 'true' : undefined}>
                <header className={styles.ruleHead}>
                  <h4 className={styles.ruleTitle}>
                    {t('config_management.visual.payload_rules.rule')}{' '}
                    <span className={styles.ruleNumber}>{ruleIndex + 1}</span>
                  </h4>
                  <RowTools
                    id={rule.id}
                    index={ruleIndex}
                    length={value.length}
                    moveDisabled={disabled}
                    removeDisabled={disabled}
                    labels={toolLabels}
                    removeIcon="trash"
                    onMove={(direction) => {
                      pendingFocus.current = { id: rule.id, direction };
                      onChange(move(value, ruleIndex, direction));
                    }}
                    onRemove={() => onChange(value.filter((_, i) => i !== ruleIndex))}
                  />
                </header>

                {error && (
                  <div className={styles.notice}>
                    <p className={styles.noticeMessage} role="alert">
                      <IconAlertTriangle size={14} aria-hidden="true" />
                      <span>{t(`config_management.visual.validation.${error}`)}</span>
                    </p>
                    {needsReset && (
                      <div className={styles.noticeRecovery}>
                        <p>{copy('invalid_hint')}</p>
                        <Button
                          variant="secondary"
                          size="sm"
                          disabled={disabled}
                          onClick={() =>
                            onChange(
                              value.map((entry, i) => (i === ruleIndex ? newRule(rule.id) : entry))
                            )
                          }
                        >
                          {copy('reset_invalid')}
                        </Button>
                      </div>
                    )}
                  </div>
                )}

                <section className={styles.band}>
                  <PayloadModelsEditor
                    value={rule.models}
                    disabled={locked}
                    onChange={(models) => update(ruleIndex, { models })}
                  />
                </section>

                <section className={styles.band}>
                  <div className={styles.bandLabel}>{copy('target')}</div>
                  <Select
                    className={styles.targetChoice}
                    fullWidth={false}
                    dropdownMinWidth={180}
                    value={pathMode ? 'paths' : rule.target.role}
                    options={targetOptions}
                    placeholder={rule.target.role || '—'}
                    ariaLabel={copy('target_selector')}
                    disabled={locked}
                    onChange={(role) => {
                      const target =
                        role === 'paths'
                          ? { role: '', all: false, positions: '', paths: [''] }
                          : pathMode
                            ? { role, all: false, positions: '1' }
                            : { ...rule.target, role };
                      update(ruleIndex, { target });
                    }}
                  />
                  {pathMode ? (
                    <>
                      <StringListEditor
                        value={rule.target.paths ?? []}
                        inputAriaLabel={copy('target_paths')}
                        placeholder="metadata.description"
                        disabled={locked}
                        onChange={(paths) =>
                          update(ruleIndex, { target: { ...rule.target, paths } })
                        }
                      />
                      <p className={styles.hint}>{copy('paths_hint')}</p>
                    </>
                  ) : (
                    <>
                      <div className={styles.target}>
                        <div
                          role="radiogroup"
                          aria-label={copy('target')}
                          className={styles.segmented}
                          data-side={rule.target.all ? 'start' : 'end'}
                          data-disabled={locked ? 'true' : undefined}
                        >
                          <span className={styles.segmentedThumb} aria-hidden="true" />
                          <label className={styles.segment}>
                            <input
                              type="radio"
                              name={radioName}
                              checked={rule.target.all}
                              disabled={locked}
                              onChange={() => setAll(true)}
                            />
                            <span>{copy('target_all')}</span>
                          </label>
                          <label className={styles.segment}>
                            <input
                              type="radio"
                              name={radioName}
                              checked={!rule.target.all}
                              disabled={locked}
                              onChange={() => setAll(false)}
                            />
                            <span>{copy('target_positions')}</span>
                          </label>
                        </div>
                        {!rule.target.all && (
                          <input
                            type="text"
                            className={`input ${styles.positions}`}
                            value={rule.target.positions}
                            placeholder="1, 3"
                            aria-label={copy('target_positions')}
                            aria-describedby={`${radioName}-hint`}
                            autoComplete="off"
                            spellCheck={false}
                            disabled={locked}
                            onChange={(event) =>
                              update(ruleIndex, {
                                target: {
                                  ...rule.target,
                                  all: false,
                                  positions: event.target.value,
                                },
                              })
                            }
                          />
                        )}
                      </div>
                      {!rule.target.all && (
                        <p id={`${radioName}-hint`} className={styles.hint}>
                          {copy('target_hint')}
                        </p>
                      )}
                    </>
                  )}
                </section>

                <section className={styles.band}>
                  <div className={styles.bandLabel}>
                    {copy('replacements')}
                    <span className={styles.count}>{rule.replacements.length}</span>
                  </div>
                  {rule.replacements.length > 0 && (
                    <div className={styles.pairs}>
                      <div className={styles.pairsHead} aria-hidden="true">
                        <span />
                        <span>{copy('from_or_pattern')}</span>
                        <span />
                        <span>{copy('to')}</span>
                        <span />
                      </div>
                      <ol className={styles.pairList}>
                        {rule.replacements.map((entry, index) => (
                          <li key={entry.id} className={styles.pair}>
                            <div className={styles.pairIndex}>
                              <span aria-hidden="true">{index + 1}</span>
                              <Select
                                size="sm"
                                fullWidth={false}
                                dropdownMinWidth={200}
                                ariaLabel={copy('replacement_type')}
                                value={entry.regex ? 'regex' : 'literal'}
                                options={replacementTypes}
                                disabled={locked}
                                onChange={(type) => {
                                  const regex = type === 'regex' ? true : undefined;
                                  update(ruleIndex, {
                                    replacements: rule.replacements.map((item, i) =>
                                      i === index ? { ...item, regex } : item
                                    ),
                                  });
                                }}
                              />
                            </div>
                            {(['from', 'to'] as const).map((key) => (
                              <label key={key} className={styles.operand} data-operand={key}>
                                <span className={styles.operandCaption} aria-hidden="true">
                                  {copy(key === 'from' && entry.regex ? 'pattern' : key)}
                                </span>
                                <textarea
                                  className={`input ${styles.literal}`}
                                  rows={1}
                                  value={entry[key]}
                                  aria-label={copy(key === 'from' && entry.regex ? 'pattern' : key)}
                                  disabled={locked}
                                  spellCheck={false}
                                  autoComplete="off"
                                  onChange={(event) =>
                                    update(ruleIndex, {
                                      replacements: rule.replacements.map((item, i) =>
                                        i === index ? { ...item, [key]: event.target.value } : item
                                      ),
                                    })
                                  }
                                />
                              </label>
                            ))}
                            <svg
                              className={styles.arrow}
                              width="14"
                              height="14"
                              viewBox="0 0 24 24"
                              fill="none"
                              stroke="currentColor"
                              strokeWidth="2"
                              strokeLinecap="round"
                              strokeLinejoin="round"
                              aria-hidden="true"
                            >
                              <path d="M5 12h14" />
                              <path d="m13 6 6 6-6 6" />
                            </svg>
                            <RowTools
                              id={entry.id}
                              index={index}
                              length={rule.replacements.length}
                              moveDisabled={locked}
                              removeDisabled={locked}
                              labels={toolLabels}
                              removeIcon="x"
                              onMove={(direction) => {
                                pendingFocus.current = { id: entry.id, direction };
                                update(ruleIndex, {
                                  replacements: move(rule.replacements, index, direction),
                                });
                              }}
                              onRemove={() =>
                                update(ruleIndex, {
                                  replacements: rule.replacements.filter((_, i) => i !== index),
                                })
                              }
                            />
                          </li>
                        ))}
                      </ol>
                    </div>
                  )}
                  <button
                    type="button"
                    className={styles.add}
                    disabled={locked}
                    onClick={() =>
                      update(ruleIndex, {
                        replacements: [
                          ...rule.replacements,
                          { id: makeClientId(), from: '', to: '' },
                        ],
                      })
                    }
                  >
                    <IconPlus size={14} aria-hidden="true" />
                    <span>{copy('add_replacement')}</span>
                  </button>
                </section>
              </li>
            );
          })}
        </ol>
      )}

      <div className={blockStyles.actionRow}>
        <Button
          variant="secondary"
          size="sm"
          disabled={disabled}
          onClick={() => onChange([...value, newRule()])}
        >
          {t('config_management.visual.payload_rules.add_rule')}
        </Button>
      </div>
    </div>
  );
});
