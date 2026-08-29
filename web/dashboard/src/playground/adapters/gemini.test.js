import test from 'node:test';
import assert from 'node:assert/strict';
import { geminiAdapter } from './gemini.js';

const history = [
  { role: 'system', content: 'be brief' },
  { role: 'user', content: 'hi' },
];
const params = { temperature: 0.3, max_tokens: 200, top_p: 0.9, stream: true };

test('endpoint uses streamGenerateContent when streaming is enabled', () => {
  assert.equal(
    geminiAdapter.endpoint('gemini-1.5-pro', { stream: true }),
    '/v1beta/models/gemini-1.5-pro:streamGenerateContent?alt=sse',
  );
});

test('endpoint uses generateContent when streaming is disabled', () => {
  assert.equal(
    geminiAdapter.endpoint('gemini-1.5-pro', { stream: false }),
    '/v1beta/models/gemini-1.5-pro:generateContent',
  );
});

test('buildRequest converts messages to Gemini contents[] with systemInstruction', () => {
  const body = geminiAdapter.buildRequest('gemini-1.5-pro', history, params);
  assert.equal(body.model, undefined); // model is in the URL, not the body
  assert.deepEqual(body.systemInstruction, { parts: [{ text: 'be brief' }] });
  assert.deepEqual(body.contents, [{ role: 'user', parts: [{ text: 'hi' }] }]);
  assert.equal(body.generationConfig.temperature, 0.3);
  assert.equal(body.generationConfig.maxOutputTokens, 200);
  assert.equal(body.generationConfig.topP, 0.9);
});

test('buildRequest maps assistant role to model role', () => {
  const body = geminiAdapter.buildRequest('m', [
    { role: 'user', content: 'q' },
    { role: 'assistant', content: 'a' },
    { role: 'user', content: 'q2' },
  ], {});
  assert.deepEqual(body.contents, [
    { role: 'user', parts: [{ text: 'q' }] },
    { role: 'model', parts: [{ text: 'a' }] },
    { role: 'user', parts: [{ text: 'q2' }] },
  ]);
});

test('parseStreamChunk extracts a text token from a Gemini SSE data line', () => {
  const line = 'data: {"candidates":[{"content":{"parts":[{"text":"hello"}]}}]}';
  const out = geminiAdapter.parseStreamChunk(line);
  assert.equal(out.token, 'hello');
  assert.equal(out.done, false);
});
