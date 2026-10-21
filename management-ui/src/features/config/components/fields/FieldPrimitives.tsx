import {
  Children,
  cloneElement,
  Fragment,
  isValidElement,
  useId,
  type InputHTMLAttributes,
  type ReactElement,
  type ReactNode,
} from 'react';
import { Select, type SelectOption } from '@/components/ui/Select';
import { ToggleSwitch } from '@/components/ui/ToggleSwitch';
import { configFieldDomId } from '../../searchIndex';
import { resolveRowSpans } from './rowSpans';
import styles from './Field.module.scss';

/** 搜索跳转的脉冲高亮 class（useFieldJump 命令式挂载/移除）。 */
export const FIELD_HIGHLIGHT_CLASS: string = styles.fieldHighlightActive;

/**
 * 表单控件宿主 class：区块编辑器内 :global(.input/.form-group/...) 覆盖的作用域根。
 * SectionCard 的内容区自动挂载；脱离卡片渲染表单块（如 Modal 内容）时手动挂。
 */
export const FIELDS_ROOT_CLASS: string = styles.fieldsRoot;

const cx = (...classes: Array<string | false | undefined>) => classes.filter(Boolean).join(' ');

/* ======================================================================
   设置行：开关 / 输入 / 下拉 / 区块编辑器共用同一副骨架——
   左列「标题 + 说明 + 错误」，右列控件；只有控件槽的内容不同。
   行收进 SettingList（圆角容器 + 发丝分隔线），宽容器下两列排布。
   ====================================================================== */

type SettingCopy = {
  /** 搜索锚点：行本身即锚点（DOM id 见 configFieldDomId）。 */
  fieldId: string;
  label: string;
  description?: ReactNode;
  error?: string;
  /**
   * 两列布局下独占一整行——只为语义而设（长 URL、总开关置顶）。
   * 落单的行（末尾或下一项独占）由 SettingList 自动补成整行，无需手动凑奇偶。
   */
  wide?: boolean;
};

type SettingIds = { controlId: string; labelId: string; hintId: string; errorId: string };

function useSettingIds(fieldId: string): SettingIds {
  const controlId = useId();
  return {
    controlId,
    labelId: `${controlId}-${fieldId}-label`,
    hintId: `${controlId}-hint`,
    errorId: `${controlId}-error`,
  };
}

function describedBy(ids: SettingIds, description: ReactNode, error?: string) {
  return (
    [error ? ids.errorId : '', description ? ids.hintId : ''].filter(Boolean).join(' ') || undefined
  );
}

type RowKind = 'toggle' | 'input' | 'block';
type ControlSize = 'sm' | 'md' | 'lg';

function SettingRow({
  fieldId,
  label,
  description,
  error,
  wide,
  ids,
  kind,
  size,
  labelFor,
  control,
}: SettingCopy & {
  ids: SettingIds;
  kind: RowKind;
  size?: ControlSize;
  /** 有单一控件时标题即 <label htmlFor>；区块编辑器无单一控件，标题只作分组名。 */
  labelFor?: string;
  control: ReactNode;
}) {
  return (
    <div
      id={configFieldDomId(fieldId)}
      className={cx(
        styles.row,
        kind === 'toggle' && styles.rowToggle,
        kind === 'block' && styles.rowBlock,
        size && styles[`size_${size}`],
        (wide || kind === 'block') && styles.rowWide
      )}
    >
      <div className={styles.rowCopy}>
        {labelFor ? (
          <label id={ids.labelId} htmlFor={labelFor} className={styles.rowLabel}>
            {label}
          </label>
        ) : (
          <span id={ids.labelId} className={styles.rowLabel}>
            {label}
          </span>
        )}
        {description ? (
          <div id={ids.hintId} className={styles.rowDescription}>
            {description}
          </div>
        ) : null}
        {error ? (
          <div id={ids.errorId} className={styles.rowError}>
            {error}
          </div>
        ) : null}
      </div>
      <div className={styles.rowControl}>{control}</div>
    </div>
  );
}

