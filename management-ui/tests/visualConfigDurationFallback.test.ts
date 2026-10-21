import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parseDocument } from 'yaml';
import { useVisualConfig, getVisualConfigValidationErrors } from '@/hooks/useVisualConfig';
import { DEFAULT_VISUAL_VALUES } from '@/types/visualConfig';
import { runVisualConfig } from './helpers/visualConfig';

const cases = [
  {
    key: 'videoResultAuthCacheTTL',
    path: ['multimedia', 'video-result-auth-cache-ttl'],
    existing: '0s',
    edited: '-1s',
    error: 'positive_duration',
  },
  {
    key: 'codexStreamBootstrapTimeout',
    path: ['upstream', 'codex', 'stream-bootstrap-timeout'],
    existing: '-1s',
    edited: '-2s',
    error: 'invalid_duration',
  },
  {
    key: 'antigravityConnectionPoolIdleTimeout',
    path: ['oauth', 'providers', 'antigravity', 'connection-pool', 'idle-conn-timeout'],
    existing: 'invalid-timeout',
    edited: 'still-invalid',
    error: 'invalid_duration',
  },
] as const;

function fixture(path: readonly string[], value: string): string {
  const doc = parseDocument('server: {port: 8317}\nfuture: retained # keep\n');
  doc.setIn(path, value);
  return doc.toString();
}

describe('existing backend duration fallbacks', () => {
  for (const { key, path, existing, edited, error } of cases) {
    test(`${key}: unrelated edits preserve the original value without blocking save`, () => {
      const yaml = fixture(path, existing);
      const loaded = runVisualConfig(yaml);
      expect(loaded.visualValues[key]).toBe(existing);
      expect(loaded.visualValidationErrors[key]).toBeUndefined();
      const changed = runVisualConfig(yaml, [{ debug: true }]);
      expect(Object.values(changed.visualValidationErrors).some(Boolean)).toBe(false);
      expect([...changed.visualDirtyFields]).toEqual(['debug']);
      const output = changed.applyVisualChangesToYaml(yaml);
      expect(parseDocument(output).getIn(path)).toBe(existing);
      expect(parseDocument(output).getIn(['observability', 'logs', 'debug'])).toBe(true);
      expect(output).toContain('future: retained # keep');
    });

    test(`${key}: newly edited invalid values are still blocked, and reverting clears the error`, () => {
      const yaml = fixture(path, existing);
      const invalid = runVisualConfig(yaml, [{ [key]: edited }]);
      expect(invalid.visualDirtyFields.has(key)).toBe(true);
      expect(invalid.visualValidationErrors[key]).toBe(error);
      const restored = runVisualConfig(yaml, [{ [key]: edited }, { [key]: existing }]);
      expect(restored.visualDirty).toBe(false);
      expect(restored.visualValidationErrors[key]).toBeUndefined();
      const newlyAdded = runVisualConfig('{}', [{ [key]: existing }]);
      expect(newlyAdded.visualValidationErrors[key]).toBe(error);
      const valid = runVisualConfig(yaml, [{ [key]: '30s' }]);
      expect(valid.visualValidationErrors[key]).toBeUndefined();
      expect(parseDocument(valid.applyVisualChangesToYaml(yaml)).getIn(path)).toBe('30s');
      const cleared = runVisualConfig(yaml, [{ [key]: '' }]);
      expect(cleared.visualValidationErrors[key]).toBeUndefined();
      expect(parseDocument(cleared.applyVisualChangesToYaml(yaml)).hasIn(path)).toBe(false);
    });

    test(`${key}: reload/rebase use the server baseline, not a stale dirty flag`, () => {
      const baseline = fixture(path, existing);
      const draft = fixture(path, edited);
      let checks = 0;
      function Harness() {
        const v = useVisualConfig();
        const [step, setStep] = useState(0);
        if (step === 0) v.loadVisualValuesFromYaml(baseline);
        if (step === 1) v.rebaseVisualValuesFromYaml(baseline, draft);
        if (step === 2) {
          expect(v.visualValidationErrors[key]).toBe(error);
          checks++;
          // Once the backend confirms this value, it no longer blocks unrelated edits.
          v.rebaseVisualValuesFromYaml(draft, draft);
        }
        if (step === 3) {
          expect(v.visualValidationErrors[key]).toBeUndefined();
          expect(v.visualDirtyFields.has(key)).toBe(false);
          checks++;
          v.setVisualValues({ [key]: existing });
        }
        if (step === 4) {
          expect(v.visualValidationErrors[key]).toBe(error);
          checks++;
          v.loadVisualValuesFromYaml(baseline);
        }
        if (step === 5) {
          expect(v.visualValidationErrors[key]).toBeUndefined();
          expect(v.visualDirty).toBe(false);
          checks++;
        } else setStep(step + 1);
        return null;
      }
      renderToStaticMarkup(createElement(Harness));
      expect(checks).toBe(4);
    });
  }

  test('standalone validation remains strict and unrelated constraints remain enforced', () => {
    for (const { key, existing, error } of cases) {
      expect(
        getVisualConfigValidationErrors({ ...DEFAULT_VISUAL_VALUES, [key]: existing })[key]
      ).toBe(error);
    }
    const loaded = runVisualConfig('server: {port: 70000}\n', [{ debug: true }]);
    expect(loaded.visualValidationErrors.port).toBe('port_range');
  });
});
