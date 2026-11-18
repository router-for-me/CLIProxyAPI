import { describe, expect, test } from 'bun:test';
import {
  assignPriorities,
  RoutingPriorityError,
  buildRoutingPlan,
  canStepMember,
  collectModelOptions,
  draftFromBoard,
  moveMember,
  resolveRoute,
  stepMember,
  tiersFromMembers,
} from '../src/features/routing/logic';
import {
  RoutingConflictError,
  buildRoutingBoard,
  buildRoutingConfigPatch,
  credentialWrites,
  readRoutingSettings,
} from '../src/services/api/routing';
import type { AuthFileItem } from '../src/types/authFile';
import type { RoutingBoard, RoutingModelCatalog } from '../src/types/routing';

const RAW = {
  routing: {
    strategy: 'weighted-round-robin',
    'session-affinity': true,
    retry: { 'request-retry': 3, 'max-retry-credentials': 0 },
  },
  'api-keys': {
    codex: [
      {
        name: 'opencode',
        'base-url': 'https://opencode.example/v1',
        priority: 20,
        auth_index: 'g-ignored',
        keys: [
          { 'api-key': 'sk-codex-aaaa', auth_index: 'a1' },
          { 'api-key': 'sk-codex-bbbb', priority: 10, weight: 3, auth_index: 'a2' },
        ],
      },
    ],
    claude: [
      {
        name: 'claude-1',
        'excluded-models': ['*'],
        keys: [{ 'api-key': 'sk-claude-cccc' }],
      },
    ],
    'openai-compatibility': [
      {
        name: 'openrouter',
        'base-url': 'https://openrouter.example/api/v1',
        priority: 10,
        models: [{ name: 'deepseek/deepseek-v4', alias: 'deepseek-v4' }],
        keys: [{ 'api-key': 'sk-or-1111' }, { 'api-key': 'sk-or-2222', weight: 2 }],
      },
    ],
  },
};

const FILES: AuthFileItem[] = [
  { name: 'main.json', type: 'codex', email: 'main@example.com', priority: 0 },
  { name: 'runtime', type: 'gemini', runtimeOnly: true, priority: 0 },
  {
    name: 'cool.json',
    type: 'codex',
    priority: 0,
    cooldownSnapshot: {
      receivedAtMs: 1_000,
      records: [
        {
          scope: 'model',
          modelKey: 'gpt-5',
          reason: 'quota',
          retryAt: '',
          remainingSeconds: 60,
        },
      ],
    },
  },
];

const board = (): RoutingBoard => buildRoutingBoard(RAW, FILES);

const catalog: RoutingModelCatalog = {
  channels: { codex: ['gpt-5', 'gpt-5-mini'] },
  credentials: { 'main.json': ['gpt-5'], 'cool.json': ['gpt-5'], runtime: ['gemini-3-pro'] },
};

describe('routing board reading', () => {
  test('reads settings with CPA defaults for absent values', () => {
    expect(readRoutingSettings(RAW)).toEqual({
      strategy: 'weighted-round-robin',
      sessionAffinity: true,
      requestRetry: 3,
      maxRetryCredentials: 0,
    });
    expect(readRoutingSettings({ routing: { strategy: 'bogus' } }).strategy).toBe('round-robin');
  });

  test('expands groups with key overrides, compat providers and credentials', () => {
    const members = board().members;
    const byKey = new Map(members.map((member) => [member.key, member]));
    expect(byKey.get('key:codex:0:0')?.priority).toBe(20);
    expect(byKey.get('key:codex:0:1')?.priority).toBe(10);
    expect(byKey.get('key:codex:0:1')?.weights[0].weight).toBe(3);
    expect(byKey.get('key:codex:0:0')?.modelSource).toEqual({ type: 'channel', channel: 'codex' });
    expect(byKey.get('key:claude:0:0')?.disabled).toBe(true);
    const compat = byKey.get('compat:0');
    expect(compat?.keyCount).toBe(2);
    expect(compat?.weights.map((slot) => slot.weight)).toEqual([null, 2]);
    expect(compat?.modelSource).toEqual({ type: 'list', models: ['deepseek-v4'] });
    expect(byKey.get('cred:main.json')?.name).toBe('main@example.com');
    expect(byKey.get('cred:runtime')?.locked).toBe(true);
    // Masked keys only: the raw key never reaches display fields.
    expect(JSON.stringify(members.map((member) => [member.name, member.detail]))).not.toContain(
      'sk-codex-aaaa'
    );
  });
});

