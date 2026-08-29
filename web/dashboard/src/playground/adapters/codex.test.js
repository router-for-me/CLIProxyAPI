import test from 'node:test';
import assert from 'node:assert/strict';
import { codexAdapter } from './codex.js';

const history = [
  { role: 'user', content: 'hi' },
  { role: 'assistant', content: 'hello' },
];
const params = { temperature: 0.7, max_tokens: 512, stream: true };

test('buildRequest maps to Responses API input + model', () => {
  const body = codexAdapter.buildRequest('gpt-4o', history, params);
  assert.equal(body.model, 'gpt-4o');
  assert.equal(body.stream, true);
  assert.equal(body.temperature, 0.7);
  assert.equal(body.max_output_tokens, 512);
  // Codex Responses API takes a flat `input` string; for v1 we join the
  // history with role tags so multi-turn works.
  assert.match(body.input, /user: hi/);
  assert.match(body.input, /assistant: hello/);
});

test('parseStreamChunk extracts a text token from a Responses SSE delta', () => {
  const line = 'data: {"type":"response.output_text.delta","delta":"yo"}';
  const out = codexAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'yo');
  assert.equal(out.done, false);
});

test('parseStreamChunk signals done on response.completed', () => {
  const line = 'data: {"type":"response.completed"}';
  const out = codexAdapter.parseStreamChunk(line);
  assert.equal(out.token, '');
  assert.equal(out.done, true);
});
