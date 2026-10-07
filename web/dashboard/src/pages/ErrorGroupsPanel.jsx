// ErrorGroupsPanel — grouped error leaderboard for the Error Patterns tab.
//
// Presentational panel: all data + callbacks via props. The segmented control
// picks the grouping dimension, and clicking a row calls onSelectGroup to
// narrow the main Failed-attempts table below.

import React from 'react';
import {
  Spinner, ErrorBanner, EmptyState,
} from '../components/Primitives.jsx';
import { formatInTZ, CopyButton } from './usageShared.jsx';
import { classLabel, classTone } from './errorClass.js';

// Labels map group_by values to short UI labels for the segmented control.
const GROUP_LABELS = [
  { value: 'class', label: 'Class' },
  { value: 'fingerprint', label: 'Message' },
  { value: 'provider', label: 'Provider' },
  { value: 'model', label: 'Model' },
  { value: 'status', label: 'Status' },
];

export default function ErrorGroupsPanel({
  groups,
  loading,
  error,
  groupBy,
  onGroupByChange,
  onSelectGroup,
  timezone,
  onRetry,
}) {
  return (
    <>
      {/* Segmented control — choose the grouping dimension */}
      <div className="seg-group" role="tablist" aria-label="Group by">
        {GROUP_LABELS.map((g) => (
          <button
            key={g.value}
            type="button"
            className={`seg-btn ${groupBy === g.value ? 'seg-btn--active' : ''}`}
            onClick={() => onGroupByChange(g.value)}
          >
            {g.label}
          </button>
        ))}
      </div>

      {/* Leaderboard table */}
      {loading && (
        <div style={{ overflowX: 'auto', marginTop: 12 }}>
          <table className="table" style={{ tableLayout: 'fixed' }}>
            <thead>
              <tr>
                <th>Class</th>
                <th>Sample message</th>
                <th style={{ textAlign: 'right' }}>Count</th>
                <th>First seen</th>
                <th>Last seen</th>
                <th>Providers</th>
                <th>Models</th>
              </tr>
            </thead>
            <tbody>
              {Array.from({ length: 4 }).map((_, i) => (
                <tr key={i} className="skeleton-row">
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                  <td><span className="skeleton-line" /></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {error && <ErrorBanner error={error} onRetry={onRetry} />}
      {!loading && !error && (!groups || groups.length === 0) && (
        <EmptyState title="No error patterns in this window" />
      )}
      {!loading && !error && groups && groups.length > 0 && (
        <div style={{ overflowX: 'auto', marginTop: 12 }}>
          <table className="table">
            <thead>
              <tr>
                <th>Class</th>
                <th>Sample message</th>
                <th style={{ textAlign: 'right' }}>Count</th>
                <th>First seen</th>
                <th>Last seen</th>
                <th>Providers</th>
                <th>Models</th>
              </tr>
            </thead>
            <tbody>
              {groups.map((g, i) => (
                <tr
                  key={g.key || String(i)}
                  className="row-link"
                  onClick={() => onSelectGroup(g)}
                  title="Click to filter the Failed attempts table to this group"
                >
                  <td>
                    {g.error_class ? (
                      <span
                        className="err-class-chip"
                        style={{ '--chip-tone': classTone(g.error_class), cursor: 'default' }}
                      >
                        <span className="err-class-chip__dot" />
                        <span className="err-class-chip__label">{classLabel(g.error_class)}</span>
                      </span>
                    ) : (
                      <span style={{ color: 'var(--text-dim)', fontSize: 12 }}>—</span>
                    )}
                  </td>
                  <td style={{ maxWidth: 360, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                    {g.sample_message || '—'}
                    {g.sample_message && (
                      <CopyButton value={g.sample_message} label="error message" />
                    )}
                  </td>
                  <td className="mono" style={{ textAlign: 'right' }}>{g.count.toLocaleString()}</td>
                  <td className="mono" style={{ whiteSpace: 'nowrap', fontSize: 12 }}>
                    {g.first_seen ? formatInTZ(g.first_seen, timezone) : '—'}
                  </td>
                  <td className="mono" style={{ whiteSpace: 'nowrap', fontSize: 12 }}>
                    {g.last_seen ? formatInTZ(g.last_seen, timezone) : '—'}
                  </td>
                  <td style={{ maxWidth: 160 }}>
                    <TruncatedList items={g.providers} />
                  </td>
                  <td style={{ maxWidth: 160 }}>
                    <TruncatedList items={g.models} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

// Renders a short comma-separated list of items, collapsing beyond ~2-3 into
// a "+N more" suffix so the table column stays compact.
function TruncatedList({ items, max = 3 }) {
  if (!items || items.length === 0) return <span style={{ color: 'var(--text-dim)', fontSize: 12 }}>—</span>;
  const visible = items.slice(0, max);
  const rest = items.length - max;
  return (
    <span style={{ fontSize: 12 }}>
      {visible.map((item, i) => (
        <span key={item}>
          {i > 0 && <span style={{ color: 'var(--text-dim)' }}>, </span>}
          <span className="mono" style={{ fontSize: 12 }}>{item}</span>
        </span>
      ))}
      {rest > 0 && <span style={{ color: 'var(--text-dim)', marginLeft: 2 }}>+{rest} more</span>}
    </span>
  );
}