describe('arranging tiers', () => {
  test('groups members by priority, highest first', () => {
    const tiers = tiersFromMembers(board().members);
    expect(tiers.map((tier) => tier.id)).toEqual(['p20', 'p10', 'p0']);
  });

  test('moves into a tier, into a gap, and refuses no-op moves', () => {
    const tiers = tiersFromMembers(board().members);
    const joined = moveMember(tiers, 'cred:main.json', { kind: 'tier', tierId: 'p20' });
    expect(joined[0].memberKeys).toContain('cred:main.json');
    const top = moveMember(tiers, 'cred:main.json', { kind: 'gap', index: 0 });
    expect(top[0].memberKeys).toEqual(['cred:main.json']);
    expect(top).toHaveLength(4);
    // The lone member of the top tier cannot move above itself.
    expect(moveMember(tiers, 'key:codex:0:0', { kind: 'gap', index: 0 })).toEqual(tiers);
    expect(canStepMember(tiers, 'key:codex:0:0', 'up')).toBe(false);
    expect(stepMember(tiers, 'key:codex:0:0', 'down')[0].memberKeys).toContain('key:codex:0:0');
  });

  test('keeps stored numbers that still fit and numbers the gaps', () => {
    const current = board();
    const tiers = moveMember(tiersFromMembers(current.members), 'cred:main.json', {
      kind: 'gap',
      index: 0,
    });
    const priorities = assignPriorities(tiers, current.members);
    expect(priorities['cred:main.json']).toBe(30);
    expect(priorities['key:codex:0:0']).toBe(20);
    expect(priorities['compat:0']).toBe(10);
    expect(priorities['cred:runtime']).toBe(0);
  });

  test('squeezes a new tier between neighbours and renumbers when there is no room', () => {
    const current = board();
    const between = moveMember(tiersFromMembers(current.members), 'cred:main.json', {
      kind: 'gap',
      index: 1,
    });
    expect(assignPriorities(between, current.members)['cred:main.json']).toBe(15);

    const tight = buildRoutingBoard(
      {
        'api-keys': {
          'openai-compatibility': [
            { name: 'a', priority: 2, keys: [] },
            { name: 'b', priority: 1, keys: [] },
          ],
        },
      },
      [{ name: 'c.json', type: 'codex', priority: 0 }]
    );
    const squeezed = moveMember(tiersFromMembers(tight.members), 'cred:c.json', {
      kind: 'gap',
      index: 1,
    });
    const values = assignPriorities(squeezed, tight.members);
    const order = squeezed.map((tier) => values[tier.memberKeys[0]]);
    expect(order).toEqual([...order].sort((left, right) => right - left));
    expect(new Set(order).size).toBe(order.length);
  });

  test('rejects inserting between adjacent locked priorities without changing the draft', () => {
    const current = buildRoutingBoard({}, [
      { name: 'a', priority: 1, runtimeOnly: true },
      { name: 'b', priority: 0, runtimeOnly: true },
      { name: 'c', priority: -10 },
    ]);
    const draft = draftFromBoard(current);
    const moved = moveMember(draft.tiers, 'cred:c', { kind: 'gap', index: 1 });
    expect(() => assignPriorities(moved, current.members)).toThrow(RoutingPriorityError);
    expect(() => buildRoutingPlan(current, { ...draft, tiers: moved })).toThrow(
      RoutingPriorityError
    );
    expect(buildRoutingPlan(current, draft).changes).toEqual([]);
  });

  test('renumbers editable tiers while preserving locked anchors', () => {
    const current = buildRoutingBoard({}, [
      { name: 'a', priority: 2 },
      { name: 'b', priority: 1, runtimeOnly: true },
      { name: 'c', priority: -10 },
    ]);
    const draft = draftFromBoard(current);
    draft.tiers = moveMember(draft.tiers, 'cred:c', { kind: 'gap', index: 1 });
    const values = assignPriorities(draft.tiers, current.members);
    expect(values['cred:b']).toBe(1);
    expect(values['cred:a']).toBeGreaterThan(values['cred:c']);
    expect(values['cred:c']).toBeGreaterThan(1);
    expect(buildRoutingPlan(current, draft).priorities['cred:b']).toBeUndefined();
  });

  test('plans only real changes and never moves locked credentials', () => {
    const current = board();
    const draft = draftFromBoard(current);
    expect(buildRoutingPlan(current, draft).changes).toEqual([]);

    draft.tiers = moveMember(draft.tiers, 'cred:main.json', { kind: 'gap', index: 0 });
    draft.weights['compat:0'] = [5, 2];
    draft.settings = { ...draft.settings, strategy: 'fill-first' };
    const plan = buildRoutingPlan(current, draft);
    expect(plan.priorities).toEqual({ 'cred:main.json': 30 });
    expect(plan.weights).toEqual({ 'compat:0': [5, 2] });
    expect(plan.settings).toEqual({ strategy: 'fill-first' });
    expect(plan.priorities['cred:runtime']).toBeUndefined();
  });
});

