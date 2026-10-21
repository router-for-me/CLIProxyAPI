import { describe, expect, test } from 'bun:test';
import { applyLogPage, emptyLogBuffer } from '../src/features/logs/model/logBuffer';
import {
  createLogParserCache,
  filterLogEntries,
  searchLogEntries,
} from '../src/features/logs/model/logSelectors';

const access = (path: string, status = 200) =>
  `[2026-06-15 10:00:00] [abcd1234] [info ] [gin_logger.go:1] ${status} | 1ms | ::1 | POST "${path}"`;
const noFilters = () => ({
  methods: new Set<never>(),
  statuses: new Set<never>(),
  paths: new Set<string>(),
  level: '',
});

describe('log selectors use the whole retained buffer', () => {
  test('finds matches before the initial display window', () => {
    const parse = createLogParserCache();
    const lines = [access('/rare', 500), ...Array.from({ length: 150 }, () => access('/common'))];
    const entries = parse(applyLogPage(emptyLogBuffer(), { lines }));
    const result = filterLogEntries(entries, { ...noFilters(), paths: new Set(['/rare']) });
    expect(result).toHaveLength(1);
    expect(result[0].id).toBe(0);
    expect(searchLogEntries(entries, '/rare', false)).toHaveLength(1);
  });
  test('hides management requests, not application messages mentioning the API', () => {
    const parse = createLogParserCache();
    const text =
      '[2026-06-15 10:00:00] [--------] [info ] Registered /v8/management/config endpoint';
    const entries = parse(
      applyLogPage(emptyLogBuffer(), {
        lines: [access('/v8/management/logs?limit=10'), text, access('/v8/management-other')],
      })
    );
    expect(searchLogEntries(entries, '', true).map((entry) => entry.raw)).toEqual([
      text,
      access('/v8/management-other'),
    ]);
  });
  test('retains object identity for surviving records but not duplicate IDs', () => {
    const parse = createLogParserCache();
    let state = applyLogPage(emptyLogBuffer(), { lines: ['same'], nextCursor: 'a' });
    const first = parse(state);
    state = applyLogPage(state, { lines: ['same'], nextCursor: 'b' }, 'a');
    const next = parse(state);
    expect(next[0]).toBe(first[0]);
    expect(next[1]).not.toBe(first[0]);
    expect(next.map((entry) => entry.id)).toEqual([0, 1]);
  });
  test('ordinary UUID search remains literal instead of matching short IDs', () => {
    const entries = createLogParserCache()(
      applyLogPage(emptyLogBuffer(), { lines: [access('/v1/responses')] })
    );
    expect(searchLogEntries(entries, '019a0000-0000-7000-8000-0000abcd1234', false)).toEqual([]);
    expect(searchLogEntries(entries, 'abcd1234', false)).toHaveLength(1);
  });
  test('combines level and status filters without mutating entries', () => {
    const entries = createLogParserCache()(
      applyLogPage(emptyLogBuffer(), { lines: [access('/ok'), access('/fail', 500)] })
    );
    expect(
      filterLogEntries(entries, { ...noFilters(), statuses: new Set(['5xx']), level: 'info' })
    ).toEqual([entries[1]]);
    expect(entries).toHaveLength(2);
  });
});
