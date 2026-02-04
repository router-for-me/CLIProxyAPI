import { Fragment } from 'react';
import { Button } from './Button';
import { IconX } from './icons';
import type { HeaderEntry } from '@/utils/headers';

interface HeaderInputListProps {
  entries: HeaderEntry[];
  onChange: (entries: HeaderEntry[]) => void;
  addLabel: string;
  disabled?: boolean;
  keyPlaceholder?: string;
  valuePlaceholder?: string;
}

export function HeaderInputList({
  entries,
  onChange,
  addLabel,
  disabled = false,
  keyPlaceholder = 'X-Custom-Header',
  valuePlaceholder = 'value'
}: HeaderInputListProps) {
  const currentEntries = entries.length ? entries : [{ key: '', value: '' }];

  const updateEntry = (index: number, field: 'key' | 'value', value: string) => {
    const next = currentEntries.map((entry, idx) => (idx === index ? { ...entry, [field]: value } : entry));
    onChange(next);
  };

  const addEntry = () => {
    onChange([...currentEntries, { key: '', value: '' }]);
  };

  const removeEntry = (index: number) => {
    const next = currentEntries.filter((_, idx) => idx !== index);
    onChange(next.length ? next : [{ key: '', value: '' }]);
  };

  return (
    <div className="header-input-list">
      {currentEntries.map((entry, index) => (
        <Fragment key={index}>
          <div className="header-input-row">
            <input
              className="input"
              placeholder={keyPlaceholder}
              value={entry.key}
              onChange={(e) => updateEntry(index, 'key', e.target.value)}
              disabled={disabled}
            />
            <span className="header-separator">:</span>
            <input
              className="input"
              placeholder={valuePlaceholder}
              value={entry.value}
              onChange={(e) => updateEntry(index, 'value', e.target.value)}
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
