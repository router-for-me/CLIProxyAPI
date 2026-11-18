import { useEffect, useId, useLayoutEffect, useRef, useState } from 'react';
import { useTranslation } from 'react-i18next';
import { IconChevronDown, IconChevronUp, IconPlus, IconTrash2 } from '@/components/ui/icons';
import { prefersReducedMotion } from '@/hooks/motion';
import { serializeLiteralDraft } from './sensitiveReplacementDraft';
import styles from './SensitiveReplacementEditor.module.scss';

type Pattern = { pattern: string; replacement: string };
/** `fresh`: created by the add button in this session; only these rows play the enter motion. */
type LiteralRow = { id: string; key: string; value: string; fresh?: boolean };
type PatternRow = Pattern & { id: string; fresh?: boolean };

export interface HostStyleProps {
  disabled?: boolean;
  /** id(s) of host-rendered hint text; attached to every match textarea's aria-describedby */
  describedBy?: string;
  /** class for the textareas. Default 'input' (global class, restyled by the config page's fieldsRoot) */
  inputClassName?: string;
  /** class for the add button. Default 'btn btn-secondary btn-sm' */
  addButtonClassName?: string;
  /** render a 14px IconPlus before the add label. Default false */
  addButtonIcon?: boolean;
}

export interface LiteralReplacementEditorProps extends HostStyleProps {
  value: Record<string, string>;
  onChange(value: Record<string, string>): void;
}

export interface PatternReplacementEditorProps extends HostStyleProps {
  value: Pattern[];
  onChange(value: Pattern[]): void;
}

const cx = (...classes: Array<string | false | undefined>) => classes.filter(Boolean).join(' ');

const wordSignature = (words: Record<string, string>) =>
  JSON.stringify(Object.entries(words).sort(([a], [b]) => a.localeCompare(b)));

const toLiteralRows = (words: Record<string, string>): LiteralRow[] =>
  Object.entries(words).map(([key, value], index) => ({ id: `l${index}`, key, value }));

const toPatternRows = (patterns: Pattern[]): PatternRow[] =>
  patterns.map((rule, index) => ({ ...rule, id: `p${index}` }));

/* ---------- Shared list view: both editors render rows through this ---------- */

type Kind = 'literal' | 'pattern';
type ViewRow = { id: string; match: string; replacement: string; fresh?: boolean };
type Field = 'match' | 'replacement';
type Direction = -1 | 1;

const COPY: Record<
  Kind,
  {
    header: string;
    matchRow: string;
    valueRow: string;
    remove: string;
    add: string;
    empty: string;
    placeholder: string;
  }
> = {
  literal: {
    header: 'literal_key',
    matchRow: 'literal_key_row',
    valueRow: 'literal_value_row',
    remove: 'remove_literal',
    add: 'add_literal',
    empty: 'empty_literal',
    placeholder: 'Claude Code',
  },
  pattern: {
    header: 'pattern',
    matchRow: 'pattern_row',
    valueRow: 'pattern_value_row',
    remove: 'remove_pattern',
    add: 'add_pattern',
    empty: 'empty_pattern',
    placeholder: '(?m)^You are omp[^\\n]*\\n?',
  },
};

const FLIP_DURATION = 200;
const FLIP_EASING = 'cubic-bezier(0.23, 1, 0.32, 1)';

type RowIds = {
  row: (id: string) => string;
  match: (id: string) => string;
  error: (id: string) => string;
  move: (id: string, direction: Direction) => string;
};

interface RuleRowProps {
  kind: Kind;
  row: ViewRow;
  index: number;
  count: number;
  ids: RowIds;
  invalid: boolean;
  ordered: boolean;
  disabled: boolean;
  describedBy?: string;
  inputClassName: string;
  onEdit(id: string, field: Field, value: string): void;
  onRemove(index: number): void;
  onMove(index: number, direction: Direction): void;
}

