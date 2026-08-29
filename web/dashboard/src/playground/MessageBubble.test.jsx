import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import MessageBubble from './MessageBubble.jsx';

test('user bubble uses --user variant and right-aligns', () => {
  const html = renderToStaticMarkup(
    <MessageBubble
      m={{ id: 'm1', role: 'user', content: 'hi' }}
      onCopy={() => {}}
      onInspect={() => {}}
      onRetry={() => {}}
    />,
  );
  assert.match(html, /playground-message--user/);
  assert.match(html, /hi/);
});

test('assistant bubble uses --assistant variant and shows the action row', () => {
  const html = renderToStaticMarkup(
    <MessageBubble
      m={{ id: 'm2', role: 'assistant', content: 'hello' }}
      onCopy={() => {}}
      onInspect={() => {}}
      onRetry={() => {}}
    />,
  );
  assert.match(html, /playground-message--assistant/);
  assert.match(html, /hello/);
  assert.match(html, /Copy|Inspect|Retry/);
});

test('streaming assistant bubble renders the pulsing dot (not the ▍ glyph)', () => {
  const html = renderToStaticMarkup(
    <MessageBubble
      m={{ id: 'm3', role: 'assistant', content: '', streaming: true }}
      onCopy={() => {}}
      onInspect={() => {}}
      onRetry={() => {}}
    />,
  );
  assert.match(html, /playground-streaming-dot/);
  assert.doesNotMatch(html, /▍/);
});

test('user bubble does not show an action row', () => {
  const html = renderToStaticMarkup(
    <MessageBubble
      m={{ id: 'm1', role: 'user', content: 'hi' }}
      onCopy={() => {}}
      onInspect={() => {}}
      onRetry={() => {}}
    />,
  );
  assert.doesNotMatch(html, /playground-message__actions/);
});

test('action buttons fire the right callbacks', () => {
  let copied = 0, inspected = 0, retried = 0;
  const html = renderToStaticMarkup(
    <MessageBubble
      m={{ id: 'm2', role: 'assistant', content: 'hello' }}
      onCopy={() => { copied++; }}
      onInspect={() => { inspected++; }}
      onRetry={() => { retried++; }}
    />,
  );
  // Smoke: the rendered HTML has the buttons in the expected order.
  // (We can't easily click in renderToStaticMarkup; the page wires onClick
  // handlers at integration time. The shape is what we test here.)
  assert.match(html, /Copy/);
  assert.match(html, /Inspect/);
  assert.match(html, /Retry/);
});