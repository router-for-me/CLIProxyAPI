import React from 'react';

export function Spinner({ label = 'Loading…' }) {
  return (
    <div className="empty-state">
      <div className="spinner" style={{ margin: '0 auto 12px' }} />
      <div className="empty-state__title">{label}</div>
    </div>
  );
}

export function ErrorBanner({ error, onRetry }) {
  if (!error) return null;
  return (
    <div className="error-banner">
      <div className="row row--between">
        <span>{errorMessage(error)}</span>
        {onRetry && (
          <button onClick={onRetry} style={{ padding: '2px 8px', fontSize: 12 }}>
            Retry
          </button>
        )}
      </div>
    </div>
  );
}

export function EmptyState({ title = 'Nothing here yet', hint }) {
  return (
    <div className="empty-state">
      <div className="empty-state__title">{title}</div>
      {hint && <div className="dim" style={{ marginTop: 4, fontSize: 12 }}>{hint}</div>}
    </div>
  );
}

export function StatusBadge({ status }) {
  const cls = {
    active: 'badge--active',
    failed: 'badge--revoked',
    disabled: 'badge--disabled',
    revoked: 'badge--revoked',
    expired: 'badge--expired',
  }[status] || 'badge--muted';
  return <span className={`badge ${cls}`}>{status || 'unknown'}</span>;
}

export function Modal({ title, onClose, children }) {
  return (
    <div className="modal-backdrop" onClick={onClose}>
      <div className="modal" onClick={(e) => e.stopPropagation()}>
        <div className="row row--between" style={{ marginBottom: 16 }}>
          <h3 className="modal__title">{title}</h3>
          <button onClick={onClose} style={{ padding: '4px 10px' }}>Close</button>
        </div>
        {children}
      </div>
    </div>
  );
}

export function Stat({ label, value, delta }) {
  return (
    <div className="stat">
      <div className="stat__label">{label}</div>
      <div className="stat__value">{value}</div>
      {delta && <div className="stat__delta">{delta}</div>}
    </div>
  );
}

function errorMessage(err) {
  if (!err) return 'Unknown error';
  if (err.message) return err.message;
  return String(err);
}
