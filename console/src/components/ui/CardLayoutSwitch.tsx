import { useRef, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { IconGrid3x3, IconLayoutDashboard, IconRows3 } from './icons';
import { CARD_LAYOUTS, type CardLayout } from './cardLayout';
import styles from './CardLayoutSwitch.module.scss';

const LAYOUT_ICONS = {
  even: IconRows3,
  stacked: IconLayoutDashboard,
  compact: IconGrid3x3,
} as const;

export interface CardLayoutSwitchProps {
  value: CardLayout;
  onChange: (layout: CardLayout) => void;
  disabled?: boolean;
  className?: string;
}

/** Icon-only radio group for Even rows / Stacked / Compact. Arrow keys move the choice. */
export function CardLayoutSwitch({ value, onChange, disabled, className }: CardLayoutSwitchProps) {
  const { t } = useTranslation();
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const select = (index: number) => {
    const next = (index + CARD_LAYOUTS.length) % CARD_LAYOUTS.length;
    onChange(CARD_LAYOUTS[next]);
    optionRefs.current[next]?.focus();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const current = CARD_LAYOUTS.indexOf(value);
    if (event.key === 'ArrowRight' || event.key === 'ArrowDown') {
      event.preventDefault();
      select(current + 1);
    } else if (event.key === 'ArrowLeft' || event.key === 'ArrowUp') {
      event.preventDefault();
      select(current - 1);
    }
  };

  return (
    <div
      role="radiogroup"
      aria-label={t('card_layout.label')}
      className={className ? `${styles.switch} ${className}` : styles.switch}
      onKeyDown={handleKeyDown}
    >
      {CARD_LAYOUTS.map((layout, index) => {
        const Icon = LAYOUT_ICONS[layout];
        const checked = value === layout;
        const label = t(`card_layout.${layout}`);
        return (
          <button
            key={layout}
            ref={(node) => {
              optionRefs.current[index] = node;
            }}
            type="button"
            role="radio"
            aria-checked={checked}
            aria-label={label}
            title={label}
            tabIndex={checked ? 0 : -1}
            disabled={disabled}
            className={checked ? `${styles.option} ${styles.optionActive}` : styles.option}
            onClick={() => onChange(layout)}
          >
            <Icon size={16} />
          </button>
        );
      })}
    </div>
  );
}
