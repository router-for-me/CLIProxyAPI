import test from 'node:test';
import assert from 'node:assert/strict';
import { toJsonExport, toMarkdownExport } from './exportConversation.js';

const messages = [
  { role: 'user', content: 'hi', model: 'gpt-4o', protocol: 'openai-compat' },
  { role: 'assistant', content: 'hello!', usage: { prompt_tokens: 3, completion_tokens: 2 } },
];

test('toJsonExport returns a string of JSON with full metadata', () => {
  const out = JSON.parse(toJsonExport(messages));
  assert.equal(out.length, 2);
  assert.equal(out[0].model, 'gpt-4o');
  assert.deepEqual(out[1].usage, { prompt_tokens: 3, completion_tokens: 2 });
});

test('toMarkdownExport renders user and assistant turns with headings', () => {
  const md = toMarkdownExport(messages);
  assert.match(md, /## User/);
  assert.match(md, /## Assistant/);
  assert.match(md, /hi/);
  assert.match(md, /hello!/);
});
