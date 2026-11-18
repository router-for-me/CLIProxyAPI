import { readFileSync } from 'node:fs';
import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import i18n from '@/i18n';
import { PayloadReplaceRulesEditor } from '@/features/config/components/blocks/PayloadReplaceRulesEditor';
import type { PayloadReplaceRule } from '@/types/visualConfig';

const prefix = 'config_management.visual.payload_replace.';
const rule: PayloadReplaceRule = {
  id: 'rule',
  models: [
    {
      id: 'model',
      name: 'test-*',
      protocol: 'openai',
      fromProtocol: 'claude',
      headers: [{ id: 'header', name: 'x-test', value: 'present' }],
      match: [{ id: 'match', path: 'temperature', valueType: 'number', value: '0.2' }],
      notMatch: [{ id: 'not-match', path: 'stream', valueType: 'boolean', value: 'false' }],
      exist: ['messages'],
      notExist: ['tools'],
    },
  ],
  target: { role: 'user', all: false, positions: '1, 3' },
  replacements: [{ id: 'replacement', from: '  literal  ', to: '' }],
};
const render = (value = [rule], disabled = false) =>
  renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(PayloadReplaceRulesEditor, {
        value,
        disabled,
        onChange: () => {},
      })
    )
  );

describe('payload replacement editor', () => {
  test('renders full shared model conditions and literal operands', () => {
    const html = render();
    for (const text of ['test-*', 'x-test', 'present', 'temperature', 'messages', 'tools']) {
      expect(html).toContain(text);
    }
    expect(html).toContain('>  literal  </textarea>');
    expect(html).toMatch(/<textarea[^>]*><\/textarea>/);
    expect(html).toContain('value="1, 3"');
    expect(html).not.toContain('role="alert"');
  });

  test('all mode excludes the positions input', () => {
    const html = render([{ ...rule, target: { role: 'user', all: true, positions: '' } }]);
    expect(html).not.toMatch(/<input[^>]*type="text"/);
    expect(html).not.toContain(i18n.t(`${prefix}target_hint`));
    expect(html).toMatch(/data-side="start"/);
    expect(html.match(/<input[^>]*type="radio"[^>]*checked=""/g)).toHaveLength(1);
  });

  test('positions mode shows the positions input with its contextual hint', () => {
    const html = render();
    expect(html).toMatch(/<input[^>]*type="text"[^>]*value="1, 3"/);
    expect(html).toContain(i18n.t(`${prefix}target_hint`));
    expect(html).toMatch(/data-side="end"/);
  });

  test('icon-only controls and literal operands carry accessible names', () => {
    const html = render();
    const buttons = html.match(/<button\b[^>]*>(?:(?!<\/button>).)*<\/button>/gs) ?? [];
    for (const button of buttons) {
      const text = button.replace(/<[^>]+>/g, '').trim();
      expect(text !== '' || /aria-label="[^"]+"/.test(button)).toBe(true);
    }
    for (const key of ['from', 'to', 'move_up', 'move_down']) {
      expect(html).toContain(`aria-label="${i18n.t(`${prefix}${key}`)}"`);
    }
    expect(html).toContain(`role="radiogroup" aria-label="${i18n.t(`${prefix}target`)}"`);
  });

  test('malformed source is visibly blocked and offers explicit reset', () => {
    const html = render([{ ...rule, invalid: true }]);
    expect(html).toContain('role="alert"');
    expect(html).toContain(i18n.t(`${prefix}reset_invalid`));
    expect(html).toContain(i18n.t(`${prefix}invalid_hint`));
    expect(html).toMatch(/<textarea[^>]*disabled=""/);
  });

  test.each(['system', 'developer'])('renders %s as an editable role', (role) => {
    const html = render([{ ...rule, target: { ...rule.target, role } }]);
    expect(html).toMatch(
      new RegExp(
        `<button[^>]*aria-haspopup="listbox"[^>]*aria-label="${i18n.t(`${prefix}target_selector`)}"[^>]*><span[^>]*>${role}</span>`
      )
    );
    expect(html).not.toContain('role="alert"');
  });

  test('path targets render paths instead of message positions', () => {
    const html = render([
      {
        ...rule,
        target: { role: '', all: false, positions: '', paths: ['metadata.description'] },
        replacements: [{ ...rule.replacements[0], regex: true }],
      },
    ]);
    expect(html).toContain('value="metadata.description"');
    expect(html).toContain(i18n.t(`${prefix}paths_hint`));
    expect(html).toContain(i18n.t(`${prefix}pattern`));
    expect(html).toMatch(
      new RegExp(
        `<button[^>]*aria-haspopup="listbox"[^>]*aria-label="${i18n.t(`${prefix}replacement_type`)}"[^>]*>`
      )
    );
    expect(html).not.toMatch(/<select\b/);
    expect(html).not.toContain('role="radiogroup"');
    expect(html).not.toContain('role="alert"');
  });

  test('unsupported source role also offers reset instead of silently changing role', () => {
    const html = render([{ ...rule, target: { ...rule.target, role: 'assistant' } }]);
    expect(html).toContain('role="alert"');
    expect(html).toContain(i18n.t(`${prefix}reset_invalid`));
  });

  test('disabled state disables every button', () => {
    const buttons = render([rule], true).match(/<button\b[^>]*>/g) ?? [];
    expect(buttons.length).toBeGreaterThan(5);
    expect(buttons.every((button) => button.includes('disabled=""'))).toBe(true);
  });

  test('payload groups never remount and each opens only for its own errors', () => {
    const source = readFileSync(
      'src/features/config/components/sections/SectionPayload.tsx',
      'utf8'
    );
    const panels = source.split('<Collapsible').slice(1);
    expect(panels).toHaveLength(6);
    for (const panel of panels) expect(panel.split('>')[0]).not.toContain('key=');
    expect(source).not.toContain('hasPayloadValidationErrors');
    expect(source).toContain('open: Boolean(opened[group]) || groupErrors[group]');
    for (const group of [
      'payloadDefaultRules',
      'payloadDefaultRawRules',
      'payloadOverrideRules',
      'payloadOverrideRawRules',
      'payloadReplaceRules',
    ]) {
      expect(source).toContain(`{...groupProps('${group}')}`);
    }
  });

  test('both rule editors reuse the full model editor and edits preserve source-invalid flag', () => {
    const read = (name: string) =>
      readFileSync(`src/features/config/components/blocks/${name}.tsx`, 'utf8');
    const source = read('PayloadReplaceRulesEditor');
    expect(source).toMatch(
      /<Select\s+className=\{styles\.targetChoice\}[^>]*dropdownMinWidth=\{180\}/
    );
    expect(source).toContain('<PayloadModelsEditor');
    expect(read('PayloadRulesEditor')).toContain('<PayloadModelsEditor');
    expect(source).toContain('{ ...rule, ...patch }');
    expect(source).not.toContain('invalid: false');
    expect(source).not.toContain('.trim(');
    expect(source).toContain('move(value, ruleIndex, direction)');
    expect(source).toContain('move(rule.replacements, index, direction)');
  });
});
