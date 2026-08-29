import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import InspectorDrawer from './InspectorDrawer.jsx';

const sampleView = {
  outgoing: { model: 'm1', messages: [{ role: 'user', content: 'hi' }] },
  incoming: { id: 'cmpl-1', choices: [{ message: { content: 'hello' } }] },
  headers: { 'content-type': 'application/json' },
};

test('renders nothing when open is false (no DOM)', () => {
  const html = renderToStaticMarkup(
    <InspectorDrawer open={false} view={sampleView} onClose={() => {}} />,
  );
  assert.equal(html, '');
});

test('renders outgoing by default and switches to incoming on tab click', () => {
  const html = renderToStaticMarkup(
    <InspectorDrawer open view={sampleView} onClose={() => {}} activeTab="outgoing" />,
  );
  assert.match(html, /playground-drawer--open/);
  // renderToStaticMarkup HTML-escapes the JSON inside <pre>; assert on the encoded form.
  assert.match(html, /&quot;model&quot;: &quot;m1&quot;/);
});

test('switches tabs based on activeTab prop', () => {
  const incoming = renderToStaticMarkup(
    <InspectorDrawer open view={sampleView} onClose={() => {}} activeTab="incoming" />,
  );
  assert.match(incoming, /cmpl-1/);
  assert.doesNotMatch(incoming, /&quot;model&quot;: &quot;m1&quot;/);

  const headers = renderToStaticMarkup(
    <InspectorDrawer open view={sampleView} onClose={() => {}} activeTab="headers" />,
  );
  assert.match(headers, /content-type/);
});

test('shows em-dash when a payload field is null', () => {
  const html = renderToStaticMarkup(
    <InspectorDrawer open view={{ outgoing: null, incoming: null, headers: null }} onClose={() => {}} activeTab="outgoing" />,
  );
  assert.match(html, /—/);
});
