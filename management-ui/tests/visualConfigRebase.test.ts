import { describe, expect, test } from 'bun:test';
import { createElement, useState } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { parse, stringify } from 'yaml';
import { useVisualConfig } from '../src/hooks/useVisualConfig';
import {
  buildConfigPatch,
  ConfigDraftConflictError,
  rebaseConfigDraft,
} from '../src/services/api/configPatch';

type Visual = ReturnType<typeof useVisualConfig>;

// Render-phase updates exercise the actual hook/reducer without mocking React.
function runSteps(...steps: ((visual: Visual) => void)[]) {
  function Harness() {
    const visual = useVisualConfig();
    const [step, setStep] = useState(0);
    steps[step](visual);
    if (step + 1 < steps.length) setStep(step + 1);
    return null;
  }
  renderToStaticMarkup(createElement(Harness));
}

const before = 'server: {port: 8317}\nrouting: {retry: {request-retry: 3}}\n';
const server = 'server: {port: 9000}\nrouting: {retry: {request-retry: 3}}\n';

describe('visual config rebase', () => {
  test('advances baseline after partial save, retains deletions and concurrent unedited values', () => {
    let draft = '';
    const latest = `${server}observability: {logs: {debug: true}}\n`;
    runSteps(
      (v) => {
        v.loadVisualValuesFromYaml(before);
      },
      (v) => {
        v.setVisualValues({ port: '9000', requestRetry: '' });
      },
      (v) => {
        draft = v.applyVisualChangesToYaml(latest);
        expect(v.rebaseVisualValuesFromYaml(latest, draft)).toEqual({ ok: true });
      },
      (v) => {
        expect([...v.visualDirtyFields]).toEqual(['requestRetry']);
        expect(v.visualValues.debug).toBe(true);
        expect(buildConfigPatch(latest, v.applyVisualChangesToYaml(latest))).toEqual({
          patch: {},
          deletions: [['routing', 'retry', 'request-retry']],
        });
        v.setVisualValues({ port: '8317' });
      },
      (v) => {
        expect(v.visualDirtyFields.has('port')).toBe(true);
        expect(buildConfigPatch(latest, v.applyVisualChangesToYaml(latest)).patch).toEqual({
          server: { port: 8317 },
        });
        v.loadVisualValuesFromYaml(latest);
      },
      (v) => {
        expect(v.visualDirty).toBe(false);
        expect(v.visualValues.port).toBe('9000');
        expect(v.visualValues.requestRetry).toBe('3');
      }
    );
  });

  const rule = (name: string, value: number) => ({
    models: [
      { name, 'future-model': name, match: [{ count: value }], headers: { 'X-Test': name } },
    ],
    params: { temperature: value },
    'future-rule': name,
  });

  for (const section of ['default', 'default-raw', 'override', 'override-raw', 'filter']) {
    test(`${section}: deletion, modification, reorder and append retain the draft AST on retry`, () => {
      const rules = [rule('a', 1), rule('b', 2), rule('c', 3)];
      const desired = [rule('c', 9), rule('b-renamed', 2), rule('new', 4)];
      if (section.endsWith('-raw')) {
        for (const entry of [...rules, ...desired]) {
          Object.assign(entry, { params: { temperature: String(entry.params.temperature) } });
        }
      }
      if (section === 'filter') {
        for (const entry of [...rules, ...desired]) {
          Object.assign(entry, { params: ['secret'] });
        }
      }
      const saved = stringify({
        requests: { payload: { [section]: rules } },
        server: { port: 9000 },
      });
      const draft = stringify({
        requests: { payload: { [section]: desired } },
        server: { port: 9000 },
      });
      runSteps(
        (v) => {
          v.loadVisualValuesFromYaml(saved);
        },
        (v) => {
          v.rebaseVisualValuesFromYaml(saved, draft);
        },
        (v) => {
          expect(v.visualDirtyFields.size).toBe(1);
          const retry = v.applyVisualChangesToYaml(saved);
          expect(parse(retry)).toEqual(parse(draft));
          expect(v.applyVisualChangesToYaml(saved)).toBe(retry);
          v.rebaseVisualValuesFromYaml(retry, retry);
        },
        (v) => {
          expect(v.visualDirty).toBe(false);
        }
      );
    });
  }

  for (const [section, field] of [
    ['default', 'payloadDefaultRules'],
    ['default-raw', 'payloadDefaultRawRules'],
    ['override', 'payloadOverrideRules'],
    ['override-raw', 'payloadOverrideRawRules'],
    ['filter', 'payloadFilterRules'],
  ] as const) {
    test(`${section}: recovery does not replay old list indexes after a successful PATCH`, () => {
      const rules = [rule('a', 1), rule('b', 2), rule('c', 3)].map((entry) => ({
        ...entry,
        params:
          section === 'filter'
            ? ['secret']
            : {
                temperature: section.endsWith('-raw')
                  ? String(entry.params.temperature)
                  : entry.params.temperature,
              },
      }));
      const original = stringify({
        requests: { payload: { [section]: rules } },
        routing: { retry: { 'request-retry': 3 } },
      });
      let confirmed = '';
      let readback = '';
      runSteps(
        (v) => {
          v.loadVisualValuesFromYaml(original);
        },
        (v) => {
          // Delete A and reorder the survivors. Their original indexes no longer exist.
          const entries = v.visualValues[field];
          v.setVisualValues({ [field]: [entries[2], entries[1]], requestRetry: '' });
        },
        (v) => {
          confirmed = v.applyVisualChangesToYaml(original);
          const saved = parse(confirmed);
          // PATCH applied, DELETE failed; another client also changed an unrelated value.
          saved.routing = { retry: { 'request-retry': 3 } };
          saved.observability = { logs: { debug: true } };
          readback = stringify(saved);
          const recovered = rebaseConfigDraft(original, confirmed, readback);
          expect(parse(recovered).requests.payload[section]).toEqual([rules[2], rules[1]]);
          expect(v.rebaseVisualValuesFromYaml(readback, recovered)).toEqual({ ok: true });
        },
        (v) => {
          expect([...v.visualDirtyFields]).toEqual(['requestRetry']);
          expect(v.visualValues.debug).toBe(true);
          expect(buildConfigPatch(readback, v.applyVisualChangesToYaml(readback))).toEqual({
            patch: {},
            deletions: [['routing', 'retry', 'request-retry']],
          });
        }
      );
    });
  }

  test('recovery retains unapplied list intent and adopts concurrent unrelated fields', () => {
    const original = stringify({
      requests: { payload: { default: [rule('a', 1), rule('b', 2)] } },
    });
    let confirmed = '';
    const readback = `${original}server: {port: 9000}\n`;
    runSteps(
      (v) => {
        v.loadVisualValuesFromYaml(original);
      },
      (v) => {
        v.setVisualValues({ payloadDefaultRules: [v.visualValues.payloadDefaultRules[1]] });
      },
      (v) => {
        confirmed = v.applyVisualChangesToYaml(original);
        const recovered = rebaseConfigDraft(original, confirmed, readback);
        expect(parse(recovered).requests.payload.default).toEqual([rule('b', 2)]);
        v.rebaseVisualValuesFromYaml(readback, recovered);
      },
      (v) => {
        expect(v.visualValues.port).toBe('9000');
        expect([...v.visualDirtyFields]).toEqual(['payloadDefaultRules']);
        expect(parse(v.applyVisualChangesToYaml(readback)).requests.payload.default).toEqual([
          rule('b', 2),
        ]);
      }
    );
  });

  for (const section of ['default', 'default-raw', 'override', 'override-raw', 'filter']) {
    test(`${section}: a retry blocks concurrent list changes instead of replacing the snapshot`, () => {
      const entry = (value: number) => ({
        ...rule('b', 1),
        params:
          section === 'filter'
            ? [value === 1 ? 'secret' : 'other-secret']
            : { temperature: section.endsWith('-raw') ? String(value) : value },
      });
      const saved = stringify({ requests: { payload: { [section]: [entry(1)] } } });
      const draft = stringify({ requests: { payload: { [section]: [entry(2)] } } });
      const concurrent = entry(1);
      concurrent.models[0].match = [{ count: 99 }];
      const latest = stringify({ requests: { payload: { [section]: [concurrent] } } });
      runSteps(
        (v) => {
          v.rebaseVisualValuesFromYaml(saved, draft);
        },
        (v) => {
          expect(() => v.applyVisualChangesToYaml(latest)).toThrow(ConfigDraftConflictError);
          // Repeated previews/confirmations cannot silently accept the stale snapshot.
          expect(() => v.applyVisualChangesToYaml(latest)).toThrow(ConfigDraftConflictError);
          expect(v.visualDirty).toBe(true);
          expect(parse(v.applyVisualChangesToYaml(saved))).toEqual(parse(draft));
          // A lost response from an already-applied write is not a conflict.
          expect(parse(v.applyVisualChangesToYaml(draft))).toEqual(parse(draft));
        }
      );
    });
  }

  test('retry adopts concurrent unrelated lists, but detects unknown fields and list deletion', () => {
    const saved = stringify({ requests: { payload: { default: [rule('a', 1)] } } });
    const draft = stringify({ requests: { payload: { default: [rule('a', 2)] } } });
    const unrelated = `${saved}access: {api-keys: [fixture-only]}\n`;
    const changed = parse(saved);
    changed.requests.payload.default[0]['future-rule'] = 'concurrent';
    runSteps(
      (v) => {
        v.rebaseVisualValuesFromYaml(saved, draft);
      },
      (v) => {
        expect(parse(v.applyVisualChangesToYaml(unrelated)).access).toEqual({
          'api-keys': ['fixture-only'],
        });
        expect(() => v.applyVisualChangesToYaml(stringify(changed))).toThrow(
          ConfigDraftConflictError
        );
        expect(() => v.applyVisualChangesToYaml('server: {}')).toThrow(ConfigDraftConflictError);
      }
    );
  });

  test('local source previews are not treated as concurrent server snapshots', () => {
    const saved = stringify({ requests: { payload: { default: [rule('a', 1)] } } });
    const draft = stringify({ requests: { payload: { default: [rule('a', 2)] } } });
    let sourcePreview = '';
    runSteps(
      (v) => {
        v.rebaseVisualValuesFromYaml(saved, draft);
      },
      (v) => {
        sourcePreview = v.applyVisualChangesToYaml(saved, 'draft');
        const entry = v.visualValues.payloadDefaultRules[0];
        v.setVisualValues({
          payloadDefaultRules: [
            { ...entry, params: entry.params.map((param) => ({ ...param, value: '3' })) },
          ],
        });
      },
      (v) => {
        const nextPreview = v.applyVisualChangesToYaml(sourcePreview, 'draft');
        expect(parse(nextPreview).requests.payload.default[0].params.temperature).toBe(3);
        expect(parse(v.applyVisualChangesToYaml(saved))).toEqual(parse(nextPreview));
        // The same document from a real server must still trigger a conflict.
        expect(() => v.applyVisualChangesToYaml(sourcePreview)).toThrow(ConfigDraftConflictError);
      }
    );
  });

  test('subsequent nested edits use the rebased draft IDs, not old positional nodes', () => {
    const saved = stringify({ requests: { payload: { default: [rule('a', 1), rule('b', 2)] } } });
    const draft = stringify({ requests: { payload: { default: [rule('b', 8)] } } });
    runSteps(
      (v) => {
        v.rebaseVisualValuesFromYaml(saved, draft);
      },
      (v) => {
        const entry = v.visualValues.payloadDefaultRules[0];
        v.setVisualValues({
          payloadDefaultRules: [
            {
              ...entry,
              models: [{ ...entry.models[0], name: 'renamed' }],
              params: entry.params.map((param) => ({ ...param, value: '10' })),
            },
          ],
        });
      },
      (v) => {
        const rules = parse(v.applyVisualChangesToYaml(saved)).requests.payload.default;
        expect(rules).toHaveLength(1);
        expect(rules[0]['future-rule']).toBe('b');
        expect(rules[0].models[0]['future-model']).toBe('b');
        expect(rules[0].models[0].name).toBe('renamed');
        expect(rules[0].params.temperature).toBe(10);
        v.loadVisualValuesFromYaml(saved);
      },
      (v) => {
        const entries = v.visualValues.payloadDefaultRules;
        v.setVisualValues({ payloadDefaultRules: [entries[0]] });
      },
      (v) => {
        expect(parse(v.applyVisualChangesToYaml(saved)).requests.payload.default).toEqual([
          rule('a', 1),
        ]);
      }
    );
  });

  test('equal rules and plugin auth do not acquire spurious dirty IDs', () => {
    const yaml = stringify({
      requests: { payload: { default: [rule('a', 1), rule('b', 2)] } },
      plugins: {
        'store-auth': [{ match: 'example.test', type: 'bearer', 'token-env': 'TEST_TOKEN' }],
      },
    });
    runSteps(
      (v) => {
        v.loadVisualValuesFromYaml(before);
      },
      (v) => {
        v.rebaseVisualValuesFromYaml(yaml, yaml);
      },
      (v) => {
        expect(v.visualDirty).toBe(false);
        v.setVisualValues({ payloadDefaultRules: [...v.visualValues.payloadDefaultRules] });
      },
      (v) => {
        expect(v.visualDirty).toBe(false);
      }
    );
  });

  test('failed parse does not partially replace values or baseline', () => {
    runSteps(
      (v) => {
        v.loadVisualValuesFromYaml(before);
      },
      (v) => {
        v.setVisualValues({ port: '9000' });
      },
      (v) => {
        expect(v.rebaseVisualValuesFromYaml(server, 'bad: [')).toMatchObject({ ok: false });
      },
      (v) => {
        expect(v.visualValues.port).toBe('9000');
        expect(v.visualDirty).toBe(true);
        expect(v.visualParseError).toBeNull();
        v.setVisualValues({ port: '8317' });
      },
      (v) => {
        expect(v.visualDirty).toBe(false);
      }
    );
  });
});
