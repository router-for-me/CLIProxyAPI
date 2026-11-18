import { afterAll, afterEach, describe, expect, mock, spyOn, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { createElement } from 'react';
import { renderToStaticMarkup } from 'react-dom/server';
import i18n from '../src/i18n/index';
import { authFilesApi } from '../src/services/api/authFiles';
import { apiClient } from '../src/services/api/client';
import { buildRoutingBoard, routingApi } from '../src/services/api/routing';
import {
  buildRoutingPlan,
  draftFromBoard,
  moveMember,
  resolveRoute,
} from '../src/features/routing/logic';
import { TierLadder } from '../src/features/routing/components/TierLadder';
import { PolicyPanel } from '../src/features/routing/components/PolicyPanel';
import type { AuthFileItem } from '../src/types/authFile';

test('language changes do not trigger board reloads or reset the draft', () => {
  const source = readFileSync('src/features/routing/RoutingPage.tsx', 'utf8');
  const load = source.slice(
    source.indexOf('  const load = useCallback'),
    source.indexOf('  useEffect(() => {')
  );
  expect(load).toContain('[connected, fetchConfig, loadModels]');
  expect(load).not.toContain("t('common.unknown_error')");
  expect(source).toContain("message: error || t('common.unknown_error')");
});

const RAW = {
  routing: { strategy: 'round-robin' },
  'api-keys': {
    codex: [{ name: 'opencode', priority: 20, keys: [{ 'api-key': 'sk-codex-aaaa' }] }],
    claude: [
      { name: 'claude-off', keys: [{ 'api-key': 'sk-claude-bbbb', 'excluded-models': ['*'] }] },
    ],
  },
};
const FILES: AuthFileItem[] = [
  { name: 'main.json', type: 'codex', email: 'main@example.com', priority: 0 },
  { name: 'runtime', type: 'gemini', runtimeOnly: true },
];

/** One card's markup: from its key to the next card. */
const cardMarkup = (html: string, key: string): string => {
  const rest = html.slice(html.indexOf(`data-member-key="${key}"`));
  const next = rest.indexOf('data-member-key=', 1);
  return next < 0 ? rest : rest.slice(0, next);
};

const restores: Array<{ mockRestore: () => void }> = [];
afterEach(() => {
  restores.splice(0).forEach((spy) => spy.mockRestore());
});

// i18n is a process-wide singleton shared with other test files.
const initialLanguage = i18n.language;
afterAll(async () => {
  await i18n.changeLanguage(initialLanguage);
});

describe('routing board rendering', () => {
  test('renders tiers on a rail with drop gaps, states and accessible controls', async () => {
    await i18n.changeLanguage('zh-CN');
    const board = buildRoutingBoard(RAW, FILES);
    const draft = draftFromBoard(board);
    const html = renderToStaticMarkup(
      createElement(TierLadder, {
        members: new Map(board.members.map((member) => [member.key, member])),
        draft,
        preview: resolveRoute(board, draft, { channels: {}, credentials: {} }, null, 0),
        priorities: {
          'key:codex:0:0': 20,
          'key:claude:0:0': 0,
          'cred:main.json': 0,
          'cred:runtime': 0,
        },
        model: null,
        nowMs: 0,
        theme: 'light',
        entryControl: null,
        onMove: () => {},
        onStep: () => {},
        onEditWeights: () => {},
      })
    );
    expect(html).toContain('请求进入');
    expect(html).toContain('首选');
    expect(html).toContain('备用 1');
    expect(html).toContain('正在承接请求');
    expect(html).toContain('整层不可用时转入下一层');
    expect(html).toContain('main@example.com');
    expect(html).toContain('aria-haspopup="menu"');
    // Runtime-only credentials show a lock instead of a drag handle and are not draggable.
    expect(html).toMatch(/draggable="false"[^>]*data-member-key="cred:runtime"/);
    expect(html).not.toContain('sk-codex-aaaa');
  });

  test('API keys show their configured disabled state; only enabled ones note missing cooldowns', async () => {
    await i18n.changeLanguage('zh-CN');
    const board = buildRoutingBoard(RAW, FILES);
    const draft = draftFromBoard(board);
    const html = renderToStaticMarkup(
      createElement(TierLadder, {
        members: new Map(board.members.map((member) => [member.key, member])),
        draft,
        preview: resolveRoute(board, draft, { channels: {}, credentials: {} }, null, 0),
        priorities: {},
        model: null,
        nowMs: 0,
        theme: 'light',
        entryControl: null,
        onMove: () => {},
        onStep: () => {},
        onEditWeights: () => {},
      })
    );
    // CSS Modules are not compiled under bun:test; the route state drives the dimmed style.
    const cardState = (key: string) =>
      html.match(new RegExp(`data-member-key="${key}" data-state="([^"]*)"`))?.[1] ?? '';

    // Disabled in configuration: reliable, so marked and dimmed, with no cooldown note.
    const disabledKey = cardMarkup(html, 'key:claude:0:0');
    expect(disabledKey).toContain('已停用');
    expect(disabledKey).not.toContain('无冷却信息');
    expect(cardState('key:claude:0:0')).toBe('disabled');

    // Enabled API key: normal; the missing-runtime-state note lives in its tooltip, not inline.
    const enabledKey = cardMarkup(html, 'key:codex:0:0');
    const cardTitle = (key: string) =>
      html.match(new RegExp(`title="([^"]*)" data-member-key="${key}"`))?.[1] ?? '';
    expect(cardTitle('key:codex:0:0')).toContain('无冷却信息');
    expect(enabledKey).not.toContain('无冷却信息');
    expect(cardTitle('key:claude:0:0')).not.toContain('无冷却信息');
    expect(enabledKey).not.toContain('已停用');
    expect(cardState('key:codex:0:0')).toBe('serving');

    // Credentials report real runtime state and never carry the note.
    expect(cardMarkup(html, 'cred:main.json')).not.toContain('无冷却信息');
    const card = readFileSync('src/features/routing/components/MemberCard.tsx', 'utf8');
    expect(card).toContain("const muted = state === 'disabled' || state === 'ineligible';");
  });

  test('the strategy picker is a keyboard radio group', async () => {
    await i18n.changeLanguage('en');
    const html = renderToStaticMarkup(
      createElement(PolicyPanel, {
        settings: {
          strategy: 'weighted-round-robin',
          sessionAffinity: false,
          requestRetry: 3,
          maxRetryCredentials: 0,
        },
        onChange: () => {},
      })
    );
    expect(html).toContain('role="radiogroup"');
    expect(html).toMatch(/role="radio" aria-checked="true" tabindex="0"[^>]*>.*Weighted/);
    expect(html).toContain('Requests are split across the tier by weight.');
  });

  test('policy explanations sit in InfoTips after their labels, controls stay described', async () => {
    await i18n.changeLanguage('en');
    const html = renderToStaticMarkup(
      createElement(PolicyPanel, {
        settings: {
          strategy: 'round-robin',
          sessionAffinity: true,
          requestRetry: 1,
          maxRetryCredentials: 0,
        },
        onChange: () => {},
      })
    );
    const tips = html.match(/<button\b[^>]*data-info-tip=""[^>]*>/g) ?? [];
    const attr = (tag: string, name: string) =>
      tag.match(new RegExp(`\\b${name}="([^"]*)"`))?.[1] ?? '';
    expect(tips.map((tip) => attr(tip, 'aria-label'))).toEqual([
      'More information about Gateway policy',
      'More information about Within a tier',
      'More information about Session affinity',
      'More information about Retry rounds',
      'More information about Credentials per round',
    ]);

    // The strategy tip compares all three strategies, not just the selected one.
    const strategyTip = html.slice(html.indexOf(tips[1]), html.indexOf(tips[2]));
    for (const key of ['round_robin', 'weighted_round_robin', 'fill_first']) {
      expect(strategyTip).toContain(i18n.t(`routing.strategy_${key}_desc`));
    }

    // Every explanation lives only in a hidden copy, never as a visible line.
    for (const key of ['affinity_desc', 'request_retry_desc', 'max_retry_credentials_desc']) {
      const text = i18n.t(`routing.${key}`);
      expect(html.split(text).length - 1).toBe(1);
      expect(html).toMatch(new RegExp(`<span id="[^"]+" hidden="">${text}</span>`));
    }

    // Number inputs: named by a real <label for>, described by their explanation.
    for (const [label, desc] of [
      ['request_retry', 'request_retry_desc'],
      ['max_retry_credentials', 'max_retry_credentials_desc'],
    ]) {
      const labelTag = html.match(
        new RegExp(`<label for="([^"]+)"[^>]*>${i18n.t(`routing.${label}`)}</label>`)
      );
      expect(labelTag).not.toBeNull();
      const input = html.match(new RegExp(`<input[^>]*id="${labelTag![1]}"[^>]*>`))?.[0] ?? '';
      const descId = attr(input, 'aria-describedby');
      expect(html).toContain(`<span id="${descId}" hidden="">${i18n.t(`routing.${desc}`)}</span>`);
    }
    // The affinity switch is described by the same hidden copy its InfoTip uses.
    const affinityTip = tips[2];
    const affinitySwitch = html.match(/<input type="checkbox"[^>]*>/)?.[0] ?? '';
    expect(attr(affinitySwitch, 'aria-describedby')).toBe(attr(affinityTip, 'aria-describedby'));
  });

  test('the preview caveat is an InfoTip on the entry label, not a footnote', async () => {
    await i18n.changeLanguage('en');
    const board = buildRoutingBoard(RAW, FILES);
    const draft = draftFromBoard(board);
    const html = renderToStaticMarkup(
      createElement(TierLadder, {
        members: new Map(board.members.map((member) => [member.key, member])),
        draft,
        preview: resolveRoute(board, draft, { channels: {}, credentials: {} }, null, 0),
        priorities: {},
        model: null,
        nowMs: 0,
        theme: 'light',
        entryControl: null,
        onMove: () => {},
        onStep: () => {},
        onEditWeights: () => {},
      })
    );
    expect(html).toContain('aria-label="More information about Request in"');
    expect(html).toContain(`hidden="">${i18n.t('routing.footnote')}</span>`);
    const page = readFileSync('src/features/routing/RoutingPage.tsx', 'utf8');
    expect(page).not.toContain("t('routing.footnote')");
  });

  test('styles respect reduced motion and the route is registered behind navigation', () => {
    const page = readFileSync('src/features/routing/RoutingPage.module.scss', 'utf8');
    const card = readFileSync('src/features/routing/components/MemberCard.module.scss', 'utf8');
    const bar = readFileSync('src/features/routing/components/RoutingSaveBar.module.scss', 'utf8');
    expect(page).toContain('@media (prefers-reduced-motion: reduce)');
    expect(card).toContain('@media (prefers-reduced-motion: reduce)');
    expect(bar).toContain('@media (prefers-reduced-motion: reduce)');
    expect(readFileSync('src/router/MainRoutes.tsx', 'utf8')).toContain(
      "{ path: '/routing', element: <RoutingPage /> }"
    );
    expect(readFileSync('src/components/layout/MainLayout.tsx', 'utf8')).toContain(
      "labelKey: 'nav.routing'"
    );
  });
});

describe('routing save bar', () => {
  test('floats over the policy column, outside the transition layer', () => {
    const bar = readFileSync('src/features/routing/components/RoutingSaveBar.tsx', 'utf8');
    const policy = readFileSync('src/features/routing/components/PolicyPanel.tsx', 'utf8');
    const page = readFileSync('src/features/routing/RoutingPage.tsx', 'utf8');
    // Portalled: sticky/fixed inside the page-transition layer does not track the viewport.
    expect(bar).toContain('createPortal(');
    // The bar measures the column the policy panel marks.
    expect(bar).toContain("POLICY_COLUMN_ATTR = 'data-routing-policy-column'");
    expect(policy).toContain('data-routing-policy-column=""');
    // A page sliding out must not leave its bar behind.
    expect(page).toContain('visible={isCurrentLayer && dirty}');
  });
});

describe('applying a routing plan', () => {
  const planFor = () => {
    const board = buildRoutingBoard(RAW, FILES);
    const draft = draftFromBoard(board);
    draft.tiers = moveMember(draft.tiers, 'cred:main.json', { kind: 'gap', index: 0 });
    // Joining the runtime-only credential's tier takes its fixed priority.
    draft.tiers = moveMember(draft.tiers, 'key:codex:0:0', { kind: 'tier', tierId: 'p0' });
    draft.settings = { ...draft.settings, sessionAffinity: true };
    return { board, plan: buildRoutingPlan(board, draft) };
  };

  test('writes credentials first, then the configuration once against a fresh read', async () => {
    const calls: string[] = [];
    const patchFields = mock(async (name: string) => {
      calls.push(`fields:${name}`);
      return {};
    });
    restores.push(spyOn(authFilesApi, 'patchFields').mockImplementation(patchFields));
    restores.push(
      spyOn(apiClient, 'get').mockImplementation((async (url: string) => {
        calls.push(`get:${url}`);
        return RAW;
      }) as typeof apiClient.get)
    );
    const patch = mock(async (url: string, body: unknown) => {
      calls.push(`patch:${url}`);
      return body;
    });
    restores.push(spyOn(apiClient, 'patch').mockImplementation(patch as typeof apiClient.patch));

    const { board, plan } = planFor();
    const result = await routingApi.applyPlan(board, plan);

    expect(calls).toEqual(['fields:main.json', 'get:/config', 'patch:/config']);
    expect(result.failed).toBeNull();
    expect(result.applied).toEqual(['cred:main.json', 'key:codex:0:0', 'settings']);
    const body = patch.mock.calls[0][1] as Record<string, any>; // eslint-disable-line @typescript-eslint/no-explicit-any
    expect(body.routing).toEqual({ 'session-affinity': true });
    expect(plan.priorities).toEqual({ 'cred:main.json': 10, 'key:codex:0:0': 0 });
    expect(body['api-keys'].codex[0].keys[0].priority).toBe(0);
  });

  test('stops at the first failed credential and reports what was not attempted', async () => {
    restores.push(
      spyOn(authFilesApi, 'patchFields').mockImplementation(async () => {
        throw new Error('boom');
      })
    );
    const patch = spyOn(apiClient, 'patch');
    restores.push(patch);

    const { board, plan } = planFor();
    const result = await routingApi.applyPlan(board, plan);

    expect(result.failed?.key).toBe('cred:main.json');
    expect(result.applied).toEqual([]);
    expect(result.notAttempted).toEqual(['key:codex:0:0', 'settings']);
    expect(patch).not.toHaveBeenCalled();
  });
});
