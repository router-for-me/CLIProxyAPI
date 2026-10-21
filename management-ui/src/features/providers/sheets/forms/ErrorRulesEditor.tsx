import { useEffect, useId, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronDown, IconChevronUp, IconPlus, IconTrash2 } from '@/components/ui/icons';
import { Select } from '@/components/ui/Select';
import {
  ERROR_RULE_ACTIONS,
  createErrorMatch,
  createErrorRule,
  isInvalidErrorMatch,
  moveErrorRule,
  validateErrorRule,
  type ErrorRuleDraft,
} from '../../errorRules';
import form from './sharedForm.module.scss';
import styles from './ErrorRulesEditor.module.scss';

export interface ErrorRulesEditorProps {
  rules: ErrorRuleDraft[];
  onChange: (rules: ErrorRuleDraft[]) => void;
  disabled: boolean;
}

const actions: readonly string[] = ERROR_RULE_ACTIONS;
const key = 'providersPage.errorRules';

export function ErrorRulesEditor({ rules, onChange, disabled }: ErrorRulesEditorProps) {
  const { t } = useTranslation();
  const id = useId();
  const counter = useRef(0);
  const pendingFocus = useRef<string | null>(null);
  const root = useRef<HTMLDivElement>(null);
  const [touched, setTouched] = useState<Set<string>>(() => new Set());
  const nextId = () => `${id}-draft-${++counter.current}`;
  const statusId = (ruleId: string) => `${id}-${ruleId}-status`;
  const matchId = (match: string) => `${id}-${match}-value`;

  useEffect(() => {
    if (!pendingFocus.current) return;
    const target = document.getElementById(pendingFocus.current);
    if (target && root.current?.contains(target)) target.focus();
    pendingFocus.current = null;
  }, [rules]);

  const update = (rule: ErrorRuleDraft) => {
    onChange(rules.map((item) => (item.id === rule.id ? rule : item)));
  };

  return (
    <div ref={root} className={`${form.section} ${styles.editor}`}>
      <p className={form.sectionDesc}>{t(`${key}.hint`)}</p>
      {rules.length === 0 && <p className={form.sectionDesc}>{t(`${key}.empty`)}</p>}
      {rules.map((rule, index) => {
        const error = touched.has(rule.id) ? validateErrorRule(rule) : null;
        const errorId = `${id}-${rule.id}-error`;
        const statusError = error === `${key}.invalidStatus`;
        const actionError = error === `${key}.invalidAction`;
        const matchError = error === `${key}.invalidMatch`;
        return (
          <div
            key={rule.id}
            role="group"
            aria-labelledby={`${id}-${rule.id}-title`}
            aria-describedby={error ? errorId : undefined}
            className={form.entryCard}
            onBlur={() => setTouched((previous) => new Set(previous).add(rule.id))}
          >
            <div className={form.entryCardHeader}>
              <span id={`${id}-${rule.id}-title`}>{t(`${key}.rule`, { number: index + 1 })}</span>
              <div className={form.entryCardHeaderRight}>
                {([-1, 1] as const).map((direction) => (
                  <button
                    key={direction}
                    type="button"
                    className={form.entryCardIconBtn}
                    disabled={
                      disabled || (direction === -1 ? index === 0 : index === rules.length - 1)
                    }
                    aria-label={t(`${key}.${direction === -1 ? 'moveUp' : 'moveDown'}`)}
                    title={t(`${key}.${direction === -1 ? 'moveUp' : 'moveDown'}`)}
                    onClick={() => {
                      pendingFocus.current = statusId(rule.id);
                      onChange(moveErrorRule(rules, rule.id, direction));
                    }}
                  >
                    {direction === -1 ? <IconChevronUp size={16} /> : <IconChevronDown size={16} />}
                  </button>
                ))}
                <button
                  type="button"
                  className={form.removeBtn}
                  disabled={disabled}
                  aria-label={t(`${key}.removeRule`)}
                  title={t(`${key}.removeRule`)}
                  onClick={() => {
                    const neighbor = rules[index + 1] ?? rules[index - 1];
                    pendingFocus.current = neighbor ? statusId(neighbor.id) : `${id}-add-rule`;
                    onChange(rules.filter((item) => item.id !== rule.id));
                  }}
                >
                  <IconTrash2 size={16} />
                </button>
              </div>
            </div>
            <div className={styles.ruleFields}>
              <div className={form.field}>
                <label htmlFor={statusId(rule.id)} className={form.label}>
                  {t(`${key}.status`)}
                </label>
                <input
                  id={statusId(rule.id)}
                  className={form.input}
                  type="text"
                  inputMode="numeric"
                  value={rule.status}
                  disabled={disabled}
                  aria-invalid={statusError || undefined}
                  aria-describedby={statusError ? errorId : undefined}
                  onChange={(event) => update({ ...rule, status: event.target.value })}
                />
              </div>
              <div className={form.field}>
                <label htmlFor={`${id}-${rule.id}-action`} className={form.label}>
                  {t(`${key}.action`)}
                </label>
                <Select
                  id={`${id}-${rule.id}-action`}
                  ariaLabel={t(`${key}.action`)}
                  value={rule.action}
                  disabled={disabled}
                  ariaInvalid={actionError || undefined}
                  ariaDescribedBy={actionError ? errorId : undefined}
                  options={[
                    { value: '', label: t(`${key}.selectAction`) },
                    ...(rule.action && !actions.includes(rule.action)
                      ? [{ value: rule.action, label: rule.action }]
                      : []),
                    ...actions.map((action) => ({
                      value: action,
                      label: t(`${key}.actions.${action}`),
                    })),
                  ]}
                  onChange={(action) => update({ ...rule, action })}
                />
              </div>
            </div>
            {rule.matches.map((match, matchIndex) => (
              <div key={match.id} className={styles.matchRow}>
                <div className={form.field}>
                  <label htmlFor={`${id}-${match.id}-kind`} className={form.label}>
                    {t(`${key}.matchType`)}
                  </label>
                  <Select
                    id={`${id}-${match.id}-kind`}
                    ariaLabel={t(`${key}.matchType`)}
                    value={match.kind}
                    disabled={disabled}
                    options={[
                      { value: 'text', label: t(`${key}.text`) },
                      { value: 'regex', label: t(`${key}.regex`) },
                    ]}
                    onChange={(kind) =>
                      update({
                        ...rule,
                        matches: rule.matches.map((item) =>
                          item.id === match.id ? { ...item, kind: kind as 'text' | 'regex' } : item
                        ),
                      })
                    }
                  />
                </div>
                <div className={form.field}>
                  <label htmlFor={matchId(match.id)} className={form.label}>
                    {t(`${key}.matchValue`)}
                  </label>
                  <textarea
                    id={matchId(match.id)}
                    className={`${form.input} ${styles.matchInput}`}
                    rows={1}
                    value={match.value}
                    disabled={disabled}
                    spellCheck={false}
                    aria-invalid={(matchError && isInvalidErrorMatch(rule, match)) || undefined}
                    aria-describedby={
                      [
                        match.kind === 'regex' ? `${id}-${match.id}-hint` : '',
                        matchError && isInvalidErrorMatch(rule, match) ? errorId : '',
                      ]
                        .filter(Boolean)
                        .join(' ') || undefined
                    }
                    onChange={(event) =>
                      update({
                        ...rule,
                        matches: rule.matches.map((item) =>
                          item.id === match.id ? { ...item, value: event.target.value } : item
                        ),
                      })
                    }
                  />
                  {match.kind === 'regex' && (
                    <span id={`${id}-${match.id}-hint`} className={form.labelHint}>
                      {t(`${key}.regexHint`)}
                    </span>
                  )}
                </div>
                <button
                  type="button"
                  className={`${form.removeBtn} ${styles.removeMatch}`}
                  disabled={disabled}
                  aria-label={t(`${key}.removeMatch`)}
                  title={t(`${key}.removeMatch`)}
                  onClick={() => {
                    const neighbor = rule.matches[matchIndex + 1] ?? rule.matches[matchIndex - 1];
                    pendingFocus.current = neighbor
                      ? matchId(neighbor.id)
                      : `${id}-${rule.id}-add-match`;
                    update({
                      ...rule,
                      matches: rule.matches.filter((item) => item.id !== match.id),
                    });
                  }}
                >
                  <IconTrash2 size={16} />
                </button>
              </div>
            ))}
            <button
              id={`${id}-${rule.id}-add-match`}
              type="button"
              className={form.addBtn}
              disabled={disabled}
              onClick={() => {
                const match = createErrorMatch(nextId());
                pendingFocus.current = matchId(match.id);
                update({ ...rule, matches: [...rule.matches, match] });
              }}
            >
              <IconPlus size={14} />
              {t(`${key}.addMatch`)}
            </button>
            {error && (
              <div id={errorId} className={form.errorBox} role="status">
                {t(error)}
              </div>
            )}
          </div>
        );
      })}
      <button
        id={`${id}-add-rule`}
        type="button"
        className={form.addBtn}
        disabled={disabled}
        onClick={() => {
          const rule = createErrorRule(nextId());
          pendingFocus.current = statusId(rule.id);
          onChange([...rules, rule]);
        }}
      >
        <IconPlus size={14} />
        {t(`${key}.addRule`)}
      </button>
    </div>
  );
}
