import React from 'react';

// CooldownBanner — thin strip above the chat column. Renders nothing when
// state is null. Tolerates both the normalized selector view
// ({ cooldownUntil, reason }) and legacy ({ cooldownUntil }) shapes.
export default function CooldownBanner({ state, onRefresh }) {
  if (!state) return null;
  const until = state.cooldownUntil || state.next_retry_after;
  return (
    <div className="playground-cooldown-banner" role="status">
      <span>
        {state.model ? `${state.model} on ` : ''}
        {state.provider ? `${state.provider}` : 'This model’s provider'} is in cooldown
        {until ? ` until ${new Date(until).toLocaleString()}` : ''}.
        {state.reason ? ` ${state.reason}.` : ''} Requests may fail or auto-route.
      </span>
      <button type="button" className="linklike" onClick={() => onRefresh?.()}>
        Refresh status
      </button>
    </div>
  );
}
