import { useRef, type KeyboardEvent } from 'react';
import { useTranslation } from 'react-i18next';
import { IconGrid3x3, IconRows3 } from '@/components/ui/icons';
import switchStyles from '@/components/ui/CardLayoutSwitch.module.scss';
import { STORE_LAYOUTS, type StoreLayout } from '../pluginsView';

const LAYOUT_ICONS = { list: IconRows3, cards: IconGrid3x3 } as const;
const LAYOUT_LABEL_KEYS = {
  list: 'plugin_management.layout_list',
  cards: 'plugin_management.layout_cards',
} as const;

interface StoreLayoutSwitchProps {
  value: StoreLayout;
  onChange: (layout: StoreLayout) => void;
  className?: string;
}

/** List / Cards radio group for the Store view; same pill and keys as `CardLayoutSwitch`. */
export function StoreLayoutSwitch({ value, onChange, className }: StoreLayoutSwitchProps) {
  const { t } = useTranslation();
  const optionRefs = useRef<(HTMLButtonElement | null)[]>([]);

  const select = (index: number) => {
    const next = (index + STORE_LAYOUTS.length) % STORE_LAYOUTS.length;
    onChange(STORE_LAYOUTS[next]);
    optionRefs.current[next]?.focus();
  };

  const handleKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    const current = STORE_LAYOUTS.indexOf(value);
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
      aria-label={t('plugin_management.layout_label')}
      className={className ? `${switchStyles.switch} ${className}` : switchStyles.switch}
      onKeyDown={handleKeyDown}
    >
      {STORE_LAYOUTS.map((layout, index) => {
        const Icon = LAYOUT_ICONS[layout];
        const checked = value === layout;
        const label = t(LAYOUT_LABEL_KEYS[layout]);
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
            className={
              checked ? `${switchStyles.option} ${switchStyles.optionActive}` : switchStyles.option
            }
            onClick={() => onChange(layout)}
          >
            <Icon size={16} />
          </button>
        );
      })}
    </div>
  );
}
