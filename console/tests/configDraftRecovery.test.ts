import { describe, expect, test } from 'bun:test';
import { parse } from 'yaml';
import { ConfigDraftConflictError, rebaseConfigDraft } from '../src/services/api/configPatch';

describe('confirmed config draft recovery', () => {
  test('retains pending changes without resetting untouched concurrent values', () => {
    const before = 'server: {port: 8317}\nrouting: {retry: {request-retry: 3}}\n';
    const draft = 'server: {port: 9000}\n';
    const latest =
      '# current server\nserver: {port: 9000, host: localhost}\nrouting: {retry: {request-retry: 3, future: true}}\n';
    const recovered = rebaseConfigDraft(before, draft, latest);
    expect(parse(recovered)).toEqual({
      server: { port: 9000, host: 'localhost' },
      routing: { retry: { future: true } },
    });
    expect(recovered).toContain('# current server');
  });

  test('already completed deletions are idempotent and empty ancestors are pruned', () => {
    const before = 'server: {port: 8317}\nrouting: {retry: {request-retry: 3}}\n';
    const draft = 'server: {port: 9000}\n';
    expect(parse(rebaseConfigDraft(before, draft, before))).toEqual({ server: { port: 9000 } });
    expect(parse(rebaseConfigDraft(before, draft, draft))).toEqual(parse(draft));
  });

  test('retains explicit empty maps, nulls and replacements of scalar ancestors', () => {
    const before = 'routing: null\nplugins: {configs: {fixture: {old: true}}}\n';
    const draft =
      'routing: {strategy: round-robin}\nplugins: {configs: {fixture: {}}}\noptional: null\n';
    expect(parse(rebaseConfigDraft(before, draft, before))).toEqual(parse(draft));
  });

  test('rejects concurrent changes to a list before advancing the recovery baseline', () => {
    const before =
      'requests: {payload: {default: [{models: [{name: a}], params: {temperature: 1}}]}}';
    const draft = before.replace('temperature: 1', 'temperature: 2');
    const latest = before.replace('name: a', 'name: b');
    expect(() => rebaseConfigDraft(before, draft, latest)).toThrow(ConfigDraftConflictError);
    // Deleting a list must not discard concurrent additions either.
    expect(() => rebaseConfigDraft(before, 'server: {}', latest)).toThrow(ConfigDraftConflictError);
    expect(parse(rebaseConfigDraft(before, draft, draft))).toEqual(parse(draft));
  });

  test('list comparisons ignore formatting and mapping key order', () => {
    const before = 'access: {api-keys: [a, b]}';
    const draft = 'access: {api-keys: [b]}';
    const latest = '# formatting only\naccess:\n  api-keys:\n    - a\n    - b\n';
    expect(parse(rebaseConfigDraft(before, draft, latest))).toEqual(parse(draft));
  });

  test('rejects malformed readback without producing a new baseline', () => {
    expect(() => rebaseConfigDraft('server: {}', 'server: {port: 9000}', 'bad: [')).toThrow();
    expect(() => rebaseConfigDraft('server: {}', 'server: {port: 9000}', '')).toThrow();
  });
});
