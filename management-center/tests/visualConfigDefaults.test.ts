import { describe, expect, test } from 'bun:test';
import { parse as parseYaml } from 'yaml';
import { runVisualConfig } from './helpers/visualConfig';

// Backend contract: config_load.go defaults WebsocketAuth to true; QuotaExceeded
// uses Go's false zero values. config.example.yaml values are not missing-key defaults.
describe('visual config boolean defaults', () => {
  test('initial and missing-key values agree with backend defaults', () => {
    for (const config of [runVisualConfig(), runVisualConfig('port: 8317\n')]) {
      expect(config.visualValues.wsAuth).toBe(true);
      expect(config.visualValues.quotaSwitchProject).toBe(false);
      expect(config.visualValues.quotaSwitchPreviewModel).toBe(false);
      expect(config.visualDirty).toBe(false);
    }
  });

  for (const enabled of [true, false]) {
    test(`preserves explicit boolean values: ${enabled}`, () => {
      const yaml = `ws-auth: ${enabled}
quota-exceeded:
  switch-project: ${enabled}
  switch-preview-model: ${enabled}
`;
      const config = runVisualConfig(yaml);
      expect(config.visualValues.wsAuth).toBe(enabled);
      expect(config.visualValues.quotaSwitchProject).toBe(enabled);
      expect(config.visualValues.quotaSwitchPreviewModel).toBe(enabled);
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual(parseYaml(yaml));
    });
  }

  test('writes explicit false when disabling omitted ws-auth', () => {
    const yaml = 'port: 8317\n';
    const config = runVisualConfig(yaml, [{ wsAuth: false }]);
    expect([...config.visualDirtyFields]).toEqual(['wsAuth']);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      port: 8317,
      'ws-auth': false,
    });
  });

  test('writes enabled quota options when their block was omitted', () => {
    const yaml = 'port: 8317\n';
    const config = runVisualConfig(yaml, [
      { quotaSwitchProject: true, quotaSwitchPreviewModel: true },
    ]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
      port: 8317,
      'quota-exceeded': { 'switch-project': true, 'switch-preview-model': true },
    });
  });

  test('writes toggles in both directions without changing unrelated settings', () => {
    for (const enabled of [true, false]) {
      const yaml = `ws-auth: ${!enabled}
quota-exceeded:
  switch-project: ${!enabled}
  switch-preview-model: ${!enabled}
  antigravity-credits: true
`;
      const config = runVisualConfig(yaml, [
        { wsAuth: enabled, quotaSwitchProject: enabled, quotaSwitchPreviewModel: enabled },
      ]);
      expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({
        'ws-auth': enabled,
        'quota-exceeded': {
          'switch-project': enabled,
          'switch-preview-model': enabled,
          'antigravity-credits': true,
        },
      });
    }
  });

  test('reverting toggles clears dirty state without inserting missing keys', () => {
    const yaml = 'port: 8317\n';
    const config = runVisualConfig(yaml, [
      { wsAuth: false, quotaSwitchProject: true, quotaSwitchPreviewModel: true },
      { wsAuth: true, quotaSwitchProject: false, quotaSwitchPreviewModel: false },
    ]);
    expect(config.visualDirty).toBe(false);
    expect(config.visualDirtyFields.size).toBe(0);
    expect(config.applyVisualChangesToYaml(yaml)).toBe(yaml);
  });

  test('unrelated edits do not materialize boolean defaults', () => {
    const yaml = 'port: 8317\n';
    const config = runVisualConfig(yaml, [{ debug: true }]);
    expect(parseYaml(config.applyVisualChangesToYaml(yaml))).toEqual({ port: 8317, debug: true });
  });
});
