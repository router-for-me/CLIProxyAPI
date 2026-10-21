import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import {
  applyAuthFileOfficialClientOnly,
  readAuthFileOfficialClientOnly,
  supportsAuthFileOfficialClientOnly,
} from '../src/features/authFiles/constants';
import {
  buildAuthFileFieldsPatch,
  type PrefixProxyEditorState,
} from '../src/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';

const makeEditor = (
  json: Record<string, unknown>,
  overrides: Partial<PrefixProxyEditorState> = {}
): PrefixProxyEditorState => ({
  fileName: 'credential.json',
  fileInfoText: '',
  loading: false,
  saving: false,
  error: null,
  originalText: JSON.stringify(json),
  rawText: JSON.stringify(json),
  invalidContentPreview: '',
  json,
  providerKey: 'claude',
  prefix: '',
  proxyUrl: '',
  priority: '',
  weight: '',
  weightError: null,
  disableCooling: false,
  disableCoolingTouched: false,
  websockets: false,
  websocketsTouched: false,
  usingApi: false,
  usingApiTouched: false,
  officialClientOnly: readAuthFileOfficialClientOnly(json),
  officialClientOnlyTouched: false,
  note: '',
  noteTouched: false,
  excludedModelsText: '',
  excludedModelsTouched: false,
  headersText: '',
  headersTouched: false,
  headersError: null,
  ...overrides,
});

const resolveError = (key: string) => key;

describe('auth-file official_client_only', () => {
  test('is only offered for Claude and Codex credentials', () => {
    expect(supportsAuthFileOfficialClientOnly('claude')).toBe(true);
    expect(supportsAuthFileOfficialClientOnly('codex')).toBe(true);
    expect(supportsAuthFileOfficialClientOnly(' Codex ')).toBe(true);
    for (const provider of ['gemini', 'xai', 'grok', 'antigravity', 'kimi', 'anthropic', '']) {
      expect(supportsAuthFileOfficialClientOnly(provider)).toBe(false);
    }
  });

  test('defaults to off when the field is absent', () => {
    expect(readAuthFileOfficialClientOnly({})).toBe(false);
    expect(readAuthFileOfficialClientOnly({ official_client_only: null })).toBe(false);
    expect(readAuthFileOfficialClientOnly({ official_client_only: false })).toBe(false);
    expect(readAuthFileOfficialClientOnly({ official_client_only: true })).toBe(true);
  });

  test('reads strconv.ParseBool strings exactly like the backend', () => {
    for (const text of ['true', 'True', 'TRUE', 't', 'T', '1', ' true ']) {
      expect(readAuthFileOfficialClientOnly({ official_client_only: text })).toBe(true);
    }
    for (const text of ['false', 'False', 'FALSE', 'f', 'F', '0', ' false ']) {
      expect(readAuthFileOfficialClientOnly({ official_client_only: text })).toBe(false);
    }
  });

  test('shows unparseable values as on because the backend fails closed', () => {
    for (const value of ['maybe', 'yes', 'tRuE', '', 5, 0, {}, []]) {
      expect(readAuthFileOfficialClientOnly({ official_client_only: value })).toBe(true);
    }
  });

  test('writes an explicit boolean without touching other fields', () => {
    expect(applyAuthFileOfficialClientOnly({ type: 'claude' }, true)).toEqual({
      type: 'claude',
      official_client_only: true,
    });
    expect(
      applyAuthFileOfficialClientOnly({ type: 'codex', official_client_only: true }, false)
    ).toEqual({ type: 'codex', official_client_only: false });
  });

  describe('save patch', () => {
    test('sends nothing until the toggle is touched', () => {
      expect(buildAuthFileFieldsPatch(makeEditor({ type: 'claude' }), resolveError)).toEqual({});
    });

    test('sends the new value once, for both supported providers', () => {
      for (const providerKey of ['claude', 'codex']) {
        const editor = makeEditor(
          { type: providerKey },
          { providerKey, officialClientOnly: true, officialClientOnlyTouched: true }
        );
        expect(buildAuthFileFieldsPatch(editor, resolveError)).toEqual({
          official_client_only: true,
        });
      }
      const turnOff = makeEditor(
        { type: 'claude', official_client_only: true },
        { officialClientOnly: false, officialClientOnlyTouched: true }
      );
      expect(buildAuthFileFieldsPatch(turnOff, resolveError)).toEqual({
        official_client_only: false,
      });
    });

    test('skips the patch when the toggle is changed back to the stored value', () => {
      const editor = makeEditor(
        { type: 'claude', official_client_only: true },
        { officialClientOnly: true, officialClientOnlyTouched: true }
      );
      expect(buildAuthFileFieldsPatch(editor, resolveError)).toEqual({});
      const absent = makeEditor(
        { type: 'claude' },
        { officialClientOnly: false, officialClientOnlyTouched: true }
      );
      expect(buildAuthFileFieldsPatch(absent, resolveError)).toEqual({});
    });

    test('rewrites a non-boolean stored value as an explicit boolean', () => {
      const malformed = makeEditor(
        { type: 'claude', official_client_only: 'maybe' },
        { officialClientOnlyTouched: true }
      );
      expect(malformed.officialClientOnly).toBe(true);
      expect(buildAuthFileFieldsPatch(malformed, resolveError)).toEqual({
        official_client_only: true,
      });
    });

    test('never patches unsupported providers', () => {
      const editor = makeEditor(
        { type: 'gemini' },
        { providerKey: 'gemini', officialClientOnly: true, officialClientOnlyTouched: true }
      );
      expect(buildAuthFileFieldsPatch(editor, resolveError)).toEqual({});
    });
  });

  test('is translated in every locale', () => {
    const locales = ['en', 'zh-CN', 'zh-TW', 'ko', 'ru', 'vi'];
    for (const locale of locales) {
      const data = JSON.parse(
        readFileSync(new URL(`../src/i18n/locales/${locale}.json`, import.meta.url), 'utf8')
      ) as { auth_files: Record<string, unknown> };
      for (const key of [
        'official_client_only_label',
        'official_client_only_hint_claude',
        'official_client_only_hint_codex',
      ]) {
        const value = data.auth_files[key];
        expect(typeof value === 'string' && value.trim().length > 0).toBe(true);
      }
    }
  });

  test('details sheet renders a labelled toggle only for supported providers', () => {
    const sheet = readFileSync(
      new URL('../src/features/authFiles/components/AuthFileDetailsSheet.tsx', import.meta.url),
      'utf8'
    );
    const block = sheet.split('supportsAuthFileOfficialClientOnly(editor.providerKey) && (')[1];
    expect(block).toBeDefined();
    const toggle = block.split('</ToggleSwitch>')[0].split('/>')[0];
    expect(toggle).toContain('checked={editor.officialClientOnly}');
    expect(toggle).toContain("onChange('officialClientOnly', value)");
    expect(toggle).toContain("ariaLabel={t('auth_files.official_client_only_label')}");
    expect(toggle).toContain('disableControls || editor.saving || !editor.json');
  });
});
