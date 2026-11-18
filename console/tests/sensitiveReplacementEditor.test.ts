import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import {
  LiteralReplacementEditor,
  PatternReplacementEditor,
  type HostStyleProps,
} from '@/components/providers/SensitiveReplacementEditor';
import { serializeLiteralDraft } from '@/components/providers/sensitiveReplacementDraft';
import en from '@/i18n/locales/en.json';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });

const withI18n = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n }, element));

const renderLiteral = (value: Record<string, string>, host: HostStyleProps = {}) =>
  withI18n(createElement(LiteralReplacementEditor, { value, onChange: () => {}, ...host }));

const PATTERNS = [
  { pattern: '(?i)secret', replacement: '' },
  { pattern: '(?=unsupported)', replacement: '  kept\nline two  ' },
];
const renderPatterns = (
  value: { pattern: string; replacement: string }[] = PATTERNS,
  host: HostStyleProps = {}
) => withI18n(createElement(PatternReplacementEditor, { value, onChange: () => {}, ...host }));

const tags = (html: string, tag: string) => html.match(new RegExp(`<${tag}\\b[^>]*>`, 'g')) ?? [];

describe('sensitive replacement editor', () => {
  test('retains empty values and whitespace, omits only empty draft keys', () => {
    expect(
      serializeLiteralDraft([
        { key: 'secret', value: '' },
        { key: ' spaced ', value: '  value  ' },
        { key: '', value: 'draft' },
      ])
    ).toEqual({ secret: '', ' spaced ': '  value  ' });
  });

  test('blocks duplicate publication without overwriting either draft', () => {
    const rows = [
      { key: 'same', value: 'first' },
      { key: 'same', value: 'second' },
    ];
    expect(serializeLiteralDraft(rows)).toBeNull();
    expect(rows[1].value).toBe('second');
    expect(serializeLiteralDraft(rows.slice(1))).toEqual({ same: 'second' });
    expect(serializeLiteralDraft([{ key: '__proto__', value: '' }])).toEqual(
      Object.fromEntries([['__proto__', '']])
    );
  });

  test('renders row controls and RE2-only syntax verbatim without JS validation', () => {
    const html = renderPatterns();
    expect(html).toContain('aria-label="Regex pattern, row 1"');
    expect(html).toContain('aria-label="Move regex row 2 up"');
    expect(html).toContain('title="Move regex row 2 up"');
    expect(html).toContain('aria-label="Remove regex row 1"');
    expect(html).toContain('>(?=unsupported)</textarea>');
    expect(html).toContain('>  kept\nline two  </textarea>');
    expect(html).not.toContain('aria-invalid="true"');
    expect(html.indexOf('(?i)secret')).toBeLessThan(html.indexOf('(?=unsupported)'));
    // Editors render no title or hint of their own; hosts own those.
    expect(html).not.toContain('<h3');
    expect(html).not.toContain('<section');
    expect(en.sensitive_replacements.pattern_hint).toContain('Go RE2');
  });

  test('literal rows keep whitespace and expose remove but no move controls', () => {
    const html = renderLiteral({ ' spaced key ': '  value\nnext  ', secret: '' });
    expect(html).toContain('aria-label="Literal text, row 1"');
    expect(html).toContain('aria-label="Remove literal row 2"');
    expect(html).toContain('> spaced key </textarea>');
    expect(html).toContain('>  value\nnext  </textarea>');
    expect(html).not.toContain('Move regex row');
    expect(html).not.toContain('aria-invalid="true"');
    expect(html).toContain('placeholder="Leave empty to delete the match"');
  });

  test('regex rows expose move buttons with the ends disabled', () => {
    const html = renderPatterns([
      { pattern: 'a', replacement: '' },
      { pattern: 'b', replacement: '' },
      { pattern: 'c', replacement: '' },
    ]);
    const button = (label: string) =>
      tags(html, 'button').find((tag) => tag.includes(`aria-label="${label}"`)) ?? '';
    expect(button('Move regex row 1 up')).toContain('disabled=""');
    expect(button('Move regex row 1 down')).not.toContain('disabled=""');
    expect(button('Move regex row 2 up')).not.toContain('disabled=""');
    expect(button('Move regex row 3 up')).not.toContain('disabled=""');
    expect(button('Move regex row 3 down')).toContain('disabled=""');
    for (const tag of tags(html, 'button')) expect(tag).toContain('type="button"');
  });

  test('empty lists show the empty state and no column header', () => {
    const literal = renderLiteral({});
    expect(literal).toContain('No literal replacements yet.');
    expect(literal).not.toContain('Literal text');
    expect(literal).not.toContain('aria-hidden="true"');
    expect(literal).toContain('Add literal replacement');

    const patterns = renderPatterns([]);
    expect(patterns).toContain('No regex rules yet.');
    expect(patterns).not.toContain('>Pattern<');
    expect(patterns).not.toContain('<textarea');

    const filled = renderPatterns();
    expect(filled).not.toContain('No regex rules yet.');
    expect(filled).toContain('>Pattern<');
    expect(filled).toContain('>Replacement<');
  });

  test('describedBy lands on match textareas and host classes are applied', () => {
    const html = renderPatterns(PATTERNS, {
      describedBy: 'host-hint',
      inputClassName: 'sheet-input',
      addButtonClassName: 'sheet-add',
      addButtonIcon: true,
    });
    const areas = tags(html, 'textarea');
    expect(areas).toHaveLength(4);
    const matches = areas.filter((tag) => tag.includes('aria-label="Regex pattern'));
    const values = areas.filter((tag) => tag.includes('aria-label="Regex replacement'));
    for (const tag of matches) expect(tag).toContain('aria-describedby="host-hint"');
    for (const tag of values) expect(tag).not.toContain('aria-describedby');
    for (const tag of areas) {
      expect(tag).toMatch(/class="sheet-input\b/);
      expect(tag).toContain('rows="1"');
      expect(tag).toContain('spellCheck="false"');
    }
    const add = tags(html, 'button').find((tag) => tag.includes('class="sheet-add"'));
    expect(add).toBeDefined();
    expect(html).toMatch(/class="sheet-add"[^>]*><svg/);

    const defaults = renderLiteral({ a: '' });
    expect(defaults).toMatch(/<textarea[^>]*class="input\b/);
    expect(defaults).toContain('class="btn btn-secondary btn-sm"');
    expect(defaults).not.toMatch(/class="btn btn-secondary btn-sm"[^>]*><svg/);
  });

  test('disables every input and action when disabled', () => {
    for (const html of [
      renderPatterns(PATTERNS, { disabled: true }),
      renderLiteral({ secret: '' }, { disabled: true }),
    ]) {
      const controls = html.match(/<(?:textarea|button)\b[^>]*>/g) ?? [];
      expect(controls.length).toBeGreaterThan(0);
      for (const control of controls) expect(control).toContain('disabled=""');
    }
  });

  test('all seven locales provide the same translation keys and row placeholders', () => {
    for (const locale of ['en', 'zh-CN', 'zh-TW', 'ru', 'vi', 'ko', 'tr']) {
      const messages = JSON.parse(readFileSync(`src/i18n/locales/${locale}.json`, 'utf8'));
      const section = messages.sensitive_replacements;
      expect(Object.keys(section).sort()).toEqual(Object.keys(en.sensitive_replacements).sort());
      for (const [key, value] of Object.entries(en.sensitive_replacements)) {
        expect(section[key].length).toBeGreaterThan(0);
        if (value.includes('{{row}}')) expect(section[key]).toContain('{{row}}');
      }
    }
  });

  test('every sensitive_replacements key is referenced by the editor or a host', () => {
    const sources = [
      'src/components/providers/SensitiveReplacementEditor.tsx',
      'src/features/config/components/sections/SectionAdvanced.tsx',
      'src/features/providers/sheets/forms/BaseProviderForm.tsx',
    ]
      .map((path) => readFileSync(path, 'utf8'))
      .join('\n');
    for (const key of Object.keys(en.sensitive_replacements)) {
      expect(sources).toMatch(new RegExp(`['.]${key}'`));
    }
  });
});
