import test from 'node:test';
import assert from 'node:assert/strict';
import { openaiCompatAdapter } from './openaiCompat.js';

const history = [
  { role: 'system', content: 'be brief' },
  { role: 'user', content: 'hi' },
];
const params = { temperature: 0.2, max_tokens: 100, top_p: 1, stream: true };

test('buildRequest flattens messages and merges params', () => {
  const body = openaiCompatAdapter.buildRequest('gpt-4o', history, params);
  assert.equal(body.model, 'gpt-4o');
  assert.equal(body.stream, true);
  assert.equal(body.temperature, 0.2);
  assert.equal(body.max_tokens, 100);
  assert.equal(body.top_p, 1);
  assert.deepEqual(body.messages, history);
});

test('buildRequest omits undefined params', () => {
  const body = openaiCompatAdapter.buildRequest('m', [{ role: 'user', content: 'x' }], {});
  assert.equal('temperature' in body, false);
  assert.equal('max_tokens' in body, false);
  assert.equal('top_p' in body, false);
});

test('parseStreamChunk extracts a content token from an OpenAI SSE delta', () => {
  const line = 'data: {"choices":[{"delta":{"content":"hello"}}]}';
  const out = openaiCompatAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'hello');
  assert.equal(out.done, false);
});

test('parseStreamChunk signals done on the [DONE] sentinel', () => {
  const out = openaiCompatAdapter.parseStreamChunk('data: [DONE]');
  assert.equal(out.token, '');
  assert.equal(out.done, true);
});

test('parseStreamChunk returns empty on non-data lines', () => {
  const out = openaiCompatAdapter.parseStreamChunk(': heartbeat');
  assert.equal(out.token, '');
  assert.equal(out.done, false);
});
