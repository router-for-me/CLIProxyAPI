import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import en from '@/i18n/locales/en.json';
import type { ProviderRecentUsageMap } from '@/components/providers/utils';
import {
  claudeToResource,
  fennoAIToResource,
  geminiToResource,
  openaiToResource,
} from '@/features/providers/adapters';
import { BrandTabs } from '@/features/providers/components/BrandTabs';
import { ProviderPicker } from '@/features/providers/components/ProviderPicker';
import { ResourceCard } from '@/features/providers/components/ResourceCard';
import { FormDisclosure } from '@/features/providers/sheets/forms/FormSection';
import {
  collectGroupModels,
  getConnectionFormTarget,
  getResourceDisplayName,
  matchesResourceFilter,
  partitionProviderGroups,
  resolveResourceUsage,
  selectVisibleResources,
} from '@/features/providers/resourceView';
import type { ProviderGroup, ProviderResource } from '@/features/providers/types';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const render = (element: ReturnType<typeof createElement>) =>
  renderToStaticMarkup(createElement(I18nextProvider, { i18n }, element));

const emptyUsage: ProviderRecentUsageMap = new Map();

const gemini = (overrides: Record<string, unknown>, index: number) =>
  geminiToResource({ apiKey: `AIzaSyFIXTURE${index}`, ...overrides }, index);

const alpha = gemini(
  {
    baseUrl: 'https://alpha.example.com',
    prefix: 'team',
    priority: 5,
    models: [{ name: 'gemini-pro' }],
    source: { group: { name: 'alpha' } },
  },
  0
);
const beta = gemini(
  {
    baseUrl: 'https://beta.example.com',
    priority: 1,
    models: [{ name: 'gemini-flash' }],
    source: { group: { name: 'beta' } },
  },
  1
);
const disabled = gemini({ excludedModels: ['*'], source: { group: { name: 'gamma' } } }, 2);

const group = (id: ProviderGroup['id'], resources: ProviderResource[] = []): ProviderGroup => ({
  id,
  resources,
});

describe('resource view logic', () => {
  test('filter matches identity, endpoint and prefix case-insensitively', () => {
    expect(matchesResourceFilter(alpha, '')).toBe(true);
    expect(matchesResourceFilter(alpha, 'ALPHA.example')).toBe(true);
    expect(matchesResourceFilter(alpha, 'team')).toBe(true);
    expect(matchesResourceFilter(beta, 'team')).toBe(false);
  });

  test('visible resources apply text and model filters, then a stable sort', () => {
    const resources = [beta, alpha, disabled];
    const query = { filter: '', selectedModels: new Set<string>(), sortBy: 'name', sortDir: 'asc' };
    expect(
      selectVisibleResources(resources, query as never, emptyUsage).map((r) => r.name)
    ).toEqual(['alpha', 'beta', 'gamma']);
    expect(
      selectVisibleResources(
        resources,
        { ...query, sortBy: 'priority', sortDir: 'desc' } as never,
        emptyUsage
      ).map((r) => r.name)
    ).toEqual(['alpha', 'beta', 'gamma']);
    expect(
      selectVisibleResources(
        resources,
        { ...query, selectedModels: new Set(['gemini-flash']) } as never,
        emptyUsage
      ).map((r) => r.name)
    ).toEqual(['beta']);
    expect(collectGroupModels(group('gemini', resources))).toEqual(['gemini-flash', 'gemini-pro']);
  });

  test('quick-fill partners are split out and ordered after regular providers', () => {
    const { providers, quickFill } = partitionProviderGroups([
      group('aiCodeMirror'),
      group('gemini'),
      group('fennoAI'),
      group('kimi'),
      group('qiniuCloud'),
    ]);
    expect(providers.map((g) => g.id)).toEqual(['gemini', 'kimi']);
    expect(quickFill.map((g) => g.id)).toEqual(['fennoAI', 'qiniuCloud', 'aiCodeMirror']);
  });

  test('sponsor bundles carry no request health; others always resolve a status bar', () => {
    const fenno = fennoAIToResource({
      openai: [],
      claude: [],
      codex: [{ config: { apiKey: 'fenno-key', baseUrl: 'https://api.fenno.ai/v1' }, index: 0 }],
      gemini: [],
    });
    expect(fenno).not.toBeNull();
    expect(resolveResourceUsage(fenno!, emptyUsage)).toBeNull();
    const usage = resolveResourceUsage(alpha, emptyUsage);
    expect(usage).toMatchObject({ success: 0, failure: 0 });
    expect(usage!.statusData.blockDetails.length).toBeGreaterThan(0);
    expect(getResourceDisplayName(alpha)).toBe('alpha');
  });
});

