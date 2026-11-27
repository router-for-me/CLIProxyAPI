/**
 * Routing board arithmetic, free of React so it can be reasoned about and tested on its own.
 *
 * The operator arranges tiers; priorities are derived here, changing as few stored numbers as
 * the new order allows. Previews mirror CPA's scheduler (highest available priority first, the
 * strategy spreads load inside that tier) but cannot see runtime state CPA does not expose, such
 * as API-key cooldowns, session pinning or WebSocket preference.
 */

import { isModelExcluded } from '@/components/excludedModels/excludedModelRules';
import type {
  RouteMember,
  RouteMemberState,
  RoutePreview,
  RouteTier,
  RouteTierState,
  RoutingBoard,
  RoutingChange,
  RoutingDraft,
  RoutingDropTarget,
  RoutingMember,
  RoutingModelCatalog,
  RoutingPlan,
  RoutingSettings,
  RoutingStrategy,
  RoutingTier,
} from '@/types/routing';

/** Gap between newly numbered tiers, leaving room for later insertions. */
export const TIER_STEP = 10;
export const ROUTING_WEIGHT_MAX = 1_000_000;

/* ---------------------------------------------------------------------------------------------
 * Arranging
 * ------------------------------------------------------------------------------------------- */

export function tiersFromMembers(members: readonly RoutingMember[]): RoutingTier[] {
  const byPriority = new Map<number, RoutingMember[]>();
  for (const member of members) {
    byPriority.set(member.priority, [...(byPriority.get(member.priority) ?? []), member]);
  }
  return [...byPriority.entries()]
    .sort(([left], [right]) => right - left)
    .map(([priority, peers]) => ({
      id: `p${priority}`,
      memberKeys: peers
        .slice()
        .sort(
          (left, right) =>
            Number(left.disabled) - Number(right.disabled) || left.name.localeCompare(right.name)
        )
        .map((member) => member.key),
    }));
}

export function draftFromBoard(board: RoutingBoard): RoutingDraft {
  const weights: RoutingDraft['weights'] = {};
  for (const member of board.members) {
    weights[member.key] = member.weights.map((slot) => slot.weight);
  }
  return { tiers: tiersFromMembers(board.members), weights, settings: { ...board.settings } };
}

let tierSequence = 0;
const newTierId = (): string => {
  tierSequence += 1;
  return `new-${tierSequence}`;
};

const withoutMember = (tiers: readonly RoutingTier[], key: string): RoutingTier[] =>
  tiers.map((tier) => ({
    ...tier,
    memberKeys: tier.memberKeys.filter((memberKey) => memberKey !== key),
  }));

const withoutEmptyTiers = (tiers: readonly RoutingTier[]): RoutingTier[] =>
  tiers.filter((tier) => tier.memberKeys.length > 0);

export const tierIndexOf = (tiers: readonly RoutingTier[], key: string): number =>
  tiers.findIndex((tier) => tier.memberKeys.includes(key));

/**
 * Moves a member into a tier or a gap. A gap on either side of the member's own single-member
 * tier would recreate that tier, so it is no move at all.
 */
export function moveMember(
  tiers: readonly RoutingTier[],
  key: string,
  target: RoutingDropTarget
): RoutingTier[] {
  const from = tierIndexOf(tiers, key);
  if (from < 0) return [...tiers];
  if (target.kind === 'tier') {
    if (tiers[from].id === target.tierId || !tiers.some((tier) => tier.id === target.tierId)) {
      return [...tiers];
    }
    const moved = withoutMember(tiers, key).map((tier) =>
      tier.id === target.tierId ? { ...tier, memberKeys: [...tier.memberKeys, key] } : tier
    );
    return withoutEmptyTiers(moved);
  }
  const isAlone = tiers[from].memberKeys.length === 1;
  if (isAlone && (target.index === from || target.index === from + 1)) return [...tiers];
  const moved = withoutMember(tiers, key);
  const index = Math.max(0, Math.min(target.index, moved.length));
  moved.splice(index, 0, { id: newTierId(), memberKeys: [key] });
  return withoutEmptyTiers(moved);
}

/**
 * Keyboard and menu moves. Stepping up joins the tier above; a member already at the top leaves
 * its peers for a tier of its own above them, the only way further up. Down mirrors it.
 */
export function stepMember(
  tiers: readonly RoutingTier[],
  key: string,
  direction: 'up' | 'down'
): RoutingTier[] {
  const from = tierIndexOf(tiers, key);
  if (from < 0) return [...tiers];
  if (direction === 'up') {
    return from > 0
      ? moveMember(tiers, key, { kind: 'tier', tierId: tiers[from - 1].id })
      : moveMember(tiers, key, { kind: 'gap', index: 0 });
  }
  return from < tiers.length - 1
    ? moveMember(tiers, key, { kind: 'tier', tierId: tiers[from + 1].id })
    : moveMember(tiers, key, { kind: 'gap', index: tiers.length });
}

