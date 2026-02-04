import type { InputHTMLAttributes, ReactNode } from 'react';

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  hint?: string;
  error?: string;
  rightElement?: ReactNode;
}

export function Input({ label, hint, error, rightElement, className = '', ...rest }: InputProps) {
  return (
    <div className="form-group">
      {label && <label>{label}</label>}
      <div style={{ position: 'relative' }}>
        <input className={`input ${className}`.trim()} {...rest} />
        {rightElement && (
          <div style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)' }}>
            {rightElement}
          </div>
        )}
      </div>
      {hint && <div className="hint">{hint}</div>}
      {error && <div className="error-box">{error}</div>}
    </div>
  );
}