describe('resource card', () => {
  const card = (resource: ProviderResource, disableMutations = false) =>
    render(
      createElement(ResourceCard, {
        resource,
        selected: false,
        disableMutations,
        usage: resolveResourceUsage(resource, emptyUsage),
        onView: () => {},
        onEdit: () => {},
        onDelete: () => {},
        onToggleDisabled: () => {},
      })
    );

  test('shows identity, endpoint, prefix and capability chips with named actions', () => {
    const html = card(alpha);
    expect(html).toMatch(/<article[^>]*aria-labelledby="([^"]+)"/);
    expect(html).toContain('>alpha</button>');
    expect(html).toContain('https://alpha.example.com');
    expect(html).toContain('team');
    expect(html).toContain(en.providersPage.card.requests);
    for (const action of ['view', 'edit', 'delete'] as const) {
      expect(html).toContain(`aria-label="${en.providersPage.actions[action]}"`);
    }
    expect(html).not.toContain(en.providersPage.status.disabled);
  });

  test('disabled resources show their state and mutations lock while busy', () => {
    const html = card(disabled, true);
    expect(html).toContain(en.providersPage.status.disabled);
    const edit = new RegExp(`<button[^>]*aria-label="${en.providersPage.actions.edit}"[^>]*>`);
    expect(edit.exec(html)![0]).toContain('disabled=""');
    const view = new RegExp(`<button[^>]*aria-label="${en.providersPage.actions.view}"[^>]*>`);
    expect(view.exec(html)![0]).not.toContain('disabled=""');
  });

  test('claude without a base URL shows the default endpoint; OpenAI shows key count', () => {
    expect(card(claudeToResource({ apiKey: 'sk-ant-fixture' }, 0))).toContain(
      'https://api.anthropic.com'
    );
    const openai = openaiToResource(
      {
        name: 'router',
        baseUrl: 'https://router.example.com/v1',
        apiKeyEntries: [{ apiKey: 'sk-one' }, { apiKey: 'sk-two' }],
      },
      0
    );
    const html = card(openai);
    expect(html).toContain('+1');
    expect(html).toContain(en.providersPage.table.metrics.keys);
  });
  test('sponsor bundles show their resolved endpoint instead of repeating protocols', () => {
    const fenno = fennoAIToResource({
      openai: [],
      claude: [],
      codex: [{ config: { apiKey: 'fenno-key', baseUrl: 'https://api.fenno.ai/v1' }, index: 0 }],
      gemini: [],
    })!;
    const html = card(fenno);
    expect(html).toMatch(/title="https:\/\/api\.fenno\.ai[^"]*"/);
    expect(html).not.toContain(en.providersPage.card.requests);
  });
});

describe('brand tabs', () => {
  test('list every brand and pin a quick-connect invitation for unconnected partners', () => {
    const html = render(
      createElement(BrandTabs, {
        groups: [group('gemini', [alpha, beta]), group('codex', [disabled]), group('fennoAI')],
        activeBrand: 'gemini',
        onSelect: () => {},
        tabId: (brand: string) => `tab-${brand}`,
        panelId: 'panel',
        onAdd: () => {},
        partnerBrands: ['fennoAI', 'qiniuCloud'],
      })
    );
    expect(html).toContain('role="tablist"');
    const tabs = [...html.matchAll(/<button[^>]*role="tab"[^>]*>/g)].map((m) => m[0]);
    expect(tabs).toHaveLength(3);
    // Partners stay in the strip, after a labelled divider, without a zero badge.
    expect(html.indexOf(en.providersPage.categories.quickFill)).toBeLessThan(
      html.indexOf('id="tab-fennoAI"')
    );
    expect(html).not.toContain('>0</span>');
    const active = tabs.find((tab) => tab.includes('id="tab-gemini"'))!;
    expect(active).toContain('aria-selected="true"');
    expect(active).toContain('tabindex="0"');
    expect(active).toContain('aria-controls="panel"');
    expect(tabs.find((tab) => tab.includes('id="tab-codex"'))).toContain('tabindex="-1"');
    const invite = new RegExp(
      `<button[^>]*aria-label="${en.providersPage.picker.moreQuickConnect}"[^>]*>`
    ).exec(html);
    expect(invite).not.toBeNull();
    expect(invite![0]).toContain('FennoAI');
    // The invitation sits outside the tablist so it is not announced as a tab.
    expect(html.indexOf('</div>')).toBeLessThan(html.indexOf(invite![0]));
  });

  test('omit the invitation once every partner is connected', () => {
    const html = render(
      createElement(BrandTabs, {
        groups: [group('gemini', [alpha])],
        activeBrand: 'gemini',
        onSelect: () => {},
        tabId: (brand: string) => `tab-${brand}`,
        panelId: 'panel',
        onAdd: () => {},
      })
    );
    expect(html).not.toContain(en.providersPage.picker.moreQuickConnect);
  });
});

