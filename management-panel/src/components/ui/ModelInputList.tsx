import { Fragment } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import type { ModelEntry } from './modelInputListUtils';

interface ModelInputListProps {
  entries: ModelEntry[];
  onChange: (entries: ModelEntry[]) => void;
  addLabel: string;
  disabled?: boolean;
  namePlaceholder?: string;
  aliasPlaceholder?: string;
}

export function ModelInputList({
  entries,
  onChange,
  addLabel,
  disabled = false,
  namePlaceholder = 'model-name',
  aliasPlaceholder = 'alias (optional)'
}: ModelInputListProps) {
  const currentEntries = entries.length ? entries : [{ name: '', alias: '' }];

  const updateEntry = (index: number, field: 'name' | 'alias', value: string) => {
    const next = currentEntries.map((entry, idx) => (idx === index ? { ...entry, [field]: value } : entry));
    onChange(next);
  };

  const addEntry = () => {
    onChange([...currentEntries, { name: '', alias: '' }]);
  };

  const removeEntry = (index: number) => {
    const next = currentEntries.filter((_, idx) => idx !== index);
    onChange(next.length ? next : [{ name: '', alias: '' }]);
  };

  return (
    <div className="header-input-list">
      {currentEntries.map((entry, index) => (
        <Fragment key={index}>
          <div className="header-input-row">
            <input
              className="input"
              placeholder={namePlaceholder}
              value={entry.name}
              onChange={(e) => updateEntry(index, 'name', e.target.value)}
              disabled={disabled}
            />
            <span className="header-separator">→</span>
            <input
              className="input"
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
              title="Remove"
              aria-label="Remove"
            >
              <IconX size={14} />
            </Button>
          </div>
        </Fragment>
      ))}
      <Button variant="secondary" size="sm" onClick={addEntry} disabled={disabled} className="align-start">
        {addLabel}
      </Button>
    </div>
  );
}
