import { useRef, type KeyboardEvent, type ReactElement, type ReactNode } from 'react';
import styles from './SegmentedControl.module.scss';
import { segmentKeyTarget } from './segmentKeyTarget';

export interface SegmentedOption<T extends string | number> {
  value: T;
  label: ReactNode;
  ariaLabel?: string;
  disabled?: boolean;
}

export interface SegmentedControlProps<T extends string | number> {
  value: T;
  options: readonly SegmentedOption<T>[];
  onChange: (value: T) => void;
  ariaLabel?: string;
  ariaLabelledBy?: string;
  className?: string;
  disabled?: boolean;
}

/** Single-choice segmented control with radio-group semantics and roving focus. */
export function SegmentedControl<T extends string | number>({
  value,
  options,
  onChange,
  ariaLabel,
  ariaLabelledBy,
  className,
  disabled = false,
}: SegmentedControlProps<T>): ReactElement {
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);
  const isEnabled = (index: number) => !disabled && !options[index].disabled;

  const select = (index: number) => {
    optionRefs.current[index]?.focus();
    if (options[index].value !== value) onChange(options[index].value);
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLButtonElement>, index: number) => {
    const next = segmentKeyTarget(event.key, index, options.length, isEnabled);

    if (next < 0) return;
    event.preventDefault();
    select(next);
  };

  const checkedIndex = options.findIndex((option) => option.value === value);
  const tabStop =
    checkedIndex >= 0 && isEnabled(checkedIndex)
      ? checkedIndex
      : segmentKeyTarget('Home', 0, options.length, isEnabled);

  return (
    <div
      role="radiogroup"
      aria-label={ariaLabel}
      aria-labelledby={ariaLabelledBy}
      className={className ? `${styles.track} ${className}` : styles.track}
    >
      {options.map((option, index) => {
        const checked = index === checkedIndex;

        return (
          <button
            key={String(option.value)}
            ref={(node) => {
              optionRefs.current[index] = node;
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={option.ariaLabel}
            tabIndex={index === tabStop ? 0 : -1}
            data-checked={checked || undefined}
            disabled={!isEnabled(index)}
            className={styles.segment}
            onClick={() => select(index)}
            onKeyDown={(event) => handleKeyDown(event, index)}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
