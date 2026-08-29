import React from 'react';
import { prettyJson, rawJson } from './jsonFormat.js';

// InspectorDrawer — right-side slide-in panel with three tabs:
// Outgoing (last request body), Incoming (last response), Headers.
// Pretty/Raw toggle switches the formatter. Copy uses navigator.clipboard.
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
      <aside className="playground-drawer playground-drawer--open" role="dialog" aria-label="Raw inspector">
        <div className="playground-drawer__header">
          <strong style={{ fontSize: 12 }}>Inspector</strong>
          <div style={{ display: 'flex', gap: 6 }}>
            <button type="button" onClick={onPrettyToggle} aria-pressed={pretty}>
              {pretty ? 'Pretty' : 'Raw'}
            </button>
            <button type="button" onClick={copy}>Copy</button>
            <button type="button" onClick={onClose} aria-label="Close inspector">×</button>
          </div>
        </div>
        <div className="playground-drawer__tabs" role="tablist">
          {['outgoing', 'incoming', 'headers'].map((tab) => (
            <button
              key={tab}
              type="button"
              role="tab"
              aria-selected={activeTab === tab}
              className={activeTab === tab ? 'is-active' : ''}
              onClick={() => onTabChange?.(tab)}
            >
              {tab}
            </button>
          ))}
        </div>
        <div className="playground-drawer__content">
          <pre className="playground-pre">{text}</pre>
        </div>
      </aside>
    </>
  );
}
