import test from 'node:test';
import assert from 'node:assert/strict';
import { detectProtocol, detectProtocolFromUpstream } from './detectProtocol.js';

test('detectProtocol maps gemini/Google models to gemini', () => {
  assert.equal(detectProtocol({ model: 'gemini-1.5-pro', provider: 'google' }), 'gemini');
});

test('detectProtocol maps claude models to claude', () => {
  assert.equal(detectProtocol({ model: 'claude-3-5-sonnet-20241022' }), 'claude');
});

test('detectProtocol maps gpt/responses models to openai-compat by default', () => {
  assert.equal(detectProtocol({ model: 'gpt-4o' }), 'openai-compat');
});

test('detectProtocolFromUpstream uses provider_type', () => {
  assert.equal(detectProtocolFromUpstream({ provider_type: 'claude' }), 'claude');
  assert.equal(detectProtocolFromUpstream({ provider_type: 'gemini' }), 'gemini');
  assert.equal(detectProtocolFromUpstream({ provider_type: 'openai' }), 'openai-compat');
  assert.equal(detectProtocolFromUpstream({ provider_type: 'openai-compatible' }), 'openai-compat');
});
