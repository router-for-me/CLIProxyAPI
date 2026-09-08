import test from 'node:test';
import assert from 'node:assert/strict';
import { parseLogLine, matchesFilter } from './LogsPage.jsx';

// --- parseLogLine -----------------------------------------------------------
// Lines follow the LogFormatter from internal/logging/global_logger.go:
//   [2025-12-23 20:14:04] [reqID] [level] [file:line] message fields
// The caller segment is optional (entries without a caller).

test('parseLogLine: full formatter line with caller', () => {
  const line = '[2025-12-23 20:14:04] [a1b2c3d4] [info ] [manager.go:524] Use API key for model gpt-5.2';
  const parsed = parseLogLine(line);
  assert.equal(parsed.ts, '2025-12-23 20:14:04');
  assert.equal(parsed.reqId, 'a1b2c3d4');
  assert.equal(parsed.level, 'info');
  assert.equal(parsed.caller, 'manager.go:524');
  assert.equal(parsed.msg, 'Use API key for model gpt-5.2');
});

test('parseLogLine: line without caller segment', () => {
  const line = '[2025-12-23 20:14:04] [--------] [warn ] no caller here';
  const parsed = parseLogLine(line);
  assert.equal(parsed.ts, '2025-12-23 20:14:04');
  assert.equal(parsed.level, 'warn');
  assert.equal(parsed.caller, '');
  assert.equal(parsed.msg, 'no caller here');
});

test('parseLogLine: level padding whitespace is trimmed', () => {
  const parsed = parseLogLine('[ts] [id] [error] boom');
  assert.equal(parsed.level, 'error');
});

test('parseLogLine: message containing bracketed segments stays intact', () => {
  const line = '[ts] [id] [info ] done [extra] [bits] tail';
  const parsed = parseLogLine(line);
  assert.equal(parsed.msg, 'done [extra] [bits] tail');
});

test('parseLogLine: panic level parsed', () => {
  const parsed = parseLogLine('[ts] [id] [panic] something exploded');
  assert.equal(parsed.level, 'panic');
});

test('parseLogLine: non-formatter line falls back to other + raw message', () => {
  const parsed = parseLogLine('goroutine 1 [running]:');
  assert.equal(parsed.level, 'other');
  assert.equal(parsed.msg, 'goroutine 1 [running]:');
  assert.equal(parsed.ts, '');
});

test('parseLogLine: empty line falls back', () => {
  const parsed = parseLogLine('');
  assert.equal(parsed.level, 'other');
  assert.equal(parsed.msg, '');
});

// --- matchesFilter ----------------------------------------------------------

const SAMPLE = [
  '[ts1] [id] [info ] request served',
  '[ts2] [id] [warn ] slow upstream',
  '[ts3] [id] [error] upstream failed',
  'raw continuation line',
];

test('matchesFilter: empty filter keeps everything', () => {
  for (const line of SAMPLE) {
    assert.equal(matchesFilter(line, '', ''), true);
  }
});

test('matchesFilter: level filter keeps only matching level', () => {
  assert.equal(matchesFilter(SAMPLE[0], 'info', ''), true);
  assert.equal(matchesFilter(SAMPLE[2], 'info', ''), false);
  assert.equal(matchesFilter(SAMPLE[2], 'error', ''), true);
});

test('matchesFilter: other level keeps non-formatter lines', () => {
  assert.equal(matchesFilter(SAMPLE[3], 'other', ''), true);
  assert.equal(matchesFilter(SAMPLE[0], 'other', ''), false);
});

test('matchesFilter: search is case-insensitive substring on the raw line', () => {
  assert.equal(matchesFilter(SAMPLE[0], '', 'REQUEST'), true);
  assert.equal(matchesFilter(SAMPLE[1], '', 'slow'), true);
  assert.equal(matchesFilter(SAMPLE[1], '', 'fast'), false);
});

test('matchesFilter: level and search combine with AND', () => {
  assert.equal(matchesFilter(SAMPLE[2], 'error', 'upstream'), true);
  assert.equal(matchesFilter(SAMPLE[2], 'warn', 'upstream'), false);
  assert.equal(matchesFilter(SAMPLE[2], 'error', 'missing'), false);
});
