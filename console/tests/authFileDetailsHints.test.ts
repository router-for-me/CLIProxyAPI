// 凭证详情抽屉与排除模型选择器的说明文字：哪些收进 InfoTip、哪些保持可见、哪些删除。
// 静态渲染只覆盖关闭态结构与无障碍关联；悬停 / 聚焦 / 点按需在浏览器里核对。

import { describe, expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { MemoryRouter } from 'react-router-dom';
import i18n from '@/i18n';
import en from '@/i18n/locales/en.json';
import { AuthFileDetailsSheet } from '@/features/authFiles/components/AuthFileDetailsSheet';
import { FieldLabel } from '@/components/ui/FieldLabel';
import type { PrefixProxyEditorState } from '@/features/authFiles/hooks/useAuthFilesPrefixProxyEditor';
import { ExcludedModelsPicker } from '@/components/excludedModels';

const enI18n = i18n.cloneInstance({ lng: 'en' });
const render = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(
    createElement(I18nextProvider, { i18n: enI18n }, createElement(MemoryRouter, null, element))
  );
const esc = (text: string) => Bun.escapeHTML(text);
const copy = en.auth_files;
const noop = () => {};

const editor = (providerKey: string): PrefixProxyEditorState => ({
  fileName: `${providerKey}.json`,
  fileInfoText: '{}',
  loading: false,
  saving: false,
  error: null,
  originalText: '{}',
  rawText: '{}',
  invalidContentPreview: '',
  json: { type: providerKey },
  providerKey,
  prefix: '',
  proxyUrl: '',
  priority: '',
  weight: '',
  weightError: null,
  disableCooling: false,
  disableCoolingTouched: false,
  websockets: false,
  websocketsTouched: false,
  useSiwc: false,
  useSiwcTouched: false,
  usingApi: false,
  usingApiTouched: false,
  note: '',
  noteTouched: false,
  excludedModelsText: '',
  excludedModelsTouched: false,
  headersText: '',
  headersTouched: false,
  headersError: null,
});

const renderSheet = (providerKey: string) =>
  render(
    createElement(AuthFileDetailsSheet, {
      disableControls: false,
      editor: editor(providerKey),
      updatedText: '{}',
      dirty: false,
      onClose: noop,
      onCopyText: noop,
      onSave: noop,
      onChange: noop,
    })
  );

/** The InfoTip button right after a label (FieldLabel or Input label row). */
const tipAfterLabel = (markup: string, label: string) =>
  markup
    .slice(markup.indexOf(`>${esc(label)}</label>`))
    .match(/^[^<]*<\/label><button\b[^>]*data-info-tip=""[^>]*>/)?.[0];
const reEscape = (text: string) => text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
const describedBy = (tag: string) => tag.match(/aria-describedby="([^"]+)"/)?.[1];

describe('FieldLabel as used by the details sheet', () => {
  test('keeps a real <label> (styled by .form-group label) with a hidden description copy', () => {
    const markup = render(
      createElement(FieldLabel, {
        as: 'label',
        label: 'Mode',
        id: 'm',
        hint: 'Zero means off',
        hintId: 'h',
      })
    );
    expect(markup).toMatch(/^<div class="form-label-row"><label id="m">Mode<\/label>/);
    const tip = tipAfterLabel(markup, 'Mode');
    expect(tip).toContain('aria-label="More information about Mode"');
    expect(describedBy(tip!)).toBe('h');
    expect(markup).toContain('<span id="h" hidden="">Zero means off</span>');
    expect(markup.split('Zero means off')).toHaveLength(2);
  });
});

