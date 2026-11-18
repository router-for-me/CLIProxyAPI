import type { ReactNode } from 'react';
import { InfoTip } from './Tooltip';

export interface FieldLabelProps {
  /** Visible label text; also names the InfoTip ("More information about <label>"). */
  label: string;
  /** Associates a real `<label for>` with the control. */
  htmlFor?: string;
  /** Id on the label element, for controls that point at it with aria-labelledby. */
  id?: string;
  /**
   * Label element. Defaults to `label` when `htmlFor` is set and `span` otherwise (custom
   * controls that are named another way). Pass `label` explicitly where a stylesheet targets
   * label elements (e.g. `.form-group label`).
   */
  as?: 'label' | 'span';
  /** Explanation carried by an InfoTip after the label instead of a line on the page. */
  hint?: ReactNode;
  /**
   * Id for a `hidden` copy of `hint`. Pass it when the control points at the hint with
   * aria-describedby, so screen readers hear it on the control itself; the InfoTip reuses the
   * same copy. Without it the InfoTip carries its own description.
   */
  hintId?: string;
  /** Class of the row that holds the label and the InfoTip. Defaults to `form-label-row`. */
  className?: string;
  /** Class of the label element. */
  labelClassName?: string;
}

/**
 * A field label followed by an optional InfoTip. The InfoTip is a sibling of the label, never
 * inside it, or its accessible name would be appended to the control's name. Use this for
 * controls other than the shared `Input` (which renders the same row itself).
 */
export function FieldLabel({
  label,
  htmlFor,
  id,
  as,
  hint,
  hintId,
  className = 'form-label-row',
  labelClassName,
}: FieldLabelProps) {
  const element = as ?? (htmlFor ? 'label' : 'span');
  return (
    <div className={className}>
      {element === 'label' ? (
        <label id={id} htmlFor={htmlFor} className={labelClassName}>
          {label}
        </label>
      ) : (
        <span id={id} className={labelClassName}>
          {label}
        </span>
      )}
      {hint ? <InfoTip content={hint} label={label} descriptionId={hintId} /> : null}
      {hint && hintId ? (
        <span id={hintId} hidden>
          {hint}
        </span>
      ) : null}
    </div>
  );
}
