import { useId, type InputHTMLAttributes, type ReactNode } from 'react';
import { InfoTip } from './Tooltip';

interface InputProps extends InputHTMLAttributes<HTMLInputElement> {
  label?: string;
  /**
   * Explanation for the field. With a `label`, it is shown through an InfoTip after the label
   * and kept in the DOM visually hidden so the input stays described (aria-describedby).
   * Without a `label` there is nothing to attach the InfoTip to, so it stays visible.
   */
  hint?: ReactNode;
  /**
   * A line that always stays visible under the input, independent of `hint`. For text that
   * must be on the page: security notes, destructive consequences, or anything containing a
   * link or other interactive content (a tooltip cannot hold those). Also describes the input.
   */
  note?: ReactNode;
  error?: string;
  rightElement?: ReactNode;
}

export function Input({
  label,
  hint,
  note,
  error,
  rightElement,
  className = '',
  id,
  ...rest
}: InputProps) {
  const generatedId = useId();
  const inputId = id ?? generatedId;
  const hintId = hint ? `${inputId}-hint` : undefined;
  const noteId = note ? `${inputId}-note` : undefined;
  const errorId = error ? `${inputId}-error` : undefined;
  const describedBy =
    [rest['aria-describedby'], errorId, hintId, noteId].filter(Boolean).join(' ') || undefined;
  const hintInTooltip = Boolean(hint && label);

  return (
    <div className="form-group">
      {label &&
        (hintInTooltip ? (
          <div className="form-label-row">
            <label htmlFor={inputId}>{label}</label>
            <InfoTip content={hint} label={label} descriptionId={hintId} />
          </div>
        ) : (
          <label htmlFor={inputId}>{label}</label>
        ))}
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
        <div id={hintId} className={hintInTooltip ? 'hint visually-hidden-focusable' : 'hint'}>
          {hint}
        </div>
      )}
      {note && (
        <div id={noteId} className="hint">
          {note}
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
