/**
 * Routing board view model.
 *
 * CPA schedules in strict priority tiers: the highest priority with an available credential
 * serves a request, and `routing.strategy` spreads requests only among that tier. The board is
 * that order made explicit for every config API key, OpenAI-compatible provider and credential.
 */

import type { AuthFileCooldownSnapshot } from './authFile';
import type { RoutingStrategy } from './visualConfig';

export type { RoutingStrategy };

export const ROUTING_STRATEGIES: readonly RoutingStrategy[] = [
  'round-robin',
  'weighted-round-robin',
  'fill-first',
];

/** v8 `api-keys.<family>` lists whose keys schedule one by one. */
export const ROUTING_KEY_FAMILIES = [
  'gemini',
  'interactions',
  'codex',
  'meta',
  'xai',
  'claude',
  'vertex',
] as const;

export type RoutingKeyFamily = (typeof ROUTING_KEY_FAMILIES)[number];

export const ROUTING_COMPAT_FAMILY = 'openai-compatibility';

export type RoutingMemberKind = 'apiKey' | 'compat' | 'credential';

export type RoutingMemberLocation =
  | {
      kind: 'apiKey';
      family: RoutingKeyFamily;
      groupIndex: number;
      keyIndex: number;
      /** Identity check before writing: positions are only valid for the document they came from. */
      apiKey: string;
    }
  | { kind: 'compat'; groupIndex: number; name: string }
  | { kind: 'credential'; name: string };

/** Where a member's callable models come from. */
export type RoutingModelSource =
  /** Configured model list (alias, or name), already final. */
  | { type: 'list'; models: string[] }
  /** No configured list: CPA registers the channel's built-in models. */
  | { type: 'channel'; channel: string }
  /** Credentials: the registry's live list from `/credentials/models`. */
  | { type: 'credential'; name: string }
  /** Nothing is registered (for example an OpenAI-compatible provider without models). */
  | { type: 'none' };

export interface RoutingWeightSlot {
  /** Position of the key inside its member. */
  index: number;
  /** Absent weight: CPA runs the key as 1. */
  weight: number | null;
  /** Masked key or credential label, for display only. */
  label: string;
}

export interface RoutingMember {
  key: string;
  kind: RoutingMemberKind;
  /** Brand or credential type, for icons and labels. */
  family: string;
  name: string;
  detail: string;
  priority: number;
  weights: RoutingWeightSlot[];
  /** Keys the member schedules; a keyless OpenAI-compatible provider still schedules one. */
  keyCount: number;
  disabled: boolean;
  /** Runtime-only credentials have no file to store a priority in. */
  locked: boolean;
  prefix: string;
  excludedModels: string[];
  modelSource: RoutingModelSource;
  /** Credentials only. */
  unavailable: boolean;
  cooldowns: AuthFileCooldownSnapshot | null;
  location: RoutingMemberLocation;
}

export interface RoutingSettings {
  strategy: RoutingStrategy;
  sessionAffinity: boolean;
  requestRetry: number;
  /** 0 tries every available credential. */
  maxRetryCredentials: number;
}

export interface RoutingBoard {
  members: RoutingMember[];
  settings: RoutingSettings;
  forceModelPrefix: boolean;
}

export interface RoutingTier {
  /** Stable while members move, so a tier keeps its identity. */
  id: string;
  memberKeys: string[];
}

export interface RoutingDraft {
  /** Highest priority first. */
  tiers: RoutingTier[];
  /** Per member key, one entry per weight slot. */
  weights: Record<string, (number | null)[]>;
  settings: RoutingSettings;
}

/** A dragged member lands in a tier as a peer, or in the gap before tier `index`. */
export type RoutingDropTarget = { kind: 'tier'; tierId: string } | { kind: 'gap'; index: number };

/** Known model lists, filled in as they load. */
export interface RoutingModelCatalog {
  channels: Record<string, string[] | null>;
  credentials: Record<string, string[] | null>;
}

export type RoutingChange =
  | { kind: 'priority'; memberKey: string; from: number; to: number }
  | { kind: 'weight'; memberKey: string; index: number; from: number | null; to: number | null }
  | {
      kind: 'setting';
      field: keyof RoutingSettings;
      from: RoutingSettings[keyof RoutingSettings];
      to: RoutingSettings[keyof RoutingSettings];
    };

export interface RoutingPlan {
  changes: RoutingChange[];
  priorities: Record<string, number>;
  weights: Record<string, (number | null)[]>;
  settings: Partial<RoutingSettings>;
}

export type RouteMemberState =
  'serving' | 'excluded' | 'cooling' | 'blocked' | 'unknown' | 'ineligible' | 'disabled';

export type RouteTierState = 'active' | 'standby' | 'skipped' | 'cooling' | 'unknown';

export interface RouteMember {
  key: string;
  state: RouteMemberState;
  /** Share of the tier's traffic in percent; null when the strategy has no fixed split. */
  share: number | null;
}

export interface RouteTier {
  tierId: string;
  state: RouteTierState;
  members: RouteMember[];
}

export interface RoutePreview {
  tiers: RouteTier[];
  hasActive: boolean;
}