/** Can this step change anything? A lone member at either end has nowhere further to go. */
export function canStepMember(
  tiers: readonly RoutingTier[],
  key: string,
  direction: 'up' | 'down'
): boolean {
  const from = tierIndexOf(tiers, key);
  if (from < 0) return false;
  const isAlone = tiers[from].memberKeys.length === 1;
  if (direction === 'up') return from > 0 || !isAlone;
  return from < tiers.length - 1 || !isAlone;
}

export class RoutingPriorityError extends Error {
  constructor() {
    super('Locked priorities leave no room for this arrangement.');
    this.name = 'RoutingPriorityError';
  }
}

/** The most common stored priority of a tier; ties go to the higher number. */
function tierCandidate(memberPriorities: readonly number[]): number {
  const counts = new Map<number, number>();
  for (const priority of memberPriorities) counts.set(priority, (counts.get(priority) ?? 0) + 1);
  let best = memberPriorities[0] ?? 0;
  let bestCount = 0;
  for (const [priority, count] of counts) {
    if (count > bestCount || (count === bestCount && priority > best)) {
      best = priority;
      bestCount = count;
    }
  }
  return best;
}

/**
 * Derives a strictly decreasing priority per tier. Tiers whose stored number still fits the new
 * order keep it (chosen to keep as many members unchanged as possible); the rest are numbered
 * into the gaps. A tier with a locked member always keeps that member's priority.
 */
export function assignPriorities(
  tiers: readonly RoutingTier[],
  members: readonly RoutingMember[]
): Record<string, number> {
  const byKey = new Map(members.map((member) => [member.key, member]));
  const candidates = tiers.map((tier) => {
    const tierMembers = tier.memberKeys.flatMap((key) => byKey.get(key) ?? []);
    const locked = tierMembers.find((member) => member.locked);
    const value = locked
      ? locked.priority
      : tierCandidate(tierMembers.map((member) => member.priority));
    const score =
      tierMembers.filter((member) => member.priority === value).length + (locked ? 1_000_000 : 0);
    return { value, score };
  });

  // Heaviest strictly decreasing subsequence: the tiers that keep their number.
  const n = tiers.length;
  const best = candidates.map((candidate) => candidate.score);
  const previous = new Array<number>(n).fill(-1);
  for (let i = 0; i < n; i += 1) {
    for (let j = 0; j < i; j += 1) {
      if (candidates[j].value > candidates[i].value && best[j] + candidates[i].score > best[i]) {
        best[i] = best[j] + candidates[i].score;
        previous[i] = j;
      }
    }
  }
  const kept = new Set<number>();
  if (n > 0) {
    let end = 0;
    for (let i = 1; i < n; i += 1) if (best[i] > best[end]) end = i;
    for (let i = end; i >= 0; i = previous[i]) kept.add(i);
  }

  const values = new Array<number>(n);
  kept.forEach((index) => {
    values[index] = candidates[index].value;
  });

  let fits = true;
  let i = 0;
  while (i < n) {
    if (kept.has(i)) {
      i += 1;
      continue;
    }
    let end = i;
    while (end < n && !kept.has(end)) end += 1;
    const count = end - i;
    const upper = i > 0 ? values[i - 1] : null;
    const lower = end < n ? values[end] : null;
    for (let j = 0; j < count; j += 1) {
      if (upper === null && lower === null) values[i + j] = (count - 1 - j) * TIER_STEP;
      else if (upper === null) values[i + j] = (lower as number) + TIER_STEP * (count - j);
      else if (lower === null) values[i + j] = upper - TIER_STEP * (j + 1);
      else {
        const step = Math.floor((upper - lower) / (count + 1));
        if (step < 1) fits = false;
        values[i + j] = upper - Math.max(1, step) * (j + 1);
      }
    }
    i = end;
  }

  const anchors = tiers.flatMap((tier, index) => {
    const locked = tier.memberKeys.flatMap((key) => {
      const member = byKey.get(key);
      return member?.locked ? [member.priority] : [];
    });
    if (new Set(locked).size > 1) throw new RoutingPriorityError();
    return locked.length ? [{ index, value: locked[0] }] : [];
  });
  if (!fits || anchors.some((anchor) => values[anchor.index] !== anchor.value)) {
    // Renumber only editable tiers around fixed runtime-credential anchors.
    for (let segment = 0; segment <= anchors.length; segment += 1) {
      const upper = anchors[segment - 1];
      const lower = anchors[segment];
      const start = upper ? upper.index + 1 : 0;
      const end = lower ? lower.index : n;
      const count = end - start;
      const step =
        upper && lower ? Math.floor((upper.value - lower.value) / (count + 1)) : TIER_STEP;
      if (step < 1) throw new RoutingPriorityError();
      for (let offset = 0; offset < count; offset += 1) {
        values[start + offset] = upper
          ? upper.value - step * (offset + 1)
          : (lower?.value ?? -TIER_STEP) + step * (count - offset);
      }
      if (lower) values[lower.index] = lower.value;
    }
  }

  const result: Record<string, number> = {};
  tiers.forEach((tier, index) => {
    for (const key of tier.memberKeys) result[key] = values[index];
  });
  return result;
}