export function ToggleSetting({
  checked,
  disabled,
  onChange,
  ...copy
}: Omit<SettingCopy, 'error'> & {
  checked: boolean;
  disabled?: boolean;
  onChange: (value: boolean) => void;
}) {
  const ids = useSettingIds(copy.fieldId);
  return (
    <SettingRow
      {...copy}
      ids={ids}
      kind="toggle"
      labelFor={ids.controlId}
      control={
        <ToggleSwitch
          id={ids.controlId}
          checked={checked}
          disabled={disabled}
          onChange={onChange}
          ariaDescribedBy={describedBy(ids, copy.description)}
        />
      }
    />
  );
}

type NativeInputProps = Pick<
  InputHTMLAttributes<HTMLInputElement>,
  'type' | 'placeholder' | 'min' | 'max' | 'autoComplete'
>;

export function TextSetting({
  value,
  disabled,
  onChange,
  size = 'md',
  adornment,
  type = 'text',
  placeholder,
  min,
  max,
  autoComplete,
  ...copy
}: SettingCopy &
  NativeInputProps & {
    value: string;
    disabled?: boolean;
    onChange: (value: string) => void;
    /** sm = 数字 / 时长；md = 普通文本；lg = URL、路径、UA 等长文本。 */
    size?: ControlSize;
    /** 输入框内右侧的状态标记（如 keepalive 为 0 时的「已禁用」）。 */
    adornment?: ReactNode;
  }) {
  const ids = useSettingIds(copy.fieldId);
  return (
    <SettingRow
      {...copy}
      ids={ids}
      kind="input"
      size={size}
      labelFor={ids.controlId}
      control={
        <>
          <input
            id={ids.controlId}
            className={cx('input', adornment ? styles.inputWithAdornment : undefined)}
            type={type}
            value={value}
            placeholder={placeholder}
            min={min}
            max={max}
            autoComplete={autoComplete}
            spellCheck={false}
            disabled={disabled}
            aria-invalid={Boolean(copy.error)}
            aria-describedby={describedBy(ids, copy.description, copy.error)}
            onChange={(event) => onChange(event.target.value)}
          />
          {adornment ? <span className={styles.adornment}>{adornment}</span> : null}
        </>
      }
    />
  );
}

export function SelectSetting<T extends string>({
  value,
  options,
  disabled,
  onChange,
  size = 'md',
  ...copy
}: SettingCopy & {
  value: T;
  options: ReadonlyArray<SelectOption & { value: T }>;
  disabled?: boolean;
  onChange: (value: T) => void;
  size?: ControlSize;
}) {
  const ids = useSettingIds(copy.fieldId);
  return (
    <SettingRow
      {...copy}
      ids={ids}
      kind="input"
      size={size}
      labelFor={ids.controlId}
      control={
        <Select
          id={ids.controlId}
          className={styles.selectControl}
          fullWidth
          value={value}
          options={options}
          disabled={disabled}
          ariaLabelledBy={ids.labelId}
          ariaDescribedBy={describedBy(ids, copy.description, copy.error)}
          ariaInvalid={copy.error ? true : undefined}
          onChange={(next) => onChange(next as T)}
        />
      }
    />
  );
}

export type BlockSettingA11y = { describedBy?: string; invalid: boolean };

/**
 * 区块编辑器行（字符串列表、ICE 服务器等）：标题与说明在上，编辑器在下独占整行。
 * 编辑器外包 role="group"，以标题命名、以说明/错误描述；
 * children 传函数时可拿到 describedBy 自行挂到内部控件上。
 */
