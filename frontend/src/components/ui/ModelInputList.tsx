import { Fragment } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import type { ModelEntry } from './modelInputListUtils';

interface ModelInputListProps {
  entries: ModelEntry[];
  onChange: (entries: ModelEntry[]) => void;
  addLabel?: string;
  disabled?: boolean;
  namePlaceholder?: string;
  aliasPlaceholder?: string;
  hideAddButton?: boolean;
  onAdd?: () => void;
  className?: string;
  rowClassName?: string;
  inputClassName?: string;
  removeButtonClassName?: string;
  removeButtonTitle?: string;
  removeButtonAriaLabel?: string;
}

export function ModelInputList({
  entries,
  onChange,
  addLabel,
  disabled = false,
  namePlaceholder = 'model-name',
  aliasPlaceholder = 'alias (optional)',
  hideAddButton = false,
  onAdd,
  className = '',
  rowClassName = '',
  inputClassName = '',
  removeButtonClassName = '',
  removeButtonTitle = 'Remove',
  removeButtonAriaLabel = 'Remove',
}: ModelInputListProps) {
  const currentEntries = entries.length ? entries : [{ name: '', alias: '' }];
  const containerClassName = ['header-input-list', className].filter(Boolean).join(' ');
  const inputClassNames = ['input', inputClassName].filter(Boolean).join(' ');
  const rowClassNames = ['header-input-row', rowClassName].filter(Boolean).join(' ');

  const updateEntry = (index: number, field: 'name' | 'alias', value: string) => {
    const next = currentEntries.map((entry, idx) => (idx === index ? { ...entry, [field]: value } : entry));
    onChange(next);
  };

  const addEntry = () => {
    if (onAdd) {
      onAdd();
    } else {
      onChange([...currentEntries, { name: '', alias: '' }]);
    }
  };

  const removeEntry = (index: number) => {
    const next = currentEntries.filter((_, idx) => idx !== index);
    onChange(next.length ? next : [{ name: '', alias: '' }]);
  };

  return (
    <div className={containerClassName}>
      {currentEntries.map((entry, index) => (
        <Fragment key={index}>
          <div className={rowClassNames}>
            <input
              className={inputClassNames}
              placeholder={namePlaceholder}
              value={entry.name}
              onChange={(e) => updateEntry(index, 'name', e.target.value)}
              disabled={disabled}
            />
            <span className="header-separator">→</span>
            <input
              className={inputClassNames}
              placeholder={aliasPlaceholder}
              value={entry.alias}
              onChange={(e) => updateEntry(index, 'alias', e.target.value)}
              disabled={disabled}
            />
            <Button
              variant="ghost"
              size="sm"
              onClick={() => removeEntry(index)}
              disabled={disabled || currentEntries.length <= 1}
              className={removeButtonClassName}
              title={removeButtonTitle}
              aria-label={removeButtonAriaLabel}
            >
              <IconX size={14} />
            </Button>
          </div>
        </Fragment>
      ))}
      {!hideAddButton && addLabel && (
        <Button variant="secondary" size="sm" onClick={addEntry} disabled={disabled} className="align-start">
          {addLabel}
        </Button>
      )}
    </div>
  );
}