/* ---------------------------------------------------------------------------------------------
 * Planning and writing
 * ------------------------------------------------------------------------------------------- */

const SETTING_FIELDS: readonly (keyof RoutingSettings)[] = [
  'strategy',
  'sessionAffinity',
  'requestRetry',
  'maxRetryCredentials',
];

export function buildRoutingPlan(board: RoutingBoard, draft: RoutingDraft): RoutingPlan {
  const assigned = assignPriorities(draft.tiers, board.members);
  const changes: RoutingChange[] = [];
  const priorities: RoutingPlan['priorities'] = {};
  const weights: RoutingPlan['weights'] = {};
  for (const member of board.members) {
    if (member.locked) continue;
    const next = assigned[member.key];
    if (next !== undefined && next !== member.priority) {
      priorities[member.key] = next;
      changes.push({ kind: 'priority', memberKey: member.key, from: member.priority, to: next });
    }
    const draftWeights = draft.weights[member.key] ?? [];
    let weightChanged = false;
    member.weights.forEach((slot, index) => {
      const to = draftWeights[index] ?? null;
      if (to !== slot.weight) {
        weightChanged = true;
        changes.push({
          kind: 'weight',
          memberKey: member.key,
          index,
          from: slot.weight,
          to,
        });
      }
    });
    if (weightChanged) {
      weights[member.key] = member.weights.map((_, index) => draftWeights[index] ?? null);
    }
  }
  const settings: Partial<RoutingSettings> = {};
  for (const field of SETTING_FIELDS) {
    if (draft.settings[field] !== board.settings[field]) {
      Object.assign(settings, { [field]: draft.settings[field] });
      changes.push({
        kind: 'setting',
        field,
        from: board.settings[field],
        to: draft.settings[field],
      });
    }
  }
  return { changes, priorities, weights, settings };
}

/* ---------------------------------------------------------------------------------------------
 * Models and preview
 * ------------------------------------------------------------------------------------------- */

/** Model ids, or null while unknown. Credential catalogs already contain final callable ids. */
export function memberModels(member: RoutingMember, catalog: RoutingModelCatalog): string[] | null {
  const source = member.modelSource;
  let models: string[] | null;
  if (source.type === 'list') models = source.models;
  else if (source.type === 'none') models = [];
  else if (source.type === 'channel') models = catalog.channels[source.channel] ?? null;
  else models = catalog.credentials[source.name] ?? null;
  if (!models) return null;
  return member.excludedModels.length
    ? models.filter((model) => !isModelExcluded(member.excludedModels, model))
    : models;
}

/**
 * Whether a request for `model` may land on this member. A prefixed member also answers the
 * prefixed name; with `force-model-prefix`, it answers only that.
 */
export function memberServesModel(
  member: RoutingMember,
  models: readonly string[],
  model: string,
  forceModelPrefix: boolean
): boolean {
  const requested = model.trim().toLowerCase();
  const known = (id: string) => models.some((entry) => entry.toLowerCase() === id);
  // The credential endpoint returns registry ids with prefix policy already applied.
  if (member.modelSource.type === 'credential') return known(requested);
  const prefix = member.prefix.toLowerCase();
  if (prefix) {
    if (requested.startsWith(`${prefix}/`) && known(requested.slice(prefix.length + 1))) {
      return true;
    }
    if (forceModelPrefix && requested !== prefix) return false;
  }
  return known(requested);
}

export function collectModelOptions(board: RoutingBoard, catalog: RoutingModelCatalog): string[] {
  const options = new Map<string, string>();
  const add = (id: string) => {
    if (!options.has(id.toLowerCase())) options.set(id.toLowerCase(), id);
  };
  for (const member of board.members) {
    if (member.disabled) continue;
    const models = memberModels(member, catalog);
    if (!models) continue;
    for (const model of models) {
      if (member.modelSource.type === 'credential') {
        add(model);
        continue;
      }
      if (!member.prefix || !board.forceModelPrefix) add(model);
      if (member.prefix) add(`${member.prefix}/${model}`);
    }
  }
  return [...options.values()].sort((left, right) => left.localeCompare(right));
}

