import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { I18nextProvider } from 'react-i18next';
import { createInstance } from 'i18next';
import { AliasLedgerRow } from '../src/features/authFiles/modelRules/AliasLedger';
import {
  ModelRulesWorkspace,
  ProviderRail,
  type ModelRulesWorkspaceProps,
} from '../src/features/authFiles/modelRules/ModelRulesWorkspace';
import en from '../src/i18n/locales/en.json';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const noop = () => {};
const render = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n }, element));

function renderRow(
  overrides: { disabled?: boolean; issue?: 'incomplete' | 'same' | 'duplicate' } = {}
) {
  return render(
    createElement(AliasLedgerRow, {
      entry: { id: 'row', name: 'source-model', alias: 'client-model', fork: true },
      index: 1,
      options: [{ value: 'source-model' }],
      disabled: false,
      onChange: noop,
      onRemove: noop,
      ...overrides,
    })
  );
}

function renderWorkspace(overrides: Partial<ModelRulesWorkspaceProps> = {}) {
  return render(
    createElement(ModelRulesWorkspace, {
      kind: 'alias',
      title: 'OAuth Model Aliases',
      meta: ['2 providers', '5 mappings'],
      status: 'ready',
      errorMessage: '',
      unsupportedTitle: 'Upgrade required',
      unsupportedDescription: 'Upgrade the server',
      onRetry: noop,
      onBack: noop,
      onSwitchKind: noop,
      rail: createElement('nav', { id: 'rail' }),
      dirtyCount: 0,
      saving: false,
      canSave: false,
      onSave: noop,
      onDiscard: noop,
      children: createElement('div', { id: 'stage-content' }),
      ...overrides,
    })
  );
}

describe('model rules workspace UI', () => {
  test('mapping fields keep associated labels and a contextual delete name', () => {
    const markup = renderRow();
    expect(markup).toContain('aria-label="Alias mapping 2"');
    expect(markup).toContain('aria-label="Delete alias mapping 2"');
    expect(markup).toContain('value="source-model"');
    expect(markup).toContain('value="client-model"');
    for (const suffix of ['source', 'alias']) {
      const id = markup.match(new RegExp(`<input id="([^"]+-${suffix})"`))?.[1];
      expect(id).toBeDefined();
      expect(markup).toContain(`for="${id}"`);
    }
    expect(markup).toContain('checked=""');
    expect(markup).not.toContain('aria-invalid');
  });

  test('disables every control of a row while saving or disconnected', () => {
    // source input, alias input, keep-original switch, delete button
    expect(renderRow({ disabled: true }).match(/disabled=""/g)).toHaveLength(4);
    expect(renderRow()).not.toContain('disabled=""');
  });

  test('explains a row the backend would silently drop', () => {
    const markup = renderRow({ issue: 'incomplete' });
    const issueId = markup.match(/aria-describedby="([^"]+)"/)?.[1];
    expect(issueId).toBeDefined();
    expect(markup).toContain(`id="${issueId}"`);
    expect(markup).toContain(en.model_rules.row_incomplete);
    expect(markup).toContain('aria-invalid="true"');
    expect(renderRow({ issue: 'duplicate' })).toContain(en.oauth_model_alias.duplicate_alias);
    expect(renderRow({ issue: 'same' })).toContain(en.model_rules.row_same);
  });

  test('rail marks exactly one current scope and flags unsaved providers', () => {
    const markup = render(
      createElement(ProviderRail, {
        items: [
          { key: 'codex', label: 'Codex', icon: null, count: 3, dirty: true },
          { key: 'claude', label: 'Claude', icon: null, count: 0, dirty: false },
        ],
        selected: 'codex',
        total: 3,
        disabled: false,
        onSelect: noop,
      })
    );
    expect(markup.match(/aria-current="true"/g)).toHaveLength(1);
    expect(markup).toContain(`aria-label="${en.model_rules.unsaved_mark}"`);
    expect(markup).toContain(`aria-label="${en.model_rules.custom_provider_label}"`);
    expect(markup).toContain(`aria-label="${en.model_rules.rail_label}"`);
  });

  test('shows the workspace only once the baseline is ready', () => {
    const ready = renderWorkspace();
    expect(ready).toContain('id="rail"');
    expect(ready).toContain('id="stage-content"');
    expect(ready).toContain('2 providers');
    expect(ready.match(/aria-pressed="true"/g)).toHaveLength(1);

    const loading = renderWorkspace({ status: 'loading' });
    expect(loading).toContain('aria-busy="true"');
    expect(loading).not.toContain('id="stage-content"');

    const unsupported = renderWorkspace({ status: 'unsupported' });
    expect(unsupported).toContain('Upgrade required');
    expect(unsupported).not.toContain('id="stage-content"');

    const failed = renderWorkspace({ status: 'error', errorMessage: 'boom' });
    expect(failed).toContain('boom');
    expect(failed).toContain('empty-action');
    expect(failed).not.toContain('id="stage-content"');
  });

  test('the header carries only title, telemetry and controls, with no subtitle', () => {
    const markup = renderWorkspace();
    const header = markup.slice(markup.indexOf('<header'), markup.indexOf('</header>'));
    expect(header).toContain('<h1');
    expect(header).toContain('OAuth Model Aliases');
    // The single paragraph left in the header is the mono telemetry line.
    expect(header.match(/<p\b/g)).toHaveLength(1);
    expect(header).toContain('5 mappings');
  });
});
