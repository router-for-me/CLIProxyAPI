import { describe, expect, test } from 'bun:test';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import { createInstance } from 'i18next';
import { I18nextProvider } from 'react-i18next';
import en from '@/i18n/locales/en.json';
import { normalizeConfigResponse } from '@/services/api/transformers';
import { buildProviderGroups } from '@/features/providers/useProviderWorkbench';
import { connectionCounts } from '@/features/providers/resourceView';
import { selectVisibleSections } from '@/features/providers/nativeSections';
import { BrandPanel } from '@/features/providers/components/BrandPanel';

const i18n = createInstance();
await i18n.init({ lng: 'en', resources: { en: { translation: en } } });
const config = normalizeConfigResponse({
  'api-keys': {
    codex: [
      {
        name: 'West',
        'base-url': 'https://user:fixture-pass@west.invalid/v1?key=fixture-token',
        prefix: 'fixture-routing-prefix',
        keys: [{ 'api-key': 'fixture-one-credential' }, { 'api-key': 'fixture-two-credential' }],
      },
      { name: 'Empty', keys: [] },
      { name: 'West', keys: [{ 'api-key': 'fixture-three-credential' }] },
    ],
  },
});
const families = buildProviderGroups(config);
const group = families.find((g) => g.id === 'codex')!;
const sections = group.nativeSections!;
const query = {
  filter: '',
  sortBy: 'name',
  sortDir: 'asc',
  selectedModels: new Set<string>(),
} as const;
const usage = new Map();
const noop = () => {};

describe('native workbench', () => {
  test('keeps empty and same-name groups separate, without changing compatibility tabs', () => {
    expect(sections.map((section) => [section.source.groupIndex, section.members.length])).toEqual([
      [0, 2],
      [1, 0],
      [2, 1],
    ]);
    expect(families.find((g) => g.id === 'openaiCompatibility')?.nativeSections).toBeUndefined();
    expect(families.find((g) => g.id === 'fennoAI')?.nativeSections).toBeUndefined();
  });
  test('counts connections, not member credentials, including empty connections', () => {
    expect(connectionCounts(group)).toEqual({ total: 3, active: 2 });
  });
  test('matches by member without dropping siblings, and includes matching empty connections', () => {
    const find = (filter: string) => selectVisibleSections(sections, { ...query, filter }, usage);
    expect(find('wEsT').map((s) => s.members.length)).toEqual([2, 1]);
    expect(find('fixture-two').map((s) => s.members.map((m) => m.originalIndex))).toEqual([[0, 1]]);
    expect(find('').map((s) => s.name)).toEqual(['Empty', 'West', 'West']);
    expect(find('EMPTY').map((s) => s.name)).toEqual(['Empty']);
    expect(find('absent')).toEqual([]);
  });
  test('renders one card per connection: the card opens the editor, the switch toggles keys', () => {
    const html = renderConnections(sections);
    const cards = html.match(/<article\b[\s\S]*?<\/article>/g) ?? [];
    expect(cards).toHaveLength(3);
    const textOf = (card: string) => card.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ');
    expect(textOf(cards[0])).toContain('West');
    expect(cards[0]).toContain('https://west.invalid/v1');
    expect(textOf(cards[0])).toContain('2 keys');
    expect(textOf(cards[1])).toContain('Empty');
    expect(textOf(cards[1])).toContain('0 keys');
    expect(textOf(cards[2])).toContain('1 key');
    for (const card of cards) {
      // Editing is the card itself; no footer of icon buttons, no delete on the surface.
      expect(card).not.toContain('<footer');
      expect(card).toContain(en.providersPage.connections.edit);
      expect(card).not.toContain(en.providersPage.actions.delete);
      // Zero counts are defaults and stay off the card.
      expect(card).not.toContain(en.providersPage.table.metrics.keys);
      expect(card).not.toContain(en.providersPage.table.metrics.models);
      expect(card).not.toContain(en.providersPage.table.metrics.headers);
      expect(card).toContain(en.providersPage.card.requests);
      expect(card).toContain('type="checkbox"');
    }
    // An empty connection has nothing to switch.
    expect(cards[1]).toMatch(/<input[^>]*type="checkbox"[^>]*disabled=""/);
    expect(cards[0]).not.toMatch(/<input[^>]*type="checkbox"[^>]*disabled=""/);
    for (const secret of [
      'fixture-pass',
      'fixture-token',
      'fixture-one-credential',
      'fixture-two-credential',
      'fixture-three-credential',
    ]) {
      expect(html).not.toContain(secret);
    }
    expect(html).not.toContain(en.providersPage.table.noMatch);
  });

  test('credential searches show the owning connection and its total key count', () => {
    const matches = selectVisibleSections(sections, { ...query, filter: 'fixture-two' }, usage);
    const html = renderConnections(matches, true);
    expect(html.match(/<article\b/g)).toHaveLength(1);
    expect(html.replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ')).toContain('2 keys');
    expect(html).toMatch(/<article\b[^]*?<button[^>]*disabled/);
    expect(html).not.toContain('credential');
    expect(renderConnections([])).toContain(en.providersPage.table.noMatch);
  });
});

function renderConnections(nativeSections: typeof sections, disableMutations = false) {
  return renderToStaticMarkup(
    createElement(
      I18nextProvider,
      { i18n },
      createElement(BrandPanel, {
        id: 'panel',
        labelledBy: 'tab',
        group,
        nativeSections,
        resources: [],
        controls: {
          ...query,
          availableModels: [],
          onFilterChange: noop,
          onSortBy: noop,
          onSortDir: noop,
          onSelectedModelsChange: noop,
          onClearFilters: noop,
        },
        selectedId: null,
        disableMutations,
        usageByProvider: usage,
        onView: noop,
        onEdit: noop,
        onDelete: noop,
        onToggleDisabled: noop,
        onCreate: noop,
        onEditGroup: noop,
        onToggleGroup: noop,
      })
    )
  );
}
