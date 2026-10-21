import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import {
  readModelOptions,
  buildModelOptions,
  validateModelOptions,
} from '@/features/providers/modelOptions';
import { ModelAdvancedFields } from '@/features/providers/sheets/forms/ModelAdvancedFields';
import type { ModelEntryInput, ProviderBrand } from '@/features/providers/types';
import type { ModelAlias } from '@/types';

// Keep key-based assertions independent of other suites initializing the app's i18n.
const translations = createInstance();
await translations.init({ lng: 'cimode', resources: {}, react: { useSuspense: false } });

const draft = (model: ModelAlias): ModelEntryInput => ({
  name: model.name,
  thinkingJson: model.thinking === undefined ? undefined : JSON.stringify(model.thinking),
  ...readModelOptions(model),
});

describe('provider model options', () => {
  test('editing hybrid levels preserves budgets, explicit booleans and future fields', () => {
    const thinking = {
      min: 128,
      max: 4096,
      zero_allowed: true,
      dynamic_allowed: false,
      levels: ['low'],
      future: { mode: 'keep' },
    };
    const entry = draft({ name: 'model', thinking });
    expect(entry.thinkingLevels).toEqual(['low']);
    expect(buildModelOptions(entry).thinking).toEqual(thinking);
    expect(
      buildModelOptions({ ...entry, thinkingLevels: ['high'], thinkingLevelsTouched: true })
        .thinking
    ).toEqual({ ...thinking, levels: ['high'] });
    expect(
      buildModelOptions({ ...entry, thinkingLevels: [], thinkingLevelsTouched: true }).thinking
    ).toEqual({
      min: 128,
      max: 4096,
      zero_allowed: true,
      dynamic_allowed: false,
      future: { mode: 'keep' },
    });
  });

  test('unset, empty custom object and cleared override are distinct', () => {
    expect(buildModelOptions(draft({ name: 'model' })).thinking).toBeUndefined();
    expect(buildModelOptions(draft({ name: 'model', thinking: {} })).thinking).toEqual({});
    expect(buildModelOptions({ name: 'model', thinkingEnabled: true }).thinking).toEqual({});
    expect(
      buildModelOptions({
        ...draft({ name: 'model', thinking: { min: 1 } }),
        thinkingEnabled: false,
      }).thinking
    ).toBeUndefined();
  });

  test('zero and false survive; budget edits preserve levels without adding absent flags', () => {
    const entry = draft({
      name: 'model',
      maxContextLength: 0,
      forceMapping: false,
      isCompat: false,
      supportConfigurationUpdate: false,
      useMaxCompletionTokens: false,
      thinking: { levels: ['high'], min: 1, max: 10 },
    });
    expect(
      buildModelOptions({
        ...entry,
        thinkingMin: '0',
        thinkingMax: '',
        thinkingBudgetTouched: true,
      })
    ).toMatchObject({
      maxContextLength: 0,
      forceMapping: false,
      isCompat: false,
      supportConfigurationUpdate: false,
      useMaxCompletionTokens: false,
      thinking: { levels: ['high'], min: 0 },
    });
    expect(
      buildModelOptions({
        ...entry,
        thinkingMin: '0',
        thinkingMax: '',
        thinkingBudgetTouched: true,
      }).thinking
    ).toEqual({ levels: ['high'], min: 0 });
    expect(
      buildModelOptions({ ...entry, thinkingZeroAllowed: false, thinkingBudgetTouched: true })
        .thinking?.zero_allowed
    ).toBe(false);
  });

  test('modalities accept future values and empty text restores defaults', () => {
    expect(
      buildModelOptions({
        name: 'model',
        inputModalitiesText: 'text, image\nfuture audio',
        outputModalitiesText: ' ',
      })
    ).toMatchObject({
      inputModalities: ['text', 'image', 'future', 'audio'],
      outputModalities: undefined,
    });
    expect(
      buildModelOptions({ name: 'model', displayName: ' Catalog ', maxContextLength: '' })
    ).toMatchObject({ displayName: 'Catalog', maxContextLength: undefined });
  });

  test('preserves untouched empty modalities and explicit null thinking budgets', () => {
    const entry = draft({
      name: 'model',
      inputModalities: [],
      outputModalities: [],
      thinking: { min: null, max: null, levels: ['high'] },
    });
    expect(validateModelOptions([entry])).toBeNull();
    expect(buildModelOptions(entry)).toMatchObject({
      inputModalities: [],
      outputModalities: [],
      thinking: { min: null, max: null, levels: ['high'] },
    });
    expect(
      buildModelOptions({ ...entry, inputModalitiesText: '', inputModalitiesTouched: true })
        .inputModalities
    ).toBeUndefined();
    expect(
      buildModelOptions({ ...entry, thinkingMin: '0', thinkingBudgetTouched: true }).thinking
    ).toEqual({ min: 0, levels: ['high'] });
  });
  test('validates budgets using backend zero-bound semantics without blocking untouched imports', () => {
    for (const value of ['-1', '1.5', 'Infinity', '9007199254740992', '1e3']) {
      expect(validateModelOptions([{ name: 'model', maxContextLength: value }])).toBe(
        'providersPage.modelOptions.invalidContext'
      );
      expect(
        validateModelOptions([{ name: 'model', thinkingEnabled: true, thinkingMin: value }])
      ).toBe('providersPage.modelOptions.invalidBudget');
    }
    expect(
      validateModelOptions([
        { name: 'model', thinkingEnabled: true, thinkingMin: '20', thinkingMax: '10' },
      ])
    ).toBe('providersPage.modelOptions.invalidRange');
    expect(
      validateModelOptions([
        { name: 'model', thinkingEnabled: true, thinkingMin: '20', thinkingMax: '0' },
      ])
    ).toBe('providersPage.modelOptions.invalidRange');
    expect(
      validateModelOptions([{ name: 'model', thinkingEnabled: true, thinkingMin: '20' }])
    ).toBe('providersPage.modelOptions.invalidRange');
    expect(
      validateModelOptions([
        { name: 'model', thinkingEnabled: true, thinkingMin: '0', thinkingMax: '0' },
      ])
    ).toBeNull();
    expect(
      validateModelOptions([draft({ name: 'model', thinking: { min: 20, max: 0 } })])
    ).toBeNull();
    expect(validateModelOptions([{ name: 'model', thinkingJson: '[]' }])).toBe(
      'providersPage.modelOptions.invalidThinking'
    );
    expect(
      validateModelOptions([
        { name: 'model', thinkingEnabled: false, thinkingJson: 'invalid', thinkingMin: '-1' },
      ])
    ).toBeNull();
  });

  const render = (brand: ProviderBrand, disabled = false, enabled = true) =>
    renderToStaticMarkup(
      createElement(
        I18nextProvider,
        { i18n: translations },
        createElement(ModelAdvancedFields, {
          entry: { name: 'model', thinkingEnabled: enabled },
          providerBrand: brand,
          disabled,
          supportsThinking: true,
          onUpdate: () => {},
        })
      )
    );

  test('gates fields by provider capability', () => {
    const vertex = render('vertex');
    expect(vertex).toContain('providersPage.modelOptions.displayName');
    expect(vertex).toContain('providersPage.modelOptions.forceMapping');
    expect(vertex).not.toContain('providersPage.modelOptions.maxContextLength');
    expect(vertex).not.toContain('providersPage.modelOptions.isCompat');
    for (const brand of ['gemini', 'codex', 'openaiCompatibility'] as const) {
      const html = render(brand);
      expect(html).toContain('providersPage.modelOptions.maxContextLength');
      expect(html).toContain('providersPage.modelOptions.isCompat');
      expect(html.includes('providersPage.modelOptions.supportConfigurationUpdate')).toBe(
        brand === 'codex'
      );
      expect(html.includes('providersPage.modelOptions.inputModalitiesText')).toBe(
        brand === 'openaiCompatibility'
      );
      expect(html.includes('providersPage.modelOptions.useMaxCompletionTokens')).toBe(
        brand === 'openaiCompatibility'
      );
    }
  });

  test('mutating disables every control; default thinking disables budgets and levels', () => {
    const controls =
      render('openaiCompatibility', true).match(/<(?:input|select|button)\b[^>]*>/g) ?? [];
    expect(controls.length).toBeGreaterThan(10);
    for (const control of controls) expect(control).toContain('disabled=""');
    const defaultThinking = render('gemini', false, false).split('<fieldset')[1];
    const inputs = defaultThinking.match(/<(?:input|button)\b[^>]*>/g) ?? [];
    expect(inputs.length).toBeGreaterThan(4);
    const mode = inputs.filter((input) => input.includes('aria-haspopup="listbox"'));
    expect(mode).toHaveLength(1);
    expect(mode[0]).not.toContain('disabled=""');
    for (const input of inputs.filter((input) => !input.includes('aria-haspopup="listbox"'))) {
      expect(input).toContain('disabled=""');
    }
  });
});