const baseModelKey = (value: string): string =>
  value
    .trim()
    .toLowerCase()
    .replace(/\(.*\)$/, '');

/** Seconds until the member serves again, counting only cooldowns that block this request. */
export function activeCooldownSeconds(
  member: RoutingMember,
  model: string | null,
  nowMs: number
): number {
  const snapshot = member.cooldowns;
  if (!snapshot?.records) return 0;
  const elapsed = Math.max(0, nowMs - snapshot.receivedAtMs) / 1000;
  let requested: string | null = null;
  if (model) {
    const lower = model.trim().toLowerCase();
    const prefix = member.prefix.toLowerCase();
    requested = baseModelKey(
      prefix && lower.startsWith(`${prefix}/`) ? lower.slice(prefix.length + 1) : lower
    );
  }
  let longest = 0;
  for (const record of snapshot.records) {
    const remaining = Math.ceil(record.remainingSeconds - elapsed);
    if (remaining <= 0) continue;
    const blocks =
      record.scope === 'credential' ||
      (requested !== null && baseModelKey(record.modelKey ?? '') === requested);
    if (blocks) longest = Math.max(longest, remaining);
  }
  return longest;
}

const memberUnits = (
  member: RoutingMember,
  weights: readonly (number | null)[],
  strategy: RoutingStrategy
): number => {
  if (strategy !== 'weighted-round-robin') return member.keyCount;
  if (member.weights.length === 0) return member.keyCount;
  return member.weights.reduce((sum, _, index) => sum + Math.max(0, weights[index] ?? 1), 0);
};

/**
 * Where a request goes under the draft: the first tier with a member able to serve takes it,
 * tiers above it are skipped (no such model, or cooling down), tiers below wait.
 */
export function resolveRoute(
  board: RoutingBoard,
  draft: RoutingDraft,
  catalog: RoutingModelCatalog,
  model: string | null,
  nowMs: number
): RoutePreview {
  const byKey = new Map(board.members.map((member) => [member.key, member]));
  const strategy = draft.settings.strategy;
  let hasActive = false;
  const tiers: RouteTier[] = draft.tiers.map((tier) => {
    const units = new Map<string, number>();
    const members: RouteMember[] = tier.memberKeys.flatMap((key) => {
      const member = byKey.get(key);
      if (!member) return [];
      let state: RouteMemberState;
      const models = model ? memberModels(member, catalog) : [];
      if (member.disabled) state = 'disabled';
      else if (model && models === null) state = 'unknown';
      else if (model && !memberServesModel(member, models ?? [], model, board.forceModelPrefix)) {
        state = 'ineligible';
      } else if (activeCooldownSeconds(member, model, nowMs) > 0) state = 'cooling';
      else if (member.unavailable && !member.cooldowns?.records) state = 'blocked';
      else {
        const value = memberUnits(member, draft.weights[key] ?? [], strategy);
        if (value > 0) {
          units.set(key, value);
          state = 'serving';
        } else state = 'excluded';
      }
      return [{ key, state, share: null }];
    });

    const total = [...units.values()].reduce((sum, value) => sum + value, 0);
    const has = (state: RouteMemberState) => members.some((member) => member.state === state);
    const candidates = members.filter(
      (member) => member.state !== 'ineligible' && member.state !== 'disabled'
    );
    let state: RouteTierState;
    if (!hasActive && total > 0) {
      state = 'active';
      hasActive = true;
      if (strategy !== 'fill-first') {
        for (const member of members) {
          const value = units.get(member.key);
          if (value !== undefined) member.share = Math.round((value / total) * 1000) / 10;
        }
      }
    } else if (candidates.length === 0) state = 'skipped';
    else if (hasActive) state = 'standby';
    else if (has('cooling') || has('blocked')) state = 'cooling';
    else if (has('unknown')) state = 'unknown';
    else state = 'skipped';
    return { tierId: tier.id, state, members };
  });
  return { tiers, hasActive };
}

/** Channels whose built-in model lists the board needs. */
export function neededChannels(board: RoutingBoard): string[] {
  const channels = new Set<string>();
  for (const member of board.members) {
    if (member.modelSource.type === 'channel') channels.add(member.modelSource.channel);
  }
  return [...channels].sort();
}

/** Credentials whose live model lists the board needs (disabled ones never serve). */
export function neededCredentialModels(board: RoutingBoard): string[] {
  return board.members.flatMap((member) =>
    member.modelSource.type === 'credential' && !member.disabled ? [member.modelSource.name] : []
  );
}

export function clampWeight(value: number): number {
  if (!Number.isFinite(value)) return 1;
  return Math.min(ROUTING_WEIGHT_MAX, Math.max(0, Math.round(value)));
}
