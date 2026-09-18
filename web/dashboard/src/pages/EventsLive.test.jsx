// Unit tests for the Live events tab (Round-2 OmniRoute — Workstream 3).
//
// The dashboard follows the LogsPage / Quota convention: the React render
// path is exercised manually + by `npm run build`, and node:test covers
// the pure helpers exported from EventsLive.jsx. Those helpers cover:
//   - URL building for the SSE stream + polling fallbacks
//   - SSE frame parsing (the wire format is text/event-stream lines)
//   - Event buffer rotation (cap at 500 newest entries)
//   - Filter mapping (UI filter shape -> query string params)
//   - Status derivation (sseError + pollError -> 'live' | 'polling' | 'error')

import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildEventsStreamURL,
  buildEventsPollingParams,
  parseSSEFrame,
  appendEventCapped,
  eventsFilterQuery,
  eventsStreamStatus,
} from './EventsLive.jsx';

// --- buildEventsStreamURL --------------------------------------------------

test('buildEventsStreamURL: root path when no filters are set', () => {
  const url = buildEventsStreamURL({ type: '', auth: '' });
  assert.equal(url, '/v0/management/events/stream');
});

test('buildEventsStreamURL: appends only the non-empty filter params', () => {
  const url = buildEventsStreamURL({ type: 'routing.decision', auth: 'openai#0' });
  assert.equal(url, '/v0/management/events/stream?type=routing.decision&auth=openai%230');
});

test('buildEventsStreamURL: encodes special characters in filter values', () => {
  const url = buildEventsStreamURL({ type: 'breaker.tripped', auth: 'a/b c' });
  assert.ok(url.includes('type=breaker.tripped'));
  assert.ok(url.includes('auth=a%2Fb+c') || url.includes('auth=a%2Fb%20c'));
});

// --- buildEventsPollingParams ---------------------------------------------

test('buildEventsPollingParams: serializes filter + limit into URLSearchParams', () => {
  const qs = buildEventsPollingParams({ type: 'routing.decision', auth: 'openai#0' }, 100);
  assert.equal(qs.get('type'), 'routing.decision');
  assert.equal(qs.get('auth'), 'openai#0');
  assert.equal(qs.get('limit'), '100');
});

test('buildEventsPollingParams: omits empty filter values but keeps limit', () => {
  const qs = buildEventsPollingParams({ type: '', auth: '' }, 250);
  assert.equal(qs.get('type'), null);
  assert.equal(qs.get('auth'), null);
  assert.equal(qs.get('limit'), '250');
});

test('buildEventsPollingParams: clamps non-positive limits to 1', () => {
  assert.equal(buildEventsPollingParams({}, 0).get('limit'), '1');
  assert.equal(buildEventsPollingParams({}, -50).get('limit'), '1');
});

// --- parseSSEFrame --------------------------------------------------------

test('parseSSEFrame: parses a one-line data frame', () => {
  const frame = parseSSEFrame('data: {"type":"routing.decision","ts":"2026-09-17T10:00:00Z","auth_id":"openai#0","payload":{"model":"gpt-5"}}');
  assert.deepEqual(frame, {
    type: 'message',
    data: {
      type: 'routing.decision',
      ts: '2026-09-17T10:00:00Z',
      auth_id: 'openai#0',
      payload: { model: 'gpt-5' },
    },
  });
});

test('parseSSEFrame: parses a multi-line data frame (joined by newline)', () => {
  const frame = parseSSEFrame('data: {"type":"breaker.tripped",\ndata: "ts":"2026-09-17T10:00:00Z"}');
  assert.equal(frame.type, 'message');
  assert.equal(frame.data.type, 'breaker.tripped');
  assert.equal(frame.data.ts, '2026-09-17T10:00:00Z');
});

test('parseSSEFrame: parses an explicit event name', () => {
  const frame = parseSSEFrame('event: routing.cooldown_wait\ndata: {"type":"routing.cooldown_wait"}');
  assert.equal(frame.type, 'routing.cooldown_wait');
  assert.equal(frame.data.type, 'routing.cooldown_wait');
});

test('parseSSEFrame: returns null for keep-alive comments', () => {
  assert.equal(parseSSEFrame(': keep-alive'), null);
  assert.equal(parseSSEFrame(':heartbeat'), null);
});

test('parseSSEFrame: returns null for empty input', () => {
  assert.equal(parseSSEFrame(''), null);
  assert.equal(parseSSEFrame('\n\n'), null);
});

test('parseSSEFrame: returns null when data is not valid JSON', () => {
  assert.equal(parseSSEFrame('data: not-json{'), null);
});

// --- appendEventCapped ----------------------------------------------------

test('appendEventCapped: prepends new events (newest first)', () => {
  const out = appendEventCapped([{ id: 1 }, { id: 2 }], { id: 3 }, 500);
  assert.deepEqual(out.map((e) => e.id), [3, 1, 2]);
});

test('appendEventCapped: caps the buffer at the given limit', () => {
  const start = Array.from({ length: 499 }, (_, i) => ({ id: i }));
  const out = appendEventCapped(start, { id: 999 }, 500);
  assert.equal(out.length, 500);
  assert.equal(out[0].id, 999);
  assert.equal(out[499].id, 498);
});

test('appendEventCapped: handles empty starting buffer', () => {
  const out = appendEventCapped([], { id: 7 }, 500);
  assert.deepEqual(out, [{ id: 7 }]);
});

// --- eventsFilterQuery ----------------------------------------------------

test('eventsFilterQuery: returns canonical query object with trimmed strings', () => {
  const q = eventsFilterQuery({ type: '  routing.decision ', auth: ' openai#0 ' });
  assert.deepEqual(q, { type: 'routing.decision', auth: 'openai#0' });
});

test('eventsFilterQuery: returns empty strings for blank input', () => {
  const q = eventsFilterQuery({ type: '   ', auth: undefined });
  assert.equal(q.type, '');
  assert.equal(q.auth, '');
});

// --- eventsStreamStatus ---------------------------------------------------

test('eventsStreamStatus: live when SSE is healthy', () => {
  assert.equal(eventsStreamStatus({ sseError: false, pollError: null }), 'live');
});

test('eventsStreamStatus: polling when SSE fell back and polling has not errored', () => {
  assert.equal(eventsStreamStatus({ sseError: true, pollError: null }), 'polling');
});

test('eventsStreamStatus: error when polling has failed', () => {
  const err = new Error('boom');
  assert.equal(eventsStreamStatus({ sseError: true, pollError: err }), 'error');
});
