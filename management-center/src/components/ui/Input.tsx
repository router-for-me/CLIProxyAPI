import { useId, type InputHTMLAttributes, type ReactNode } from 'react';

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  /** 渲染在标签正下方的小字行（如赞助跳转链接）。 */
  labelExtra?: ReactNode;
  /** 渲染在标签上方的占位行（用于与同排带 labelExtra 的字段保持输入框对齐）。 */
  topExtra?: ReactNode;
  hint?: ReactNode;
  error?: string;
  rightElement?: ReactNode;
}

export function Input({
  label,
  labelExtra,
  topExtra,
  hint,
  error,
  rightElement,
  className = '',
  id,
  ...rest
}: InputProps) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const hintId = hint ? `${inputId}-hint` : undefined;
  const errorId = error ? `${inputId}-error` : undefined;
  const describedBy =
    [rest['aria-describedby'], errorId, hintId].filter(Boolean).join(' ') || undefined;

  return (
    <div className="form-group">
      {topExtra}
      {label && <label htmlFor={inputId}>{label}</label>}
      {labelExtra}
      <div style={{ position: 'relative' }}>
        <input
          id={inputId}
          className={`input ${className}`.trim()}
          aria-invalid={Boolean(error) || rest['aria-invalid']}
          aria-describedby={describedBy}
          {...rest}
        />
        {rightElement && (
          <div
            style={{ position: 'absolute', right: 8, top: '50%', transform: 'translateY(-50%)' }}
          >
            {rightElement}
          </div>
        )}
      </div>
      {hint && (
        <div id={hintId} className="hint">
          {hint}
        </div>
      )}
      {error && (
        <div id={errorId} className="error-box">
          {error}
        </div>
      )}
    </div>
  );
}