export function BlockSetting({
  children,
  ...copy
}: Omit<SettingCopy, 'wide'> & {
  children: ReactNode | ((a11y: BlockSettingA11y) => ReactNode);
}) {
  const ids = useSettingIds(copy.fieldId);
  const a11y = {
    describedBy: describedBy(ids, copy.description, copy.error),
    invalid: Boolean(copy.error),
  };
  return (
    <SettingRow
      {...copy}
      ids={ids}
      kind="block"
      control={
        <div
          role="group"
          aria-labelledby={ids.labelId}
          aria-describedby={a11y.describedBy}
          aria-invalid={a11y.invalid || undefined}
        >
          {typeof children === 'function' ? children(a11y) : children}
        </div>
      }
    />
  );
}

/** 自带标题区的区块（如 API 密钥卡片编辑器）：只提供锚点与整行单元格，不渲染标题。 */
export function SettingCell({ fieldId, children }: { fieldId: string; children: ReactNode }) {
  return (
    <div id={configFieldDomId(fieldId)} className={cx(styles.row, styles.rowCell, styles.rowWide)}>
      {children}
    </div>
  );
}

type RowElement = ReactElement<{ wide?: boolean; children?: ReactNode }>;

/** 展开 Fragment（如 TLS 开启后才出现的证书/私钥两行），并给嵌套项加前缀防 key 冲突。 */
function flattenRows(children: ReactNode, prefix = ''): RowElement[] {
  return Children.toArray(children).flatMap((child) => {
    if (!isValidElement<{ wide?: boolean; children?: ReactNode }>(child)) return [];
    if (child.type === Fragment) {
      return flattenRows(child.props.children, `${prefix}${String(child.key)}/`);
    }
    return [prefix ? cloneElement(child, { key: `${prefix}${String(child.key)}` }) : child];
  });
}

const isFullRow = (row: RowElement) =>
  row.type === BlockSetting || row.type === SettingCell || Boolean(row.props.wide);

/**
 * 设置组：可选的组标题 + 一个圆角列表容器。容器宽度足够时行两列排布，
 * 窄屏单列、输入行改为上下堆叠（容器查询，随侧栏收起/折叠组缩进自适应）。
 * 两列配对只读直接子元素的 props.wide（及 BlockSetting / SettingCell 类型），
 * 包装组件若要占整行，须在调用处显式传 wide。
 */
export function SettingList({
  title,
  description,
  children,
}: {
  title?: string;
  description?: ReactNode;
  children: ReactNode;
}) {
  const rows = flattenRows(children);
  const spans = resolveRowSpans(rows.map(isFullRow));
  // 有组标题才是带名分区；无标题时只是一个列表容器。
  const Group = title ? 'section' : 'div';
  return (
    <Group className={styles.group}>
      {title ? (
        <header className={styles.groupHeader}>
          <h3 className={styles.groupTitle}>{title}</h3>
          {description ? <p className={styles.groupDescription}>{description}</p> : null}
        </header>
      ) : null}
      <div className={styles.list}>
        {rows.map((row, index) =>
          spans[index] && !isFullRow(row) ? cloneElement(row, { wide: true }) : row
        )}
      </div>
    </Group>
  );
}

/* ---------- 布局与区块编辑器内部原语 ---------- */

export function FieldGrid({ children }: { children: ReactNode }) {
  return <div className={styles.fieldGrid}>{children}</div>;
}

export function FieldStack({ children }: { children: ReactNode }) {
  return <div className={styles.fieldStack}>{children}</div>;
}

// Stable, stateless anchor around a searchable block that is not a setting row
// (payload rule groups). Search jumps target its DOM id and pulse-highlight it.
export function FieldAnchor({ fieldId, children }: { fieldId: string; children: ReactNode }) {
  return (
    <div id={configFieldDomId(fieldId)} className={styles.fieldAnchor}>
      {children}
    </div>
  );
}

/** 独立的提示行（SettingCell 里编辑器上方的说明等）。 */
export function FieldHint({ id, children }: { id?: string; children: ReactNode }) {
  return (
    <div id={id} className={styles.fieldHint}>
      {children}
    </div>
  );
}

/** 区块编辑器内部的「标签在上」字段（载荷规则等多控件表单）。 */
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
