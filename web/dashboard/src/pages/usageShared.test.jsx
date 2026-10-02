// Component-level tests for the captured request/response section shown in
// the usage event detail modal (EventBodiesSection / EventBodiesContent).
//
// The dashboard has no DOM test harness; following the ModelPicker /
// ManagementLoginSecurityPage convention we render the presentational
// component with react-dom/server. EventBodiesSection itself fetches via
// useAsync (effects do not run under renderToStaticMarkup), so the data-driven
// assertions target EventBodiesContent, which receives the loaded payload as
// props. A smoke test covers the section's loading shell and proves the
// component is exported.

import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { EventBodiesContent, EventBodiesSection } from './usageShared.jsx';

const FULL = {
  available: true,
  reason: '',
  provider: 'openai',
  upstream_provider_id: 3,
  captured_at: '2026-10-02T12:00:00Z',
  truncated: false,
  client_request: {
    headers: { 'content-type': ['application/json'] },
    body: '{"hello":"world"}',
  },
  client_response: {
    headers: {},
    body: '{"ok":true}',
  },
  upstream_request: '{"model":"gpt-4o"}',
  upstream_response: '{"id":"chatcmpl-1"}',
};

test('EventBodiesContent renders the four labeled body blocks', () => {
  const html = renderToStaticMarkup(
    <EventBodiesContent data={FULL} loading={false} error={null} />,
  );
  assert.match(html, /Request &amp; Response/);
  assert.match(html, /Client request/);
  assert.match(html, /Upstream request/);
  assert.match(html, /Client response/);
  assert.match(html, /Upstream response/);
});

test('EventBodiesContent pretty-prints a JSON body into the markup', () => {
  const html = renderToStaticMarkup(
    <EventBodiesContent data={FULL} loading={false} error={null} />,
  );
  assert.match(html, /hello/);
  assert.match(html, /world/);
});

test('EventBodiesContent marks a truncated capture', () => {
  const html = renderToStaticMarkup(
    <EventBodiesContent data={{ ...FULL, truncated: true }} loading={false} error={null} />,
  );
  assert.match(html, /truncated/);
  assert.match(html, /badge--warn/);
});

test('EventBodiesContent shows the not-captured message when unavailable', () => {
  const html = renderToStaticMarkup(
    <EventBodiesContent
      data={{ available: false, reason: 'capture disabled' }}
      loading={false}
      error={null}
    />,
  );
  assert.match(html, /No request\/response captured/);
  assert.doesNotMatch(html, /Client request/);
});

test('EventBodiesContent surfaces a fetch error', () => {
  const html = renderToStaticMarkup(
    <EventBodiesContent data={null} loading={false} error={new Error('boom')} />,
  );
  assert.match(html, /boom/);
});

test('EventBodiesSection renders its loading shell', () => {
  const html = renderToStaticMarkup(<EventBodiesSection id={1} />);
  assert.match(html, /Request &amp; Response/);
  assert.match(html, /Loading captured payloads/);
});