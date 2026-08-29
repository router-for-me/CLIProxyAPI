import test from 'node:test';
import assert from 'node:assert/strict';
import { reducer, initialState, openStream } from './usePlaygroundChat.js';

const baseParams = { temperature: 0.5, stream: true };

// Pre-existing reducer coverage (restored after Task 2's reducer-fields
// task replaced the file rather than appending).

test('initialState has empty messages and no in-flight request', () => {
  assert.deepEqual(initialState.messages, []);
  assert.equal(initialState.inFlight, null);
  assert.equal(initialState.error, null);
});

test('SEND adds a user message and an empty assistant placeholder', () => {
  const s = reducer(initialState, {
    type: 'SEND',
    id: 'req-1',
    model: 'gpt-4o',
    protocol: 'openai-compat',
    userText: 'hi',
    params: baseParams,
  });
  assert.equal(s.messages.length, 2);
  assert.equal(s.messages[0].role, 'user');
  assert.equal(s.messages[0].content, 'hi');
  assert.equal(s.messages[1].role, 'assistant');
  assert.equal(s.messages[1].content, '');
  assert.equal(s.messages[1].streaming, true);
  assert.equal(s.inFlight, 'req-1');
});

test('TOKEN appends to the in-flight assistant message', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'TOKEN', id: 'r1', token: 'hel' });
  const c = reducer(b, { type: 'TOKEN', id: 'r1', token: 'lo' });
  assert.equal(c.messages[1].content, 'hello');
  assert.equal(c.messages[1].streaming, true);
  assert.equal(c.inFlight, 'r1');
});

test('DONE clears streaming, inFlight, and records usage', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'DONE', id: 'r1', usage: { prompt_tokens: 5, completion_tokens: 7 } });
  assert.equal(b.messages[1].streaming, false);
  assert.equal(b.inFlight, null);
  assert.deepEqual(b.messages[1].usage, { prompt_tokens: 5, completion_tokens: 7 });
  assert.equal(b.lastUsage, 'r1');
});

test('ERROR records the error and stops streaming', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'ERROR', id: 'r1', error: { message: 'boom', type: 'upstream' } });
  assert.equal(b.messages[1].streaming, false);
  assert.deepEqual(b.error, { message: 'boom', type: 'upstream' });
  assert.equal(b.inFlight, null);
});

test('CLEAR_ERROR removes the error without affecting messages', () => {
  const a = reducer(initialState, { type: 'SEND', id: 'r1', model: 'm', protocol: 'openai-compat', userText: 'hi', params: baseParams });
  const b = reducer(a, { type: 'ERROR', id: 'r1', error: { message: 'boom' } });
  const c = reducer(b, { type: 'CLEAR_ERROR' });
  assert.equal(c.error, null);
  assert.equal(c.messages.length, 2);
});

// Pre-existing openStream SSE coverage.

test('openStream dispatches onChunk for each SSE event block', async () => {
  const events = [];
  const sseBody = 'data: {"choices":[{"delta":{"content":"hi"}}]}\n\ndata: [DONE]\n\n';
  const fakeBody = new ReadableStream({
    start(controller) {
      controller.enqueue(new TextEncoder().encode(sseBody));
      controller.close();
    },
  });
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({ ok: true, status: 200, body: fakeBody });
  try {
    await openStream({
      url: '/v1/chat/completions',
      init: { method: 'POST' },
      onChunk: (e) => events.push(e),
      onDone: () => events.push('__done__'),
      onError: (e) => events.push(`ERR:${e.message}`),
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(events.length, 3);
  assert.match(events[0], /data: \{"choices":/);
  assert.equal(events[1], 'data: [DONE]');
  assert.equal(events[2], '__done__');
});

test('openStream reports a non-2xx as onError with status and body', async () => {
  let captured;
  const originalFetch = globalThis.fetch;
  globalThis.fetch = async () => ({
    ok: false,
    status: 503,
    text: async () => 'upstream down',
  });
  try {
    await openStream({
      url: '/v1/chat/completions',
      init: {},
      onChunk: () => {},
      onDone: () => { captured = 'DONE'; },
      onError: (e) => { captured = e; },
    });
  } finally {
    globalThis.fetch = originalFetch;
  }
  assert.equal(captured.status, 503);
  assert.equal(captured.type, 'upstream');
  assert.equal(captured.body, 'upstream down');
});

// Task 2: outgoing/incoming on reducer messages.

test('SEND stamps the assistant message with outgoing snapshot', () => {
  const next = reducer(initialState, {
    type: 'SEND',
    id: 'r-1',
    userText: 'hi',
    model: 'm1',
    protocol: 'openai-compat',
    params: { stream: true },
    outgoing: { model: 'm1', messages: [{ role: 'user', content: 'hi' }] },
  });
  const asst = next.messages[next.messages.length - 1];
  assert.equal(asst.role, 'assistant');
  assert.deepEqual(asst.outgoing, { model: 'm1', messages: [{ role: 'user', content: 'hi' }] });
  assert.equal(asst.incoming, undefined);
});

test('DONE stamps the assistant message with incoming snapshot', () => {
  let s = reducer(initialState, {
    type: 'SEND',
    id: 'r-1',
    userText: 'hi',
    model: 'm1',
    protocol: 'openai-compat',
    params: { stream: true },
    outgoing: { model: 'm1', messages: [] },
  });
  s = reducer(s, {
    type: 'DONE',
    id: 'r-1',
    usage: { prompt_tokens: 1, completion_tokens: 2 },
    incoming: { id: 'cmpl-1', choices: [{ message: { role: 'assistant', content: 'hello' } }] },
  });
  const asst = s.messages[s.messages.length - 1];
  assert.equal(asst.streaming, false);
  assert.deepEqual(asst.incoming, { id: 'cmpl-1', choices: [{ message: { role: 'assistant', content: 'hello' } }] });
});

test('ERROR stamps incoming with the error payload', () => {
  let s = reducer(initialState, {
    type: 'SEND',
    id: 'r-1', userText: 'hi', model: 'm1', protocol: 'openai-compat', params: {},
    outgoing: {},
  });
  s = reducer(s, {
    type: 'ERROR',
    id: 'r-1',
    error: { message: 'HTTP 500', type: 'upstream' },
    incoming: { error: 'upstream blew up', code: 500 },
  });
  const asst = s.messages[s.messages.length - 1];
  assert.equal(asst.streaming, false);
  assert.deepEqual(asst.incoming, { error: 'upstream blew up', code: 500 });
});

test('TOKEN and other actions leave outgoing/incoming untouched', () => {
  let s = reducer(initialState, {
    type: 'SEND',
    id: 'r-1', userText: 'hi', model: 'm1', protocol: 'openai-compat', params: {},
    outgoing: { model: 'm1' },
  });
  const before = s.messages[s.messages.length - 1].outgoing;
  s = reducer(s, { type: 'TOKEN', id: 'r-1', token: 'a' });
  s = reducer(s, { type: 'TOKEN', id: 'r-1', token: 'b' });
  const asst = s.messages[s.messages.length - 1];
  assert.deepEqual(asst.outgoing, before);
  assert.equal(asst.incoming, undefined);
});

test('SEND stamps reqId on the assistant message', () => {
  const next = reducer(initialState, {
    type: 'SEND', id: 'r-42', userText: 'hi', model: 'm1', protocol: 'openai-compat', params: {}, outgoing: {},
  });
  const asst = next.messages[next.messages.length - 1];
  assert.equal(asst.reqId, 'r-42');
});
