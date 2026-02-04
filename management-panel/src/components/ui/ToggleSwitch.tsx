import type { ChangeEvent, ReactNode } from 'react';

interface ToggleSwitchProps {
  checked: boolean;
  onChange: (value: boolean) => void;
  label?: ReactNode;
  ariaLabel?: string;
  disabled?: boolean;
  labelPosition?: 'left' | 'right';
}

export function ToggleSwitch({
  checked,
  onChange,
  label,
  ariaLabel,
  disabled = false,
  labelPosition = 'right'
}: ToggleSwitchProps) {
  const handleChange = (event: ChangeEvent<HTMLInputElement>) => {
    onChange(event.target.checked);
  };

  const className = ['switch', labelPosition === 'left' ? 'switch-label-left' : '']
    .filter(Boolean)
    .join(' ');

  return (
    <label className={className}>
      <input
        type="checkbox"
        checked={checked}
        onChange={handleChange}
        disabled={disabled}
        aria-label={ariaLabel}
      />
      <span className="track">
        <span className="thumb" />
      </span>
      {label && <span className="label">{label}</span>}
    </label>
  );
}