/** One rule on one line: `[#] match → replacement [actions]`, plus an optional error line. */
function RuleRow({
  kind,
  row,
  index,
  count,
  ids,
  invalid,
  ordered,
  disabled,
  describedBy,
  inputClassName,
  onEdit,
  onRemove,
  onMove,
}: RuleRowProps) {
  const { t } = useTranslation();
  const copy = COPY[kind];
  const position = index + 1;
  const text = (key: string) => t(`sensitive_replacements.${key}`, { row: position });
  const field = cx(inputClassName, styles.codeInput);
  const removeLabel = text(copy.remove);

  return (
    <div
      id={ids.row(row.id)}
      className={cx(styles.row, invalid && styles.rowInvalid)}
      data-new={row.fresh ? '' : undefined}
    >
      {ordered ? (
        <span className={cx(styles.cellIndex, styles.index)} aria-hidden="true">
          {position}
        </span>
      ) : null}
      <textarea
        id={ids.match(row.id)}
        className={cx(field, styles.cellMatch)}
        rows={1}
        spellCheck={false}
        disabled={disabled}
        aria-label={text(copy.matchRow)}
        aria-invalid={invalid || undefined}
        aria-describedby={cx(describedBy, invalid && ids.error(row.id)) || undefined}
        placeholder={copy.placeholder}
        value={row.match}
        onChange={(event) => onEdit(row.id, 'match', event.target.value)}
      />
      <span className={cx(styles.cellArrow, styles.arrow)} aria-hidden="true">
        <span className={styles.arrowWide}>→</span>
        <span className={styles.arrowNarrow}>↳</span>
      </span>
      <textarea
        className={cx(field, styles.cellValue)}
        rows={1}
        spellCheck={false}
        disabled={disabled}
        aria-label={text(copy.valueRow)}
        placeholder={text('replacement_placeholder')}
        value={row.replacement}
        onChange={(event) => onEdit(row.id, 'replacement', event.target.value)}
      />
      <div className={cx(styles.cellActions, styles.actions)}>
        {ordered
          ? ([-1, 1] as const).map((direction) => {
              const label = text(direction === -1 ? 'move_up' : 'move_down');
              const atEnd = direction === -1 ? index === 0 : index === count - 1;
              return (
                <button
                  key={direction}
                  type="button"
                  id={ids.move(row.id, direction)}
                  className={styles.iconBtn}
                  disabled={disabled || atEnd}
                  aria-label={label}
                  title={label}
                  onClick={() => onMove(index, direction)}
                >
                  {direction === -1 ? <IconChevronUp size={16} /> : <IconChevronDown size={16} />}
                </button>
              );
            })
          : null}
        <button
          type="button"
          className={cx(styles.iconBtn, styles.deleteBtn)}
          disabled={disabled}
          aria-label={removeLabel}
          title={removeLabel}
          onClick={() => onRemove(index)}
        >
          <IconTrash2 size={16} />
        </button>
      </div>
      {invalid ? (
        <p id={ids.error(row.id)} className={cx(styles.cellError, styles.error)} role="status">
          {text('duplicate_key')}
        </p>
      ) : null}
    </div>
  );
}

interface RuleListProps extends HostStyleProps {
  kind: Kind;
  rows: ViewRow[];
  /** Ids of rows whose match collides with another row's. */
  invalidIds?: ReadonlySet<string>;
  onEdit(id: string, field: Field, value: string): void;
  /** Appends an empty row and returns its id. */
  onAdd(): string;
  onRemove(id: string): void;
  /** Only ordered lists (regex) pass this; order is meaningless for the literal map. */
  onMove?(index: number, direction: Direction): void;
}

function RuleList({
  kind,
  rows,
  invalidIds,
  onEdit,
  onAdd,
  onRemove,
  onMove,
  disabled = false,
  describedBy,
  inputClassName = 'input',
  addButtonClassName = 'btn btn-secondary btn-sm',
  addButtonIcon = false,
}: RuleListProps) {
  const { t } = useTranslation();
  const uid = useId();
  const root = useRef<HTMLDivElement>(null);
  /** Candidate ids; the first one that exists and is enabled gets focus. */
  const pendingFocus = useRef<string[] | null>(null);
  /** Rows swapped by the last move and their top edge before it, for the FLIP. */
  const flip = useRef<{ id: string; top: number }[] | null>(null);
  const copy = COPY[kind];
  const ordered = Boolean(onMove);
  const addId = `${uid}-add`;
  const ids: RowIds = {
    row: (id) => `${uid}-${id}-row`,
    match: (id) => `${uid}-${id}-match`,
    error: (id) => `${uid}-${id}-error`,
    move: (id, direction) => `${uid}-${id}-${direction === -1 ? 'up' : 'down'}`,
  };

  // Runs after every commit; both refs are only armed by the handlers below.
  useLayoutEffect(() => {
    const moved = flip.current;
    flip.current = null;
    if (!moved || prefersReducedMotion()) return;
    for (const { id, top } of moved) {
      const element = document.getElementById(`${uid}-${id}-row`);
      if (!element || typeof element.animate !== 'function') continue;
      const delta = top - element.getBoundingClientRect().top;
      if (!delta) continue;
      element.animate([{ transform: `translateY(${delta}px)` }, { transform: 'none' }], {
        duration: FLIP_DURATION,
        easing: FLIP_EASING,
      });
    }
  });

  // React re-inserting a keyed node drops focus, so it is restored explicitly.
  useEffect(() => {
    const candidates = pendingFocus.current;
    pendingFocus.current = null;
    if (!candidates) return;
    for (const id of candidates) {
      const target = document.getElementById(id);
      if (!target || !root.current?.contains(target)) continue;
      if (target instanceof HTMLButtonElement && target.disabled) continue;
      target.focus();
      return;
    }
  });

  const handleMove = (index: number, direction: Direction) => {
    const row = rows[index];
    flip.current = [row, rows[index + direction]].map((item) => ({
      id: item.id,
      top: document.getElementById(ids.row(item.id))?.getBoundingClientRect().top ?? 0,
    }));
    // Same button so repeated presses keep moving the rule; at an end it is disabled,
    // so the row's other move button.
    pendingFocus.current = [ids.move(row.id, direction), ids.move(row.id, -direction as Direction)];
    onMove?.(index, direction);
  };

  const handleRemove = (index: number) => {
    const neighbor = rows[index + 1] ?? rows[index - 1];
    pendingFocus.current = [neighbor ? ids.match(neighbor.id) : addId];
    onRemove(rows[index].id);
  };

  const handleAdd = () => {
    pendingFocus.current = [ids.match(onAdd())];
  };

  return (
    <div ref={root} className={cx(styles.editor, ordered ? styles.ordered : styles.unordered)}>
      {rows.length === 0 ? (
        <p className={styles.empty}>{t(`sensitive_replacements.${copy.empty}`)}</p>
      ) : (
        <div className={styles.list}>
          <div className={styles.header} aria-hidden="true">
            <span className={styles.cellMatch}>{t(`sensitive_replacements.${copy.header}`)}</span>
            <span className={styles.cellValue}>{t('sensitive_replacements.replacement')}</span>
          </div>
          {rows.map((row, index) => (
            <RuleRow
              key={row.id}
              kind={kind}
              row={row}
              index={index}
              count={rows.length}
              ids={ids}
              invalid={invalidIds?.has(row.id) ?? false}
              ordered={ordered}
              disabled={disabled}
              describedBy={describedBy}
              inputClassName={inputClassName}
              onEdit={onEdit}
              onRemove={handleRemove}
              onMove={handleMove}
            />
          ))}
        </div>
      )}
      <div className={styles.addRow}>
        <button
          type="button"
          id={addId}
          className={addButtonClassName}
          disabled={disabled}
          onClick={handleAdd}
        >
          {addButtonIcon ? <IconPlus size={14} /> : null}
          <span>{t(`sensitive_replacements.${copy.add}`)}</span>
        </button>
      </div>
    </div>
  );
}