describe('writing a plan', () => {
  test('sends credentials individually and config changes as one patch', () => {
    const current = board();
    const draft = draftFromBoard(current);
    draft.tiers = moveMember(draft.tiers, 'key:codex:0:1', { kind: 'gap', index: 0 });
    draft.tiers = moveMember(draft.tiers, 'cred:main.json', { kind: 'tier', tierId: 'p10' });
    draft.weights['compat:0'] = [null, 4];
    draft.settings = { ...draft.settings, sessionAffinity: false };
    const plan = buildRoutingPlan(current, draft);

    expect(credentialWrites(current, plan)).toEqual([
      { key: 'cred:main.json', name: 'main.json', fields: { priority: 10 } },
    ]);

    // eslint-disable-next-line @typescript-eslint/no-explicit-any -- assertions walk the JSON body
    const patch = buildRoutingConfigPatch(RAW, current, plan) as Record<string, any>;
    expect(patch.routing).toEqual({ 'session-affinity': false });
    const codexGroup = patch['api-keys'].codex[0];
    expect(codexGroup.priority).toBe(20);
    expect(codexGroup.keys[1].priority).toBe(30);
    expect(codexGroup.keys[0].priority).toBeUndefined();
    expect(JSON.stringify(patch)).not.toContain('auth_index');
    const compatGroup = patch['api-keys']['openai-compatibility'][0];
    expect(compatGroup.keys.map((key: Record<string, unknown>) => key.weight)).toEqual([
      undefined,
      4,
    ]);
    // Untouched families are not sent; the source document is not mutated.
    expect(patch['api-keys'].claude).toBeUndefined();
    expect(RAW['api-keys'].codex[0].keys[1].priority).toBe(10);
  });

  test('refuses to write when a key moved since the board was read', () => {
    const current = board();
    const draft = draftFromBoard(current);
    draft.tiers = moveMember(draft.tiers, 'key:codex:0:1', { kind: 'gap', index: 0 });
    const plan = buildRoutingPlan(current, draft);
    const changed = structuredClone(RAW);
    changed['api-keys'].codex[0].keys.reverse();
    expect(() => buildRoutingConfigPatch(changed, current, plan)).toThrow(RoutingConflictError);
  });

  test('a settings-only plan touches no provider list', () => {
    const current = board();
    const draft = draftFromBoard(current);
    draft.settings = { ...draft.settings, requestRetry: 1, maxRetryCredentials: 2 };
    const patch = buildRoutingConfigPatch(RAW, current, buildRoutingPlan(current, draft));
    expect(patch).toEqual({
      routing: { retry: { 'request-retry': 1, 'max-retry-credentials': 2 } },
    });
  });
});

