import { describe, expect, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { apiClient } from '../src/services/api/client';
import { runSavePlan } from '../src/features/authFiles/modelRules/useModelRulesData';

describe('model rules save connection isolation', () => {
  for (const firstOperation of ['save', 'delete'] as const) {
    for (const fails of [false, true]) {
      test(`aborts after a stale ${firstOperation} ${fails ? 'failure' : 'success'}`, async () => {
        let revision = 1;
        const revisionSpy = spyOn(apiClient, 'getConnectionRevision').mockImplementation(
          () => revision
        );
        const calls: string[] = [];
        let finish!: () => void;
        const pending = new Promise<void>((resolve, reject) => {
          finish = () => (fails ? reject(new Error('stale request')) : resolve());
        });
        const operation = async (provider: string) => {
          calls.push(provider);
          await pending;
        };
        try {
          const saving = runSavePlan(
            [
              { provider: 'claude', value: firstOperation === 'delete' ? null : ['a'] },
              { provider: 'codex', value: ['b'] },
              { provider: 'gemini', value: null },
            ],
            operation,
            operation
          );
          expect(calls).toEqual(['claude']);
          revision++;
          finish();
          expect(await saving).toBeNull();
          expect(calls).toEqual(['claude']);
        } finally {
          revisionSpy.mockRestore();
        }
      });
    }
  }

  test('also discards the outcome when the last operation becomes stale', async () => {
    let revision = 1;
    const revisionSpy = spyOn(apiClient, 'getConnectionRevision').mockImplementation(
      () => revision
    );
    try {
      expect(
        await runSavePlan(
          [{ provider: 'codex', value: ['a'] }],
          async () => {
            revision++;
          },
          async () => {}
        )
      ).toBeNull();
    } finally {
      revisionSpy.mockRestore();
    }
  });
});

describe('model rules unsaved guard after reload failure', () => {
  for (const page of ['AuthFilesOAuthExcludedEditPage', 'AuthFilesOAuthModelAliasEditPage']) {
    test(`${page} retains dirty state independently of reload status`, () => {
      const source = readFileSync(`src/pages/${page}.tsx`, 'utf8');
      const expression = source.match(/const isDirty = (.*);/)?.[1];
      expect(expression).toBeDefined();
      const isDirty = new Function('data', 'dirtyProviders', `return ${expression};`);
      for (const status of ['loading', 'ready', 'error', 'unsupported']) {
        expect(isDirty({ revision: 0, status }, ['codex'])).toBe(false);
        expect(isDirty({ revision: 1, status }, ['codex'])).toBe(true);
        expect(isDirty({ revision: 1, status }, [])).toBe(false);
      }
      expect(source).toContain("useModelRulesRoute('");
      expect(source).toContain(', isDirty)');
      expect(source).toContain("canSave={data.status === 'ready' && isDirty && !disabled}");
      // A stale batch must not initiate a reload against the new connection.
      expect(source).toContain('if (!outcome) return;\n      setKeepProviders(outcome.failed);');
    });
  }
});