/* ---------- Literal map editor ---------- */

export function LiteralReplacementEditor({
  value,
  onChange,
  ...host
}: LiteralReplacementEditorProps) {
  const sequence = useRef(0);
  const valueKey = wordSignature(value);
  const [draft, setDraft] = useState(() => ({ source: valueKey, rows: toLiteralRows(value) }));
  // Content comparison preserves drafts and DOM identity when parents clone unchanged props.
  // A genuinely external value replaces drafts (e.g. switching the edited provider).
  if (draft.source !== valueKey) {
    setDraft({ source: valueKey, rows: toLiteralRows(value) });
  }
  const update = (rows: LiteralRow[]) => {
    const next = serializeLiteralDraft(rows);
    setDraft({ source: next === null ? draft.source : wordSignature(next), rows });
    if (next !== null) onChange(next);
  };
  const invalidIds = new Set(
    draft.rows
      .filter(
        (row) =>
          row.key !== '' && draft.rows.some((other) => other.id !== row.id && other.key === row.key)
      )
      .map((row) => row.id)
  );

  return (
    <RuleList
      {...host}
      kind="literal"
      rows={draft.rows.map(({ id, key, value: replacement, fresh }) => ({
        id,
        match: key,
        replacement,
        fresh,
      }))}
      invalidIds={invalidIds}
      onEdit={(id, field, text) =>
        update(
          draft.rows.map((row) =>
            row.id === id ? { ...row, [field === 'match' ? 'key' : 'value']: text } : row
          )
        )
      }
      onAdd={() => {
        const id = `new-l${sequence.current++}`;
        update([...draft.rows, { id, key: '', value: '', fresh: true }]);
        return id;
      }}
      onRemove={(id) => update(draft.rows.filter((row) => row.id !== id))}
    />
  );
}

/* ---------- Ordered regex rule editor ---------- */

export function PatternReplacementEditor({
  value,
  onChange,
  ...host
}: PatternReplacementEditorProps) {
  const sequence = useRef(0);
  const valueKey = JSON.stringify(value);
  const [draft, setDraft] = useState(() => ({ source: valueKey, rows: toPatternRows(value) }));
  if (draft.source !== valueKey) {
    setDraft({ source: valueKey, rows: toPatternRows(value) });
  }
  const update = (rows: PatternRow[]) => {
    const next = rows.map(({ pattern, replacement }) => ({ pattern, replacement }));
    setDraft({ source: JSON.stringify(next), rows });
    onChange(next);
  };

  return (
    <RuleList
      {...host}
      kind="pattern"
      rows={draft.rows.map(({ id, pattern, replacement, fresh }) => ({
        id,
        match: pattern,
        replacement,
        fresh,
      }))}
      onEdit={(id, field, text) =>
        update(
          draft.rows.map((row) =>
            row.id === id ? { ...row, [field === 'match' ? 'pattern' : 'replacement']: text } : row
          )
        )
      }
      onAdd={() => {
        const id = `new-p${sequence.current++}`;
        update([...draft.rows, { id, pattern: '', replacement: '', fresh: true }]);
        return id;
      }}
      onRemove={(id) => update(draft.rows.filter((row) => row.id !== id))}
      onMove={(index, direction) => {
        const rows = [...draft.rows];
        // A moved node is re-inserted by React; drop `fresh` so @starting-style does not replay.
        [rows[index], rows[index + direction]] = [
          { ...rows[index + direction], fresh: false },
          { ...rows[index], fresh: false },
        ];
        update(rows);
      }}
    />
  );
}