describe('provider picker', () => {
  const catalogue = [
    group('kimi'),
    group('gemini', [alpha]),
    group('codex'),
    group('openaiCompatibility'),
    group('fennoAI'),
    group('qiniuCloud'),
    group('aiCodeMirror'),
  ];

  test('features quick-connect partners first with connect and register actions', () => {
    const html = render(createElement(ProviderPicker, { groups: catalogue, onPick: () => {} }));
    const featured = html.indexOf(en.providersPage.picker.featured);
    const official = html.indexOf(en.providersPage.picker.official);
    expect(featured).toBeGreaterThanOrEqual(0);
    expect(featured).toBeLessThan(official);
    for (const name of ['FennoAI', en.providersPage.providerNames.qiniuCloud]) {
      expect(html).toContain(`aria-label="${en.providersPage.picker.connect} ${name}"`);
      expect(html.indexOf(name)).toBeLessThan(official);
    }
    const links = [...html.matchAll(/<a[^>]*target="_blank"[^>]*>/g)];
    // Three partners plus the Kimi registration link.
    expect(links).toHaveLength(4);
    expect(html).toContain(en.providersPage.picker.oneKey.replace('{{count}}', '2'));
  });

  test('edits the full connected partner bundle but allows another ordinary connection', () => {
    const bundle = fennoAIToResource({
      openai: [],
      gemini: [],
      claude: [{ config: { apiKey: 'fixture-claude' }, index: 0 }],
      codex: [{ config: { apiKey: 'fixture-codex' }, index: 0 }],
    })!;
    const partner = group('fennoAI', [bundle]);
    expect(getConnectionFormTarget(partner)).toEqual({ mode: 'edit', resource: bundle });
    expect(getConnectionFormTarget(group('fennoAI'))).toEqual({ mode: 'create', resource: null });
    expect(getConnectionFormTarget(group('gemini', [alpha])).mode).toBe('create');
    const html = render(createElement(ProviderPicker, { groups: [partner], onPick: () => {} }));
    expect(html).toContain(`aria-label="${en.providersPage.actions.edit} FennoAI"`);
  });

  test('marks connected brands and keeps the custom entry last', () => {
    const html = render(createElement(ProviderPicker, { groups: catalogue, onPick: () => {} }));
    expect(html).toContain(en.providersPage.picker.connected.replace('{{count}}', '1'));
    expect(html.indexOf(en.providersPage.picker.custom)).toBeGreaterThan(
      html.indexOf(en.providersPage.picker.official)
    );
    expect(html.lastIndexOf(en.providersPage.providerNames.openaiCompatibility)).toBeGreaterThan(
      html.indexOf(en.providersPage.picker.custom)
    );
  });
});

describe('form disclosure', () => {
  test('keeps collapsed content in the DOM and summarises with a count', () => {
    const html = render(createElement(FormDisclosure, { label: 'Models', count: 3 }, 'inner'));
    expect(html).toMatch(/^<details/);
    expect(html).not.toContain(' open=""');
    expect(html).toContain('>Models</span>');
    expect(html).toContain('>3</span>');
    expect(html).toContain('inner');
    expect(
      render(createElement(FormDisclosure, { label: 'Keys', defaultOpen: true }, 'x'))
    ).toContain(' open=""');
  });
});
