import React, { useEffect, useRef } from 'react';
import { prettyJson, rawJson } from './jsonFormat.js';

// InspectorDrawer — right-side slide-in panel with three tabs:
// Outgoing (last request body), Incoming (last response + headers), Headers.
// Pretty/Raw toggle switches the formatter. Copy uses navigator.clipboard.
// Traps focus while open and closes on Escape.
//
// Props:
//   open:        bool — whether the drawer is visible.
//   view:        { outgoing, incoming, headers } | null
//   activeTab:   'outgoing' | 'incoming' | 'headers'
//   onClose:     () => void
//   onTabChange: (tab) => void
//   pretty:      bool — Pretty (default) or Raw
//   onPrettyToggle: () => void
export default function InspectorDrawer({
  open,
  view,
  activeTab = 'outgoing',
  onClose,
  onTabChange,
  pretty = true,
  onPrettyToggle,
}) {
  const panelRef = useRef(null);
  const closeRef = useRef(null);

  useEffect(() => {
    if (!open) return undefined;
    // Move focus into the dialog so keyboard users land in the right place.
    closeRef.current?.focus();
    const onKeyDown = (e) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        onClose?.();
        return;
      }
      if (e.key !== 'Tab' || !panelRef.current) return;
      const focusable = panelRef.current.querySelectorAll(
        'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])',
      );
      if (focusable.length === 0) return;
      const first = focusable[0];
      const last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [open, onClose]);

  if (!open) return null;
  const payload = view?.[activeTab] ?? null;
  const text = pretty ? prettyJson(payload) : rawJson(payload);

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
    } catch {
      /* clipboard unavailable — silently ignore */
    }
  };

  return (
    <>
      <div className="playground-drawer__backdrop" onClick={onClose} />
      <aside
        className="playground-drawer playground-drawer--open"
        role="dialog"
        aria-modal="true"
        aria-label="Raw inspector"
        ref={panelRef}
      >
        <div className="playground-drawer__header">
          <strong style={{ fontSize: 12 }}>Inspector</strong>
          <div style={{ display: 'flex', gap: 6 }}>
            <button type="button" onClick={onPrettyToggle} aria-pressed={pretty}>
              {pretty ? 'Pretty' : 'Raw'}
            </button>
            <button type="button" onClick={copy}>Copy</button>
            <button type="button" onClick={onClose} aria-label="Close inspector" ref={closeRef}>×</button>
          </div>
        </div>
        <div className="playground-drawer__tabs" role="tablist">
          {['outgoing', 'incoming', 'headers'].map((tab) => (
            <button
              key={tab}
              type="button"
              role="tab"
              aria-selected={activeTab === tab}
              aria-controls="playground-inspector-panel"
              className={activeTab === tab ? 'is-active' : ''}
              onClick={() => onTabChange?.(tab)}
            >
              {tab}
            </button>
          ))}
        </div>
        <div className="playground-drawer__content" id="playground-inspector-panel" role="tabpanel">
          <pre className="playground-pre">{text}</pre>
        </div>
      </aside>
    </>
  );
}
