import type { ChangeEvent, ReactNode } from 'react';
import styles from './ToggleSwitch.module.scss';

interface ToggleSwitchProps {
  checked: boolean;
  onChange: (value: boolean) => void;
  label?: ReactNode;
  ariaLabel?: string;
  /** 外部 <label htmlFor> 关联用；不传则仅靠 label / ariaLabel 命名。 */
  id?: string;
  ariaDescribedBy?: string;
  disabled?: boolean;
  labelPosition?: 'left' | 'right';
}

export function ToggleSwitch({
  checked,
  onChange,
  label,
  ariaLabel,
  id,
  ariaDescribedBy,
  disabled = false,
  labelPosition = 'right',
}: ToggleSwitchProps) {
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    onChange(event.target.checked);
  };

  const className = [
    styles.root,
    labelPosition === 'left' ? styles.labelLeft : '',
    disabled ? styles.disabled : '',
  ]
    .filter(Boolean)
    .join(' ');

  return (
    <label className={className}>
      <input
        id={id}
        type="checkbox"
        checked={checked}
        onChange={handleChange}
        disabled={disabled}
        aria-label={ariaLabel}
        aria-describedby={ariaDescribedBy}
      />
      <span className={styles.track}>
        <span className={styles.thumb} />
      </span>
      {label && <span className={styles.label}>{label}</span>}
    </label>
  );
}
