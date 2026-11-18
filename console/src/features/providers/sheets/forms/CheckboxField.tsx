import { useId, type ReactNode } from 'react';
import { InfoTip } from '@/components/ui/Tooltip';
import styles from './sharedForm.module.scss';

interface CheckboxFieldProps {
  label: string;
  checked: boolean;
  disabled?: boolean;
  onChange: (checked: boolean) => void;
  /**
   * Explanation carried by an InfoTip after the label. The checkbox stays described by a
   * hidden copy, so assistive tech hears it on the checkbox itself.
   */
  hint?: ReactNode;
}

/** Checkbox row for the provider forms; the InfoTip sits outside the `<label>`. */
export function CheckboxField({ label, checked, disabled, onChange, hint }: CheckboxFieldProps) {
  const hintId = useId();
  const row = (
    <label className={styles.checkboxRow}>
      <input
        type="checkbox"
        className={styles.checkboxBox}
        checked={checked}
        disabled={disabled}
        aria-describedby={hint ? hintId : undefined}
        onChange={(event) => onChange(event.target.checked)}
      />
      <span className={styles.checkboxText}>{label}</span>
    </label>
  );
  if (!hint) return row;
  return (
    <div className={styles.checkboxLine}>
      {row}
      <InfoTip content={hint} label={label} descriptionId={hintId} />
      <span id={hintId} hidden>
        {hint}
      </span>
    </div>
  );
}
