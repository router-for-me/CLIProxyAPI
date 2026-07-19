import React from 'react';

// Pager renders a prev/next navigation block with the current page and total.
//
// The component is presentational: it calls onPageChange with the desired
// page number when the user clicks prev/next or jumps. It does not fetch
// data itself — the parent owns the page state and the reload trigger.
//
// Renders nothing when totalPages <= 1 (no pager needed).
export default function Pager({ page, totalPages, total, pageSize, onPageChange }) {
  if (totalPages <= 1) {
    // Even without prev/next, show "N of M" so operators see result size.
    if (total == null) return null;
    return (
      <div className="dim" style={{ fontSize: 12, marginTop: 12 }}>
       Showing {total.toLocaleString()} {total === 1 ? 'row' : 'rows'}
      </div>
    );
  }
  const from = (page - 1) * pageSize + 1;
  const to = Math.min(page * pageSize, total);
  return (
    <div className="row row--between" style={{ marginTop: 16 }}>
      <div className="dim" style={{ fontSize: 12 }}>
        Showing {from.toLocaleString()}–{to.toLocaleString()} of {total.toLocaleString()}
      </div>
      <div className="row gap-sm">
        <button
          onClick={() => onPageChange(page - 1)}
          disabled={page <= 1}
          style={{ padding: '4px 10px', fontSize: 12 }}
        >
          ‹ Prev
        </button>
        <span className="mono" style={{ padding: '4px 10px' }}>
          {page} / {totalPages}
        </span>
        <button
          onClick={() => onPageChange(page + 1)}
          disabled={page >= totalPages}
          style={{ padding: '4px 10px', fontSize: 12 }}
        >
          Next ›
        </button>
      </div>
    </div>
  );
}
