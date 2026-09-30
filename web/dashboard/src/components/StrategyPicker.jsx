import React from 'react';
import { activeStrategyOption } from './routingStrategies.js';

// StrategyPicker — the shared segmented control used by every routing surface
// (per-model route, auto-router tier targets, global strategy). Options come
// from routingStrategies.js so the value sets never drift apart.
//
// Props:
//   options      — [{ value, label, blurb }] option list.
//   value        — current value; unknown values fall back to the first option.
//   onChange     — called with the selected option's value.
//   ariaLabel    — accessible group label.
//   className    — extra class appended to the `.seg` container.
//   showBlurb    — render the active option's blurb below the control.
//   blurbClassName — class for the blurb element (default model-routes style).
export default function StrategyPicker({
  options,
  value,
  onChange,
  ariaLabel,
  className = '',
  showBlurb = false,
  blurbClassName = 'model-routes__strategyblurb muted',
}) {
  const active = activeStrategyOption(options, value);
  return (
    <>
      <div className={`seg ${className}`.trim()} role="group" aria-label={ariaLabel}>
        {options.map((opt) => {
          const isActive = (value || '') === opt.value;
          return (
            <button
              key={opt.value || 'default'}
              type="button"
              className={`seg__btn ${isActive ? 'seg__btn--active' : ''}`}
              onClick={() => onChange(opt.value)}
              title={opt.blurb}
              aria-pressed={isActive}
            >
              {opt.label}
            </button>
          );
        })}
      </div>
      {showBlurb && <div className={blurbClassName}>{active.blurb}</div>}
    </>
  );
}
