import test from 'node:test';
import assert from 'node:assert/strict';
import { claudeAdapter } from './claude.js';

const history = [
  { role: 'system', content: 'be brief' },
  { role: 'user', content: 'hi' },
  { role: 'assistant', content: 'hello' },
  { role: 'user', content: 'how are you?' },
];
const params = { temperature: 0.4, max_tokens: 256, top_p: 0.95, stream: true };

test('buildRequest moves system messages to top-level system field', () => {
  const body = claudeAdapter.buildRequest('claude-3-5-sonnet', history, params);
  assert.equal(body.model, 'claude-3-5-sonnet');
  assert.equal(body.system, 'be brief');
  assert.equal(body.stream, true);
  assert.equal(body.temperature, 0.4);
  assert.equal(body.max_tokens, 256);
  assert.equal(body.top_p, 0.95);
  assert.deepEqual(body.messages, [
    { role: 'user', content: 'hi' },
    { role: 'assistant', content: 'hello' },
    { role: 'user', content: 'how are you?' },
  ]);
});

test('parseStreamChunk extracts a text token from a content_block_delta event', () => {
  const line = 'event: content_block_delta\ndata: {"type":"content_block_delta","delta":{"type":"text_delta","text":"hi"}}';
  const out = claudeAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'hi');
  assert.equal(out.done, false);
});

test('parseStreamChunk signals done on message_stop', () => {
  const line = 'event: message_stop\ndata: {"type":"message_stop"}';
  const out = claudeAdapter.parseStreamChunk(line);
  assert.equal(out.token, '');
  assert.equal(out.done, true);
});
