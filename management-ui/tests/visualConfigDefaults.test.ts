import { describe, expect, test } from 'bun:test';
import { parse as parseYaml } from 'yaml';
import { runVisualConfig } from './helpers/visualConfig';

// Backend contract: WebsocketAuth defaults to true and Antigravity credits to false.
// config.example.yaml values are not missing-key defaults.
describe('visual config boolean defaults', () => {
  test('initial and missing-key values agree with backend defaults', () => {
    for (const config of [runVisualConfig(), runVisualConfig('server:\n  port: 8317\n')]) {
      expect(config.visualValues.wsAuth).toBe(true);
      expect(config.visualValues).not.toHaveProperty('quotaSwitchProject');
      expect(config.visualValues).not.toHaveProperty('quotaSwitchPreviewModel');
      expect(config.visualValues.quotaAntigravityCredits).toBe(false);
      expect(config.visualDirty).toBe(false);
    }
  });

  for (const enabled of [true, false]) {
    test(`preserves explicit boolean values: ${enabled}`, () => {
      const yaml = `oauth:
  providers:
    aistudio:
      ws-auth: ${enabled}
    antigravity:
      antigravity-credits: ${enabled}
`;
      const config = runVisualConfig(yaml);
      expect(config.visualValues.wsAuth).toBe(enabled);
      expect(config.visualValues.quotaAntigravityCredits).toBe(enabled);
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual(parseYaml(yaml));
    });
  }

  test('does not expose deprecated quota switches from existing YAML', () => {
    const yaml = 'quota-exceeded: {switch-project: true, switch-preview-model: true}\n';
    const config = runVisualConfig(yaml, [{ quotaAntigravityCredits: true }]);
    expect(config.visualValues).not.toHaveProperty('quotaSwitchProject');
    expect(config.visualValues).not.toHaveProperty('quotaSwitchPreviewModel');
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      ...parseYaml(yaml),
      oauth: { providers: { antigravity: { 'antigravity-credits': true } } },
    });
  });

  test('writes credits when the OAuth provider map is null', () => {
    const yaml = 'oauth: {providers: {antigravity: null}}\n';
    const config = runVisualConfig(yaml, [{ quotaAntigravityCredits: true }]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      oauth: { providers: { antigravity: { 'antigravity-credits': true } } },
    });
  });

  test('writes explicit false when disabling omitted ws-auth', () => {
    const yaml = 'server:\n  port: 8317\n';
    const config = runVisualConfig(yaml, [{ wsAuth: false }]);
    expect([...config.visualDirtyFields]).toEqual(['wsAuth']);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      server: { port: 8317 },
      oauth: { providers: { aistudio: { 'ws-auth': false } } },
    });
  });

  test('writes enabled credits without creating quota-exceeded', () => {
    const yaml = 'server:\n  port: 8317\n';
    const config = runVisualConfig(yaml, [{ quotaAntigravityCredits: true }]);
    expect([...config.visualDirtyFields]).toEqual(['quotaAntigravityCredits']);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      server: { port: 8317 },
      oauth: { providers: { antigravity: { 'antigravity-credits': true } } },
    });
  });

  test('writes toggles in both directions without changing unrelated settings', () => {
    for (const enabled of [true, false]) {
      const yaml = `server:
  port: 8317
oauth:
  providers:
    aistudio:
      ws-auth: ${!enabled}
    antigravity:
      antigravity-credits: ${!enabled}
`;
      const config = runVisualConfig(yaml, [{ wsAuth: enabled, quotaAntigravityCredits: enabled }]);
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
        oauth: {
          providers: {
            aistudio: { 'ws-auth': enabled },
            antigravity: { 'antigravity-credits': enabled },
          },
        },
        server: { port: 8317 },
      });
    }
  });

  test('reverting toggles clears dirty state without inserting missing keys', () => {
    const yaml = 'server:\n  port: 8317\n';
    const config = runVisualConfig(yaml, [
      { wsAuth: false, quotaAntigravityCredits: true },
      { wsAuth: true, quotaAntigravityCredits: false },
    ]);
    expect(config.visualDirty).toBe(false);
    expect(config.visualDirtyFields.size).toBe(0);
    expect(config.applyVisualChangesToYaml(yaml)).toBe(yaml);
  });

  test('unrelated edits do not materialize boolean defaults', () => {
    const yaml = 'server:\n  port: 8317\n';
    const config = runVisualConfig(yaml, [{ debug: true }]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      server: { port: 8317 },
      observability: { logs: { debug: true } },
    });
  });
});