describe('auth file details sheet hints', () => {
  test('toggle explanations move into InfoTips that still describe each switch', () => {
    const codex = renderSheet('codex');
    const xai = renderSheet('xai');
    const cases = [
      [codex, copy.disable_cooling_label, copy.disable_cooling_hint],
      [codex, copy.websockets_label, copy.websockets_hint],
      [codex, copy.use_siwc_label, copy.use_siwc_hint],
      [xai, copy.using_api_label, copy.using_api_hint],
    ] as const;
    for (const [markup, label, hint] of cases) {
      const tip = tipAfterLabel(markup, label);
      expect(tip).toBeDefined();
      const hintId = describedBy(tip!)!;
      expect(markup).toContain(`<span id="${hintId}" hidden="">${esc(hint)}</span>`);
      const toggle = markup.match(
        new RegExp(`<input[^>]*aria-label="${reEscape(esc(label))}"[^>]*>`)
      )?.[0];
      expect(toggle).toContain(`aria-describedby="${hintId}"`);
      // Only the hidden copy carries the text; no visible hint line remains.
      expect(markup.split(esc(hint))).toHaveLength(2);
    }
  });

  test('priority uses the Input InfoTip', () => {
    const markup = renderSheet('codex');
    expect(tipAfterLabel(markup, copy.priority_label)).toBeDefined();
  });

  test('weight: semantics in an InfoTip, the strategy link stays a visible, reachable note', () => {
    const markup = renderSheet('codex');

    // The value semantics are tooltip content, kept as a visually hidden description.
    const tip = tipAfterLabel(markup, copy.weight_label);
    expect(tip).toBeDefined();
    const hintId = describedBy(tip!)!;
    expect(markup).toContain(
      `<div id="${hintId}" class="hint visually-hidden-focusable">${esc(copy.weight_hint)}</div>`
    );

    // The link sentence is its own visible line, not hidden and not inside the tooltip.
    const linkText = copy.weight_strategy_note.match(/<settingsLink>(.*)<\/settingsLink>/)![1];
    const note = markup.match(/<div id="([^"]+-note)" class="hint">([\s\S]*?)<\/div>/);
    expect(note).not.toBeNull();
    expect(note![2]).toContain(esc(linkText));
    expect(note![2]).toMatch(/<a [^>]*href="#?\/config\?field=routingStrategy"/);

    // The input is described by both the hint and the note.
    const input = markup.match(new RegExp(`<input[^>]*aria-describedby="[^"]*${note![1]}"[^>]*>`));
    expect(input?.[0]).toContain(hintId);
    expect(input?.[0]).toContain('type="number"');
  });

  test('removed copy: note "optional" hint and the headers format hint repeated by the placeholder', () => {
    const markup = renderSheet('codex');
    // Source check (not locale text) so it survives the keys being deleted from the locales.
    const source = readFileSync(
      'src/features/authFiles/components/AuthFileDetailsSheet.tsx',
      'utf8'
    );
    expect(source).not.toContain("'auth_files.note_hint'");
    expect(source).not.toContain("'auth_files.headers_hint'");
    // The placeholders that make them redundant are still there.
    expect(markup).toContain(esc(copy.note_placeholder));
    expect(markup).toContain(esc(copy.headers_placeholder));
    expect(tipAfterLabel(markup, copy.note_label)).toBeUndefined();
  });
});

describe('excluded models picker rule hint', () => {
  test('wildcard semantics sit in an InfoTip after the rules label and describe the textarea', () => {
    const markup = render(
      createElement(ExcludedModelsPicker, { value: [], onChange: noop, candidates: [] })
    );
    const rulesLabel = en.excluded_models.rules_label;
    const rulesHint = esc(en.excluded_models.rules_hint);
    const tip = tipAfterLabel(markup, rulesLabel);
    expect(tip).toContain(`aria-label="More information about ${esc(rulesLabel)}"`);
    const hintId = describedBy(tip!)!;
    expect(markup).toContain(`<span id="${hintId}" hidden="">${rulesHint}</span>`);
    expect(markup.match(/<textarea\b[^>]*>/)?.[0]).toContain(`aria-describedby="${hintId}"`);
    expect(markup).not.toContain('class="hint"');
    expect(markup.split(rulesHint)).toHaveLength(2);
  });

  test('no rule editor, no hint', () => {
    const markup = render(
      createElement(ExcludedModelsPicker, {
        value: [],
        onChange: noop,
        candidates: [],
        showRuleEditor: false,
      })
    );
    expect(markup).not.toContain('data-info-tip');
  });
});
