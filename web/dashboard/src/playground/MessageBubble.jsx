import React from 'react';
import Markdown from 'react-markdown';
import remarkGfm from 'remark-gfm';

// MessageBubble — renders one chat message.
//   * User:      right-aligned bubble, plain text (no Markdown).
//   * Assistant: left-aligned, Markdown (GFM) rendered, action row
//     (Copy / Inspect / Retry) once streaming finishes.
// Streaming assistant messages show a pulsing dot; it speeds up while the
// request is in flight (caller passes streamingFast=true).
export default function MessageBubble({ m, streamingFast = false, onCopy, onInspect, onRetry }) {
  const isUser = m.role === 'user';
  const variant = isUser ? 'playground-message--user' : 'playground-message--assistant';
  const dotClass = streamingFast
    ? 'playground-streaming-dot playground-streaming-dot--fast'
    : 'playground-streaming-dot';

  const metaParts = [m.role];
  if (m.model) metaParts.push(m.model);
  if (m.protocol) metaParts.push(m.protocol);

  return (
    <div className={`playground-message ${variant}`}>
      <div className="playground-message__col">
        <div className="playground-message__meta">{metaParts.join(' · ')}</div>
        <div className="playground-message__bubble">
          {isUser ? (
            m.content
          ) : (
            <div className="playground-markdown">
              <Markdown remarkPlugins={[remarkGfm]}>{m.content || ''}</Markdown>
            </div>
          )}
          {m.streaming && !isUser && <span className={dotClass} aria-hidden="true">●</span>}
        </div>
        {!isUser && !m.streaming && (
          <div className="playground-message__actions">
            <button type="button" onClick={onCopy}>Copy</button>
            <button type="button" onClick={onInspect}>Inspect</button>
            {onRetry && <button type="button" onClick={onRetry}>Retry</button>}
          </div>
        )}
      </div>
    </div>
  );
}
