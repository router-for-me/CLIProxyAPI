import React from 'react';

// MessageBubble — renders one chat message with the v1 visual accent:
//   * User:   right-aligned, bg-elevated bubble, no action row.
//   * Assistant: left-aligned, full-width border bubble, action row
//     (Copy / Inspect / Retry).
// Streaming assistant messages show a pulsing dot instead of the old
// ▍ glyph. The dot pulses faster while the request is in flight
// (caller passes streamingFast=true).
export default function MessageBubble({ m, streamingFast = false, onCopy, onInspect, onRetry }) {
  const isUser = m.role === 'user';
  const variant = isUser ? 'playground-message--user' : 'playground-message--assistant';
  const dotClass = streamingFast
    ? 'playground-streaming-dot playground-streaming-dot--fast'
    : 'playground-streaming-dot';

  return (
    <div className={`playground-message ${variant}`}>
      <div>
        <div className="playground-message__meta">
          {m.role}
          {m.model ? ` · ${m.model}` : ''}
          {m.protocol ? ` · ${m.protocol}` : ''}
        </div>
        <div className="playground-message__bubble">
          {m.content}
          {m.streaming && !isUser && (
            <span className={dotClass}>●</span>
          )}
        </div>
        {!isUser && !m.streaming && (
          <div className="playground-message__actions">
            <button type="button" onClick={onCopy}>Copy</button>
            <button type="button" onClick={onInspect}>Inspect</button>
            <button type="button" onClick={onRetry}>Retry</button>
          </div>
        )}
      </div>
    </div>
  );
}