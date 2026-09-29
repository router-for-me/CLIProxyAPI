import type { ReactNode } from 'react';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { configFieldDomId } from '../../searchIndex';
import styles from './Field.module.scss';

/** 搜索跳转的脉冲高亮 class（useFieldJump 命令式挂载/移除）。 */
export const FIELD_HIGHLIGHT_CLASS: string = styles.fieldHighlightActive;

/**
 * 表单控件宿主 class：收编旧 VisualConfigEditor 的 :global(.form-group/.input/...)
 * 覆盖的作用域根。SectionCard 的内容区自动挂载；脱离卡片渲染表单块（如 Modal 内容）时手动挂。
 */
export const FIELDS_ROOT_CLASS: string = styles.fieldsRoot;

export type ToggleRowProps = {
  title: string;
  description?: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
};

export function ToggleRow({ title, description, checked, disabled, onChange }: ToggleRowProps) {
  return (
    <div className={styles.toggleRow}>
      <div className={styles.toggleCopy}>
        <div className={styles.toggleTitle}>{title}</div>
        {description ? <div className={styles.toggleDescription}>{description}</div> : null}
      </div>
      <ToggleSwitch checked={checked} onChange={onChange} disabled={disabled} ariaLabel={title} />
    </div>
  );
}

export function FieldGrid({ children }: { children: ReactNode }) {
  return <div className={styles.fieldGrid}>{children}</div>;
}

export function FieldStack({ children }: { children: ReactNode }) {
  return <div className={styles.fieldStack}>{children}</div>;
}

export function Divider() {
  return <div className={styles.divider} />;
}

// Stable, stateless anchor around a searchable field. Search jumps target its DOM id
// (see searchIndex.ts) and the highlight pulse is applied to it imperatively.
// `wide` 让字段在 FieldGrid 里跨两列（如长文本的代理 URL）。
export function FieldAnchor({
  fieldId,
  wide = false,
  children,
}: {
  fieldId: string;
  wide?: boolean;
  children: ReactNode;
}) {
  return (
    <div
      id={configFieldDomId(fieldId)}
      className={`${styles.fieldAnchor} ${wide ? styles.fieldAnchorWide : ''}`}
    >
      {children}
    </div>
  );
}

/** 带描边容器的字段组（原 SectionSubsection / .subsection）。title 可省略只留容器。 */
export function FieldGroup({
  title,
  description,
  children,
}: {
  title?: string;
  description?: string;
  children: ReactNode;
}) {
  return (
    <div className={styles.group}>
      {title ? (
        <div className={styles.groupHeader}>
          <h3 className={styles.groupTitle}>{title}</h3>
          {description ? <p className={styles.groupDescription}>{description}</p> : null}
        </div>
      ) : null}
      {children}
    </div>
  );
}

/** 独立的小组标题行（如 Claude / Codex 请求头小节标题）。 */
export function FieldGroupHeading({ title }: { title: string }) {
  return (
    <div className={styles.groupHeader}>
      <h3 className={styles.groupTitle}>{title}</h3>
    </div>
  );
}

export function FieldShell({
  label,
  labelId,
  htmlFor,
  hint,
  hintId,
  error,
  errorId,
  children,
}: {
  label: string;
  labelId?: string;
  htmlFor?: string;
  hint?: string;
  hintId?: string;
  error?: string;
  errorId?: string;
  children: ReactNode;
}) {
  return (
    <div className={styles.fieldShell}>
      <label id={labelId} htmlFor={htmlFor} className={styles.fieldLabel}>
        {label}
      </label>
      {children}
      {error ? (
        <div id={errorId} className="error-box">
          {error}
        </div>
      ) : null}
      {hint ? (
        <div id={hintId} className={styles.fieldHint}>
          {hint}
        </div>
      ) : null}
    </div>
  );
}

/** 独立的字段提示行（FieldShell 之外的裸 hint）。 */
export function FieldHint({ id, children }: { id?: string; children: ReactNode }) {
  return (
    <div id={id} className={styles.fieldHint}>
      {children}
    </div>
  );
}

/** 数字输入右侧的「已禁用」pill 宿主（流式 keepalive 的 0/空 提示）。 */
export function FieldControl({ children }: { children: ReactNode }) {
  return <div className={styles.fieldControl}>{children}</div>;
}

/** FieldControl 内的内联 pill。 */
export function InlinePill({ children }: { children: ReactNode }) {
  return <span className={styles.inlinePill}>{children}</span>;
}
