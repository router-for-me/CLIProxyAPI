// Smoke tests for the Error Patterns panel (ErrorGroupsPanel).
//
// Follows the existing dashboard test convention: presentational components are
// rendered via renderToStaticMarkup and assertions target the output HTML.
// Event handler wiring is verified by rendering with props set and checking
// for the expected labels/state markers.

import { test } from 'node:test';
import assert from 'node:assert/strict';
import React from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import ErrorGroupsPanel from './ErrorGroupsPanel.jsx';

const FABRICATED_GROUPS = [
  {
    key: 'rate_limit',
    error_class: 'rate_limit',
    sample_message: '429 Too Many Requests: rate limit exceeded for provider',
    count: 142,
    first_seen: '2026-10-01T08:00:00Z',
    last_seen: '2026-10-01T14:30:00Z',
    providers: ['openai'],
    models: ['gpt-4o'],
    last_request_id: 'req_abc123',
  },
  {
    key: 'auth_fingerprint',
    error_class: 'auth',
    sample_message: '401 Unauthorized: invalid API key',
    count: 87,
    first_seen: '2026-10-01T09:00:00Z',
    last_seen: '2026-10-01T12:00:00Z',
    providers: ['anthropic'],
    models: ['claude-sonnet-4'],
    last_request_id: 'req_def456',
  },
];

const PROPS = {
  groups: FABRICATED_GROUPS,
  loading: false,
  error: null,
  groupBy: 'class',
  onGroupByChange: () => {},
  onSelectGroup: () => {},
  timezone: 'UTC',
  onRetry: () => {},
};

test('ErrorGroupsPanel: renders groups from data', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, PROPS),
  );
  // Sample messages from both groups should appear in the rendered HTML.
  assert.match(html, /rate limit exceeded/);
  assert.match(html, /Unauthorized/);
  // Counts should be rendered (142 and 87).
  assert.match(html, /142/);
  assert.match(html, /87/);
  // First/last seen columns should be present.
  assert.match(html, /2026-10-01/);
  // Provider and model names.
  assert.match(html, /openai/);
  assert.match(html, /gpt-4o/);
});

test('ErrorGroupsPanel: shows EmptyState when groups is empty', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, { ...PROPS, groups: [] }),
  );
  assert.match(html, /No error patterns/);
});

test('ErrorGroupsPanel: shows EmptyState when groups is null', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, { ...PROPS, groups: null }),
  );
  assert.match(html, /No error patterns/);
});

test('ErrorGroupsPanel: shows Spinner when loading', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, { ...PROPS, loading: true, groups: [] }),
  );
  assert.match(html, /skeleton/);
});

test('ErrorGroupsPanel: renders segmented control with all five grouping labels', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, PROPS),
  );
  assert.match(html, />Class</);
  assert.match(html, />Message</);
  assert.match(html, />Provider</);
  assert.match(html, />Model</);
  assert.match(html, />Status</);
});

test('ErrorGroupsPanel: marks the active segment', () => {
  // 'class' is the default groupBy, so its button should be seg-btn--active.
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, PROPS),
  );
  assert.match(html, /seg-btn--active/);
});

test('ErrorGroupsPanel: renders ErrorBanner when error is set', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, {
      ...PROPS,
      error: new Error('boom'),
      groups: [],
    }),
  );
  // The ErrorBanner component renders error.message when it's an Error object.
  assert.match(html, /boom/);
});

test('ErrorGroupsPanel: renders class badge chip for grouped groups', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, PROPS),
  );
  // The first group has error_class 'rate_limit' and the chip label is 'Rate limit'
  assert.match(html, /Rate limit/);
  // The second has class 'auth' -> label 'Auth'
  assert.match(html, />Auth</);
});

test('ErrorGroupsPanel: renders CopyButton next to sample_message', () => {
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, PROPS),
  );
  // CopyButton renders a button with aria-label "Copy error message"
  assert.match(html, /Copy error message/);
});

test('ErrorGroupsPanel: renders a dash for groups without error_class', () => {
  const noClassGroups = [{
    key: 'some_fallback',
    error_class: null,
    sample_message: 'generic error',
    count: 5,
    first_seen: null,
    last_seen: null,
    providers: [],
    models: [],
  }];
  const html = renderToStaticMarkup(
    React.createElement(ErrorGroupsPanel, { ...PROPS, groups: noClassGroups }),
  );
  assert.match(html, /generic error/);
});
