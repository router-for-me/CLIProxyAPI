import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { errorLogViewerReducer, type ErrorLogViewerState } from '../src/pages/hooks/errorLogViewer';
import { createLogRequestGuard } from '../src/pages/hooks/logRequests';

const item = { name: 'example-error.log', size: 32, modified: 1700000000 };
const closed: ErrorLogViewerState = { status: 'closed' };

describe('error log viewer state machine', () => {
  test('open, load and close are atomic transitions with no leftover fields', () => {
    const loading = errorLogViewerReducer(closed, { type: 'open', item });
    expect(loading).toEqual({ status: 'loading', item });
    const ready = errorLogViewerReducer(loading, { type: 'ready', text: 'sample log' });
    expect(ready).toEqual({ status: 'ready', item, text: 'sample log' });
    expect(errorLogViewerReducer(ready, { type: 'close' })).toEqual(closed);
  });

  test('empty content is a successful ready state, not an error or closed viewer', () => {
    const loading = errorLogViewerReducer(closed, { type: 'open', item });
    expect(errorLogViewerReducer(loading, { type: 'ready', text: '' })).toEqual({
      status: 'ready',
      item,
      text: '',
    });
  });

  test('failure retains metadata, and reopening clears the error before loading', () => {
    const loading = errorLogViewerReducer(closed, { type: 'open', item });
    const failed = errorLogViewerReducer(loading, { type: 'error', message: 'Unavailable' });
    expect(failed).toEqual({ status: 'error', item, message: 'Unavailable' });
    expect(errorLogViewerReducer(failed, { type: 'open', item })).toEqual(loading);
  });

  test('switching files clears previous content', () => {
    const ready: ErrorLogViewerState = { status: 'ready', item, text: 'previous content' };
    const next = { name: 'another-error.log' };
    expect(errorLogViewerReducer(ready, { type: 'open', item: next })).toEqual({
      status: 'loading',
      item: next,
    });
  });

  test('terminal states do not accept results without a new open transition', () => {
    const states: ErrorLogViewerState[] = [
      closed,
      { status: 'ready', item, text: 'current content' },
      { status: 'error', item, message: 'current error' },
    ];
    for (const state of states) {
      expect(errorLogViewerReducer(state, { type: 'ready', text: 'late content' })).toBe(state);
      expect(errorLogViewerReducer(state, { type: 'error', message: 'late error' })).toBe(state);
    }
  });

  test('request ownership prevents a late file A from completing file B', () => {
    const guard = createLogRequestGuard();
    let state = errorLogViewerReducer(closed, { type: 'open', item });
    const first = guard.invalidate();
    const secondItem = { name: 'second-error.log' };
    state = errorLogViewerReducer(state, { type: 'open', item: secondItem });
    const second = guard.invalidate();
    if (guard.isCurrent(first)) {
      state = errorLogViewerReducer(state, { type: 'ready', text: 'obsolete' });
    }
    expect(state).toEqual({ status: 'loading', item: secondItem });
    if (guard.isCurrent(second)) {
      state = errorLogViewerReducer(state, { type: 'ready', text: 'current' });
    }
    expect(state).toEqual({ status: 'ready', item: secondItem, text: 'current' });
  });

  test('close invalidates a request even while response text is still decoding', async () => {
    const guard = createLogRequestGuard();
    let state = errorLogViewerReducer(closed, { type: 'open', item });
    const request = guard.invalidate();
    let resolve!: (text: string) => void;
    const decoding = new Promise<string>((res) => {
      resolve = res;
    });
    const completion = decoding.then((text) => {
      if (guard.isCurrent(request)) state = errorLogViewerReducer(state, { type: 'ready', text });
    });
    guard.invalidate();
    state = errorLogViewerReducer(state, { type: 'close' });
    resolve('late decoded content');
    await completion;
    expect(state).toEqual(closed);
  });
});

test('viewer UI derives controls and content from a single discriminated state', () => {
  const source = readFileSync(new URL('../src/pages/LogsPage.tsx', import.meta.url), 'utf8');
  expect(source).toContain('useReducer(errorLogViewerReducer');
  expect(source).not.toContain('setSelectedErrorLog');
  expect(source).toContain("open={errorLogViewer.status !== 'closed'}");
  expect(source).toContain("disabled={errorLogViewer.status !== 'ready' || !errorLogViewer.text}");
  expect(source).toContain(
    "disabled={errorLogViewer.status === 'closed' || errorLogViewer.status === 'loading'}"
  );
  expect(source).toContain("errorLogViewer.status === 'ready'");
  expect(source).toContain("errorLogViewer.status === 'error'");
});
