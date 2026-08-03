import React, { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import {
  getActiveAlerts,
  getAlerts,
  getUnreadAlertCount,
  markAlertRead,
  markAllAlertsRead,
  dismissAlert,
} from '../api/client.js';
import { useAsync } from '../hooks/useAsync.js';
import { useAutoRefresh } from '../hooks/useAutoRefresh.js';
import { formatRelativeTime } from '../utils/formatRelativeTime.js';

// AlertsDropdown is the notification panel anchored to the header bell. It is
// an alternative to a centered modal: a fixed-position, viewport-aware panel
// that opens from (and stays glued to) the bell button, closes on outside
// click / Escape, and live-refreshes while open. It lists every alert (unread
// first, then recent read ones) with per-row Read/Dismiss actions and a footer
// linking to the full Alerts page and the Alert settings.
//
// The anchoring recipe mirrors ModelIdCombobox: getBoundingClientRect() →
// position:fixed with viewport-aware up/down flipping and maxHeight clamping,
// re-anchored on scroll/resize so it stays attached to the sticky header.

const HISTORY_LIMIT = 10; // max read ("Earlier") rows shown before "View all"
const REFRESH_MS = 15000; // live refresh while the panel is open

// Anchor to the bell (passed via a ref-bearing wrapper). The panel measures the
// bell button and positions itself below it, flipping upward when it would
// otherwise overflow the viewport.
export default function AlertsDropdown({ bellRef, onClose }) {
  const navigate = useNavigate();

  // Live feed + unread count, refreshed while open.
  const active = useAsync(() => getActiveAlerts(), []);
  const unread = useAsync(() => getUnreadAlertCount(), []);
  const history = useAsync(() => getAlerts({ page: 1, page_size: 50 }), []);

  // Dropdown anchor state (view-relative left/top/maxHeight).
  const [anchor, setAnchor] = useState(null);
  const panelRef = useRef(null);

  const broadcast = useCallback(() => {
    window.dispatchEvent(new CustomEvent('nixllm:alerts-changed'));
  }, []);

  const reloadAll = useCallback(() => {
    active.reload();
    unread.reload();
    history.reload();
  }, [active, unread, history]);

  // Live refresh while the panel is open.
  useAutoRefresh(reloadAll, REFRESH_MS, true);

  // Close on route change (navigating via the footer links).
  const close = useCallback(() => { onClose?.(); }, [onClose]);

  // Click-outside + Escape to close (recipe from ModelIdCombobox).
  useEffect(() => {
    function onDocClick(e) {
      if (panelRef.current && !panelRef.current.contains(e.target) &&
          bellRef && bellRef.current && !bellRef.current.contains(e.target)) {
        close();
      }
    }
    function onKey(e) {
      if (e.key === 'Escape') close();
    }
    function onScroll(e) {
      e.stopPropagation();
    }
    document.addEventListener('mousedown', onDocClick);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDocClick);
      document.removeEventListener('keydown', onKey);
    };
  }, [close, bellRef]);

  // Measure the bell against the viewport when the panel renders.
  useEffect(() => {
    positionPanel();
    // Re-anchor on scroll/resize so the panel stays glued to the sticky bell.
    window.addEventListener('scroll', positionPanel, true);
    window.addEventListener('resize', positionPanel);
    return () => {
      window.removeEventListener('scroll', positionPanel, true);
      window.removeEventListener('resize', positionPanel);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  function positionPanel() {
    const bell = bellRef?.current;
    if (!bell) { setAnchor(null); return; }
    const r = bell.getBoundingClientRect();
    const GAP = 6;
    const PAD = 10;
    const spaceBelow = window.innerHeight - r.bottom - PAD;
    const spaceAbove = r.top - PAD;
    // Open upward when there's clearly more room above, otherwise downward.
    const openUp = spaceAbove > spaceBelow && spaceBelow < 320;
    const maxHeight = Math.max(180, (openUp ? spaceAbove : spaceBelow) - GAP);
    const width = Math.min(380, Math.max(320, window.innerWidth - 40));
    const next = {
      left: Math.max(PAD, Math.min(r.right - width, window.innerWidth - width - PAD)),
      top: openUp ? r.top - GAP : r.bottom + GAP,
      maxHeight,
      width,
    };
    setAnchor((prev) => (
      prev
      && Math.abs(prev.left - next.left) < 0.5
      && Math.abs(prev.top - next.top) < 0.5
      && Math.abs(prev.width - next.width) < 0.5
      && Math.abs(prev.maxHeight - next.maxHeight) < 0.5
      ? prev : next
    ));
  }

  const unreadCount = Number(unread.data?.unread || 0);
  const activeRows = active.data?.alerts || [];
  const historyRows = history.data?.alerts || [];

  // Combined list: active first, then any recent history not already shown.
  const rows = useMemo(() => {
    const seen = new Set();
    const out = [];
    for (const a of [...activeRows, ...historyRows]) {
      if (seen.has(a.id)) continue;
      seen.add(a.id);
      out.push(a);
    }
    return out;
  }, [activeRows, historyRows]);

  const unreadRows = rows.filter((a) => !a.read && !a.dismissed);
  const earlierRows = rows.filter((a) => a.read || a.dismissed).slice(0, HISTORY_LIMIT);

  async function handleMarkAllRead() {
    await markAllAlertsRead();
    broadcast();
    reloadAll();
  }

  async function handleRead(id) {
    await markAlertRead(id);
    broadcast();
    reloadAll();
  }

  async function handleDismiss(id) {
    await dismissAlert(id);
    broadcast();
    reloadAll();
  }

  function go(path) {
    close();
    navigate(path);
  }

  const hasUnread = unreadRows.length > 0;
  const hasRows = rows.length > 0;

  return (
    <div
      ref={panelRef}
      className="alerts-dropdown"
      role="dialog"
      aria-label="Notifications"
      style={{
        position: 'fixed',
        left: anchor?.left,
        top: anchor?.top,
        width: anchor?.width,
        maxHeight: anchor?.maxHeight,
      }}
    >
      {anchor ? (
        <>
          <div className="alerts-dropdown__header">
            <div className="alerts-dropdown__title">
              Notifications
              {unreadCount > 0 && (
                <span className="alerts-dropdown__unread-count">{unreadCount} unread</span>
              )}
            </div>
            <button
              type="button"
              className="alerts-dropdown__markall"
              onClick={handleMarkAllRead}
              disabled={!hasUnread}
              title={hasUnread ? 'Mark all alerts as read' : 'No unread alerts'}
            >
              Mark all read
            </button>
          </div>

          <div className="alerts-dropdown__body">
            {active.loading && !hasRows && (
              <div className="alerts-dropdown__status">Loading alerts…</div>
            )}
            {active.error && !hasRows && (
              <div className="alerts-dropdown__status alerts-dropdown__status--error">
                {active.error?.message || 'Failed to load alerts'}
              </div>
            )}
            {!active.loading && !active.error && !hasRows && (
              <div className="alerts-dropdown__status">You’re all caught up.</div>
            )}

            {hasUnread && (
              <div className="alerts-dropdown__section">
                <div className="alerts-dropdown__section-label">Unread</div>
                {unreadRows.map((a) => (
                  <AlertRow
                    key={a.id}
                    alert={a}
                    unread
                    onRead={handleRead}
                    onDismiss={handleDismiss}
                  />
                ))}
              </div>
            )}

            {earlierRows.length > 0 && (
              <div className="alerts-dropdown__section">
                <div className="alerts-dropdown__section-label">Earlier</div>
                {earlierRows.map((a) => (
                  <AlertRow
                    key={a.id}
                    alert={a}
                    onRead={handleRead}
                    onDismiss={handleDismiss}
                  />
                ))}
              </div>
            )}
          </div>

          <div className="alerts-dropdown__footer">
            <button type="button" className="alerts-dropdown__link" onClick={() => go('/alerts')}>
              View all alerts
            </button>
            <button type="button" className="alerts-dropdown__link" onClick={() => go('/settings')}>
              Alert settings
            </button>
          </div>
        </>
      ) : (
        <div className="alerts-dropdown__status">…</div>
      )}
    </div>
  );
}

// AlertRow renders one notification. Unread rows get a severity-tinted left
// indicator + faint unread background; read rows are muted. Per-row actions
// (Read / Dismiss) appear on hover and are always available on touch.
function AlertRow({ alert: a, unread = false, onRead, onDismiss }) {
  return (
    <div className={`alerts-dropdown__row${unread ? ' is-unread' : ''}${a.dismissed ? ' is-dismissed' : ''}`}>
      <SeverityIcon severity={a.severity} />
      <div className="alerts-dropdown__row-main">
        <div className="alerts-dropdown__row-top">
          <span className="alerts-dropdown__row-title">{a.title || 'Alert'}</span>
          <span className="alerts-dropdown__row-time">{formatRelativeTime(a.created_at)}</span>
        </div>
        <div className="alerts-dropdown__row-msg">
          {a.message || a.entity_name || a.entity_id || ''}
        </div>
      </div>
      <div className="alerts-dropdown__row-actions">
        {!a.read && !a.dismissed && (
          <button type="button" onClick={() => onRead(a.id)} title="Mark as read">Read</button>
        )}
        {!a.dismissed && (
          <button type="button" onClick={() => onDismiss(a.id)} title="Dismiss">Dismiss</button>
        )}
      </div>
    </div>
  );
}

// SeverityIcon renders a small severity-tinted glyph so operators can scan the
// feed at a glance without reading badges.
function SeverityIcon({ severity }) {
  const cls = severity === 'critical' ? 'is-critical' : severity === 'warning' ? 'is-warning' : '';
  return (
    <span className={`alerts-dropdown__severity ${cls}`} aria-hidden>
      {severity === 'critical' ? <CriticalIcon /> : <InfoIcon />}
    </span>
  );
}

function InfoIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round">
      <circle cx="8" cy="8" r="6.5" />
      <path d="M8 7.5v4" /><circle cx="8" cy="5" r="0.6" fill="currentColor" stroke="none" />
    </svg>
  );
}

function CriticalIcon() {
  return (
    <svg viewBox="0 0 16 16" fill="none" stroke="currentColor" strokeWidth="1.5" strokeLinejoin="round" strokeLinecap="round">
      <path d="M8 1.5 15 14H1L8 1.5z" />
      <path d="M8 6v3.5" /><circle cx="8" cy="11.4" r="0.6" fill="currentColor" stroke="none" />
    </svg>
  );
}
