import React from 'react';

// CooldownBanner — thin sticky strip at the top of the chat column.
// Renders nothing when state is null. Shows the model's provider is
// in cooldown and a "Refresh status" link that re-reads the snapshot.
export default function CooldownBanner({ state, onRefresh }) {
  if (!state) return null;
  return (
    <div className="playground-cooldown-banner" role="status">
      <span>
        This model's provider is in cooldown until{' '}
        {state.cooldownUntil ? new Date(state.cooldownUntil).toLocaleString() : 'further notice'}.
        Requests may fail or auto-route.
      </span>
      <a
        href="#refresh"
        onClick={(e) => { e.preventDefault(); onRefresh?.(); }}
      >
        Refresh status
      </a>
    </div>
  );
}