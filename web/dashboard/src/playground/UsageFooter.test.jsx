import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import UsageFooter from './UsageFooter.jsx';

test('renders token counts and computes cost from the model_pricing shape', () => {
  const html = renderToStaticMarkup(
    <UsageFooter
      lastUsage={{ prompt_tokens: 1_000_000, completion_tokens: 1_000_000 }}
      pricing={{ input_per_1m_usd: 3.0, output_per_1m_usd: 15.0 }}
    />,
  );
  assert.match(html, /prompt:?\s*1000000/);
  assert.match(html, /completion:?\s*1000000/);
  assert.match(html, /\$18\.000000/); // 3 + 15
});

test('accepts a normalized { input, output } shape', () => {
  const html = renderToStaticMarkup(
    <UsageFooter
      lastUsage={{ prompt_tokens: 1_000_000, completion_tokens: 1_000_000 }}
      pricing={{ input: 1, output: 2 }}
    />,
  );
  assert.match(html, /\$3\.000000/);
});

test('falls back to em-dash when pricing is missing', () => {
  const html = renderToStaticMarkup(
    <UsageFooter lastUsage={{ prompt_tokens: 1, completion_tokens: 2 }} pricing={null} />,
  );
  assert.match(html, /—/);
});

test('falls back to em-dash when usage is missing entirely', () => {
  const html = renderToStaticMarkup(
    <UsageFooter lastUsage={null} pricing={null} />,
  );
  assert.match(html, /—/);
});