describe('route preview', () => {
  for (const forceModelPrefix of [false, true]) {
    test(`credential models are final callable ids (force prefix: ${forceModelPrefix})`, () => {
      const current = board();
      current.forceModelPrefix = forceModelPrefix;
      const credential = current.members.find((member) => member.key === 'cred:main.json')!;
      current.members = [{ ...credential, prefix: 'team' }];
      const models = forceModelPrefix ? ['team/gpt-5'] : ['gpt-5', 'team/gpt-5'];
      const liveCatalog: RoutingModelCatalog = {
        channels: {},
        credentials: { 'main.json': models },
      };
      expect(collectModelOptions(current, liveCatalog)).toEqual(models);
      const draft = draftFromBoard(current);
      for (const model of models) {
        const preview = resolveRoute(current, draft, liveCatalog, model, 0);
        expect(preview.tiers[0].members[0].state).toBe('serving');
      }
      const invalid = ['team/team/gpt-5', ...(forceModelPrefix ? ['gpt-5'] : [])];
      for (const model of invalid) {
        const preview = resolveRoute(current, draft, liveCatalog, model, 0);
        expect(preview.tiers[0].members[0].state).toBe('ineligible');
      }
    });

    for (const sourceType of ['list', 'channel'] as const) {
      test(`${sourceType} models still apply prefix policy (force prefix: ${forceModelPrefix})`, () => {
        const current = board();
        current.forceModelPrefix = forceModelPrefix;
        const catalog: RoutingModelCatalog = {
          channels: { codex: ['gpt-5'] },
          credentials: {},
        };
        current.members = [
          {
            ...current.members[0],
            prefix: 'team',
            modelSource:
              sourceType === 'list'
                ? { type: 'list', models: ['gpt-5'] }
                : { type: 'channel', channel: 'codex' },
          },
        ];
        const expected = forceModelPrefix ? ['team/gpt-5'] : ['gpt-5', 'team/gpt-5'];
        expect(collectModelOptions(current, catalog)).toEqual(expected);
        const draft = draftFromBoard(current);
        for (const model of expected) {
          expect(resolveRoute(current, draft, catalog, model, 0).hasActive).toBe(true);
        }
        expect(resolveRoute(current, draft, catalog, 'team/team/gpt-5', 0).hasActive).toBe(false);
        if (forceModelPrefix) {
          expect(resolveRoute(current, draft, catalog, 'gpt-5', 0).hasActive).toBe(false);
        }
      });
    }
  }

  test('lists callable models with prefixes', () => {
    const options = collectModelOptions(board(), catalog);
    expect(options).toContain('gpt-5');
    expect(options).toContain('deepseek-v4');
    expect(options).not.toContain('claude-sonnet');
  });

  test('the first tier with a serving member takes the request, weighted by key', () => {
    const current = board();
    const draft = draftFromBoard(current);
    const preview = resolveRoute(current, draft, catalog, 'deepseek-v4', 2_000);
    // p20 only has a codex key without that model: skipped.
    expect(preview.tiers[0].state).toBe('skipped');
    expect(preview.tiers[1].state).toBe('active');
    const compat = preview.tiers[1].members.find((member) => member.key === 'compat:0');
    const codexKey = preview.tiers[1].members.find((member) => member.key === 'key:codex:0:1');
    expect(compat?.state).toBe('serving');
    expect(compat?.share).toBe(100);
    expect(codexKey?.state).toBe('ineligible');
    expect(preview.tiers[2].state).toBe('skipped');
  });

  test('weighted round-robin splits a tier by key weights (absent counts as 1)', () => {
    const current = board();
    const draft = draftFromBoard(current);
    draft.tiers = moveMember(draft.tiers, 'key:codex:0:0', { kind: 'tier', tierId: 'p0' });
    const preview = resolveRoute(current, draft, catalog, null, 0);
    // p10: compat (1 + 2) and the codex key (3).
    expect(preview.tiers[0].members.map((member) => member.share)).toEqual([50, 50]);
    draft.settings = { ...draft.settings, strategy: 'round-robin' };
    const even = resolveRoute(current, draft, catalog, null, 0);
    // Round-robin counts keys: compat has two, the codex key one.
    const shares = Object.fromEntries(
      even.tiers[0].members.map((member) => [member.key, member.share])
    );
    expect(shares).toEqual({ 'compat:0': 66.7, 'key:codex:0:1': 33.3 });
  });

  test('model cooldowns and unknown model lists are not counted as serving', () => {
    const current = board();
    const draft = draftFromBoard(current);
    draft.tiers = moveMember(draft.tiers, 'cred:cool.json', { kind: 'gap', index: 0 });
    const preview = resolveRoute(current, draft, catalog, 'gpt-5', 2_000);
    expect(preview.tiers[0].state).toBe('cooling');
    expect(preview.tiers[0].members[0].state).toBe('cooling');
    expect(preview.tiers[1].state).toBe('active');

    const loading = resolveRoute(current, draft, { channels: {}, credentials: {} }, 'gpt-5', 0);
    expect(loading.tiers[0].members[0].state).toBe('unknown');
  });

  test('fill-first has no fixed split', () => {
    const current = board();
    const draft = draftFromBoard(current);
    draft.settings = { ...draft.settings, strategy: 'fill-first' };
    const preview = resolveRoute(current, draft, catalog, null, 0);
    expect(preview.tiers[0].state).toBe('active');
    expect(preview.tiers[0].members[0].share).toBeNull();
  });
});
