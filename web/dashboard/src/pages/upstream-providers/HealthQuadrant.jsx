import React from 'react';
import { StatusDot } from './components/StatusDot.jsx';

// HealthQuadrant renders one of the four quadrants on the HealthPage.
// Rows are { row, status } pairs from partitionByHealth.
export function HealthQuadrant({ title, tone, rows, emptyHint, onEdit, onToggleDisabled }) {
  return (
    <section className="card" aria-label={`${title} providers, ${rows.length} total`}>
      <header className="flex items-center justify-between px-3 py-2 border-b border-zinc-200 dark:border-zinc-700">
        <h2 className="text-sm font-semibold">{title}</h2>
        <span className="text-xs text-zinc-500">{rows.length}</span>
      </header>
      <ul className="divide-y divide-zinc-100 dark:divide-zinc-800 max-h-96 overflow-auto">
        {rows.length === 0 ? (
          <li className="px-3 py-4 text-xs text-zinc-500 italic">{emptyHint}</li>
        ) : (
          rows.map(({ row, status }) => (
            <li key={row.id} className="px-3 py-2 flex items-center gap-3 text-sm">
              <span className="font-mono text-xs text-zinc-500">{row.provider_type}</span>
              <span className="flex-1 truncate">{row.name || row.email || row.file_name || `Provider #${row.id}`}</span>
              <StatusDot status={status} size="xs" />
              <button type="button" className="btn-icon" onClick={() => onEdit(row)} aria-label={`Edit ${row.name || row.id}`}>✎</button>
              <button type="button" className="btn-icon" onClick={() => onToggleDisabled(row)} aria-label={`Toggle ${row.name || row.id} disabled`}>{row.disabled ? '✓' : '✕'}</button>
            </li>
          ))
        )}
      </ul>
    </section>
  );
}

export default HealthQuadrant;
