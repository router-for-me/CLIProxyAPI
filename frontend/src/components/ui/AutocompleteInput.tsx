import { useEffect, useRef, useState, type ChangeEvent, type KeyboardEvent, type ReactNode } from 'react';
import { IconChevronDown } from './icons';

interface AutocompleteInputProps {
  label?: string;
  value: string;
  onChange: (value: string) => void;
  options: string[] | { value: string; label?: string }[];
  placeholder?: string;
  disabled?: boolean;
  hint?: string;
  error?: string;
  className?: string;
  wrapperClassName?: string;
  wrapperStyle?: React.CSSProperties;
  id?: string;
  rightElement?: ReactNode;
}

export function AutocompleteInput({
  label,
  value,
  onChange,
  options,
  placeholder,
  disabled,
  hint,
  error,
  className = '',
  wrapperClassName = '',
  wrapperStyle,
  id,
  rightElement
}: AutocompleteInputProps) {
  const [isOpen, setIsOpen] = useState(false);
  const [highlightedIndex, setHighlightedIndex] = useState(-1);
  const containerRef = useRef<HTMLDivElement>(null);
  
  const normalizedOptions = options.map(opt => 
    typeof opt === 'string' ? { value: opt, label: opt } : { value: opt.value, label: opt.label || opt.value }
  );

  const filteredOptions = normalizedOptions.filter(opt => {
    const v = value.toLowerCase();
    return opt.value.toLowerCase().includes(v) || (opt.label && opt.label.toLowerCase().includes(v));
  });

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(event.target as Node)) {
        setIsOpen(false);
      }
    };
    document.addEventListener('mousedown', handleClickOutside);
    return () => document.removeEventListener('mousedown', handleClickOutside);
  }, []);

  const handleInputChange = (e: ChangeEvent<HTMLInputElement>) => {
    onChange(e.target.value);
    setIsOpen(true);
    setHighlightedIndex(-1);
  };

  const handleSelect = (selectedValue: string) => {
    onChange(selectedValue);
    setIsOpen(false);
  };

  const handleKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (disabled) return;

    if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (!isOpen) {
          setIsOpen(true);
          return;
      }
      setHighlightedIndex(prev => 
        prev < filteredOptions.length - 1 ? prev + 1 : prev
      );
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setHighlightedIndex(prev => prev > 0 ? prev - 1 : 0);
    } else if (e.key === 'Enter') {
      if (isOpen && highlightedIndex >= 0 && highlightedIndex < filteredOptions.length) {
        e.preventDefault();
        handleSelect(filteredOptions[highlightedIndex].value);
      } else if (isOpen) {
          e.preventDefault();
          setIsOpen(false);
      }
    } else if (e.key === 'Escape') {
      setIsOpen(false);
    } else if (e.key === 'Tab') {
      setIsOpen(false);
    }
  };

  return (
    <div className={`form-group ${wrapperClassName}`} ref={containerRef} style={wrapperStyle}>
      {label && <label htmlFor={id}>{label}</label>}
      <div style={{ position: 'relative' }}>
        <input 
            id={id}
            className={`input ${className}`.trim()} 
            value={value}
            onChange={handleInputChange}
            onFocus={() => setIsOpen(true)}
            onKeyDown={handleKeyDown}
            placeholder={placeholder}
            disabled={disabled}
            autoComplete="off"
            style={{ paddingRight: 32 }}
        />
        <div 
            style={{ 
                position: 'absolute', 
                right: 8, 
                top: '50%', 
                transform: 'translateY(-50%)',
                display: 'flex',
                alignItems: 'center',
                pointerEvents: disabled ? 'none' : 'auto',
                cursor: 'pointer',
                height: '100%'
            }}
            onClick={() => !disabled && setIsOpen(!isOpen)}
        >
            {rightElement}
            <IconChevronDown size={16} style={{ opacity: 0.5, marginLeft: 4 }} />
        </div>

        {isOpen && filteredOptions.length > 0 && !disabled && (
            <div className="autocomplete-dropdown" style={{
                position: 'absolute',
                top: 'calc(100% + 4px)',
                left: 0,
                right: 0,
                zIndex: 1000,
                backgroundColor: 'var(--bg-secondary)',
                border: '1px solid var(--border-color)',
                borderRadius: 'var(--radius-md)',
                maxHeight: 200,
                overflowY: 'auto',
                boxShadow: '0 4px 6px -1px rgba(0, 0, 0, 0.1), 0 2px 4px -1px rgba(0, 0, 0, 0.06)'
            }}>
                {filteredOptions.map((opt, index) => (
                    <div
                        key={`${opt.value}-${index}`}
                        onClick={() => handleSelect(opt.value)}
                        style={{
                            padding: '8px 12px',
                            cursor: 'pointer',
                            backgroundColor: index === highlightedIndex ? 'var(--bg-tertiary)' : 'transparent',
                            color: 'var(--text-primary)',
                            display: 'flex',
                            flexDirection: 'column',
                            fontSize: '0.9rem'
                        }}
                        onMouseEnter={() => setHighlightedIndex(index)}
                    >
                        <span style={{ fontWeight: 500 }}>{opt.value}</span>
                        {opt.label && opt.label !== opt.value && (
                            <span style={{ fontSize: '0.85em', color: 'var(--text-secondary)' }}>{opt.label}</span>
                        )}
                    </div>
                ))}
            </div>
        )}
      </div>
      {hint && <div className="hint">{hint}</div>}
      {error && <div className="error-box">{error}</div>}
    </div>
  );
}
