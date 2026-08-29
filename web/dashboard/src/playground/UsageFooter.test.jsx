import test from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import UsageFooter from './UsageFooter.jsx';

test('renders token counts from the last message', () => {
  const html = renderToStaticMarkup(
    <UsageFooter
      lastUsage={{ prompt_tokens: 12, completion_tokens: 34 }}
      pricingPerMillion={{ input: 3.0, output: 15.0 }}
    />,
  );
  assert.match(html, /prompt:?\s*12/);
  assert.match(html, /completion:?\s*34/);
  assert.match(html, /\$0\.00[0-9]+/); // some cost string
});

test('falls back to em-dash when pricing is missing', () => {
  const html = renderToStaticMarkup(
    <UsageFooter lastUsage={{ prompt_tokens: 1, completion_tokens: 2 }} pricingPerMillion={null} />,
  );
  assert.match(html, /—/);
});

test('falls back to em-dash when usage is missing entirely', () => {
  const html = renderToStaticMarkup(
    <UsageFooter lastUsage={null} pricingPerMillion={null} />,
  );
  assert.match(html, /—/);
});
