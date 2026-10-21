// Upstream cedar_ember contract, following opencodex anthropic-reset-grants.
import { apiCallApi } from './apiCall';
import { CLAUDE_REQUEST_HEADERS } from '@/utils/quota/constants';

export const ANTHROPIC_API_ORIGIN = 'https://api.anthropic.com';
export const ANTHROPIC_RESET_GRANT_PROGRAM = 'cedar_ember';
export const ANTHROPIC_RESET_GRANT_STATUS_PATH = '/api/oauth/usage?cedar_ember=1&skip_spend=1';
export const ANTHROPIC_PROFILE_PATH = '/api/oauth/profile';
/** Same bound the Claude Code client uses for the claim. */
export const ANTHROPIC_RESET_GRANT_REDEEM_TIMEOUT_MS = 25_000;

export const ANTHROPIC_RESET_GRANT_ID_RE = /^[a-z0-9_-]{1,40}$/;
export const ANTHROPIC_RESET_REQUEST_ID_RE = /^[A-Za-z0-9_-]{1,64}$/;
export const ORGANIZATION_UUID_RE =
  /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Usage windows a grant can clear. Others upstream may add are dropped. */
export const ANTHROPIC_RESET_WINDOWS = [
  'five_hour',
  'seven_day',
  'seven_day_overage_included',
] as const;
export type AnthropicResetWindow = (typeof ANTHROPIC_RESET_WINDOWS)[number];

/** Terminal answers the claim endpoint can give. */
export const ANTHROPIC_RESET_RESULTS = [
  'reset',
  'already_used',
  'not_limited',
  'cooldown',
  'ineligible',
  'unavailable',
] as const;
export type AnthropicResetUpstreamResult = (typeof ANTHROPIC_RESET_RESULTS)[number];
/** Upstream results plus the two HTTP refusals that prove nothing was spent. */
export type AnthropicResetSettledCode =
  AnthropicResetUpstreamResult | 'rate_limited' | 'auth_error';

export interface AnthropicResetGrant {
  id: string;
  label: string;
  resetsTotal: number;
  resetsLeft: number;
  startsAt: string | null;
  endsAt: string | null;
  clears: AnthropicResetWindow[];
  paused: boolean;
  usableNow: boolean;
  useRequiresLimit: boolean;
  percentUsed: Partial<Record<AnthropicResetWindow, number>>;
}

export interface AnthropicResetGrantStatus {
  eligible: boolean;
  ineligibleReason: string | null;
  atLimit: boolean;
  grants: AnthropicResetGrant[];
  nextGrantId: string | null;
  weeklyResetsAt: string | null;
  cooldownUntil: string | null;
}

export type AnthropicResetGrantErrorCode = 'auth' | 'upstream' | 'malformed' | 'rate_limited';

/** A read failure. `message` is fixed text; upstream detail is never attached. */
export class AnthropicResetGrantError extends Error {
  constructor(readonly code: AnthropicResetGrantErrorCode) {
    super(`Anthropic reset-grant read failed (${code})`);
    this.name = 'AnthropicResetGrantError';
  }
}

/** The claim was sent (or may have been) and no terminal answer came back. */
export class AnthropicResetGrantUnknownOutcome extends Error {
  constructor() {
    super('Anthropic reset-grant claim outcome is unknown');
    this.name = 'AnthropicResetGrantUnknownOutcome';
  }
}

const KNOWN_INELIGIBLE_REASONS = new Set([
  'config_off',
  'tier',
  'seat',
  'mobile',
  'surface',
  'cli_version',
  'no_grant',
  'tenure',
  'other_experiment',
  'unavailable',
  'unknown',
]);

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isCount(value: unknown): value is number {
  return typeof value === 'number' && Number.isInteger(value) && value >= 0;
}

/** Optional ISO timestamp: absent/null → null, anything unparsable rejects the block. */
function optionalTimestamp(value: unknown): string | null | undefined {
  if (value === undefined || value === null) return null;
  if (typeof value !== 'string' || Number.isNaN(Date.parse(value))) return undefined;
  return value;
}

function optionalBoolean(value: unknown, fallback: boolean): boolean | undefined {
  if (value === undefined || value === null) return fallback;
  return typeof value === 'boolean' ? value : undefined;
}

/** Upstream display text, stripped of control characters and bounded. */
function safeLabel(value: unknown): string {
  if (typeof value !== 'string') return '';
  return (
    value
      // Strip untrusted display control characters.
      // eslint-disable-next-line no-control-regex
      .replace(/[\u0000-\u001f\u007f-\u009f]/g, ' ')
      .replace(/\s+/g, ' ')
      .trim()
      .slice(0, 120)
  );
}

function parseWindows(value: unknown): AnthropicResetWindow[] | undefined {
  if (value === undefined || value === null) return [];
  if (!Array.isArray(value)) return undefined;
  return ANTHROPIC_RESET_WINDOWS.filter((window) => value.includes(window));
}

function parsePercentUsed(value: unknown): Partial<Record<AnthropicResetWindow, number>> {
  const out: Partial<Record<AnthropicResetWindow, number>> = {};
  if (!isRecord(value)) return out;
  for (const window of ANTHROPIC_RESET_WINDOWS) {
    const percent = value[window];
    if (typeof percent === 'number' && Number.isInteger(percent) && percent >= 0 && percent <= 100)
      out[window] = percent;
  }
  return out;
}

function parseGrant(value: unknown): AnthropicResetGrant | null {
  if (!isRecord(value)) return null;
  const { id, resets_total: total, resets_left: left } = value;
  if (typeof id !== 'string' || !ANTHROPIC_RESET_GRANT_ID_RE.test(id)) return null;
  if (!isCount(total) || !isCount(left) || left > total) return null;
  const startsAt = optionalTimestamp(value.starts_at);
  const endsAt = optionalTimestamp(value.ends_at);
  const clears = parseWindows(value.clears);
  const paused = optionalBoolean(value.paused, false);
  // Missing usability flags default to the refusing side: a grant that does not
  // say it is usable is not offered for spending.
  const usableNow = optionalBoolean(value.usable_now, false);
  const useRequiresLimit = optionalBoolean(value.use_requires_limit, true);
  if (
    startsAt === undefined ||
    endsAt === undefined ||
    clears === undefined ||
    paused === undefined ||
    usableNow === undefined ||
    useRequiresLimit === undefined
  )
    return null;
  return {
    id,
    label: safeLabel(value.label),
    resetsTotal: total,
    resetsLeft: left,
    startsAt,
    endsAt,
    clears,
    paused,
    usableNow,
    useRequiresLimit,
    percentUsed: parsePercentUsed(value.percent_used),
  };
}

/**
 * Parses the `cedar_ember` block. Returns null for a missing or malformed block;
 * one malformed grant, a duplicate id, or a bad timestamp rejects the whole block.
 */
export function parseAnthropicResetGrantStatus(block: unknown): AnthropicResetGrantStatus | null {
  if (!isRecord(block) || typeof block.eligible !== 'boolean') return null;
  const rawGrants = block.grants ?? [];
  if (!Array.isArray(rawGrants)) return null;
  const grants: AnthropicResetGrant[] = [];
  const seen = new Set<string>();
  for (const raw of rawGrants) {
    const grant = parseGrant(raw);
    if (!grant || seen.has(grant.id)) return null;
    seen.add(grant.id);
    grants.push(grant);
  }
  const reason = block.ineligible_reason;
  if (reason !== undefined && reason !== null && typeof reason !== 'string') return null;
  const atLimit = optionalBoolean(block.at_limit, false);
  const weeklyResetsAt = optionalTimestamp(block.weekly_resets_at);
  const cooldownUntil = optionalTimestamp(block.cooldown_until);
  if (atLimit === undefined || weeklyResetsAt === undefined || cooldownUntil === undefined)
    return null;
  const next = block.next_grant_id;
  return {
    eligible: block.eligible,
    ineligibleReason:
      typeof reason === 'string'
        ? KNOWN_INELIGIBLE_REASONS.has(reason)
          ? reason
          : 'unknown'
        : null,
    atLimit,
    grants,
    nextGrantId: typeof next === 'string' && seen.has(next) ? next : null,
    weeklyResetsAt,
    cooldownUntil,
  };
}

/** Why a grant cannot be spent right now, or null when it can. */
export function anthropicResetGrantBlocker(
  status: AnthropicResetGrantStatus,
  grantId: string
): 'ineligible' | 'unknown_grant' | 'paused' | 'not_usable' | 'exhausted' | 'not_limited' | null {
  if (!status.eligible) return 'ineligible';
  const grant = status.grants.find((candidate) => candidate.id === grantId);
  if (!grant) return 'unknown_grant';
  if (grant.paused) return 'paused';
  if (!grant.usableNow) return 'not_usable';
  if (grant.resetsLeft <= 0) return 'exhausted';
  if (grant.useRequiresLimit && !status.atLimit) return 'not_limited';
  return null;
}

async function readAccount(authIndex: string, path: string): Promise<Record<string, unknown>> {
  const response = await apiCallApi.request(
    {
      authIndex,
      method: 'GET',
      url: ANTHROPIC_API_ORIGIN + path,
      header: { ...CLAUDE_REQUEST_HEADERS },
    },
    { timeout: 12000 }
  );
  if (response.statusCode === 429) throw new AnthropicResetGrantError('rate_limited');
  if (response.statusCode < 200 || response.statusCode >= 300 || !isRecord(response.body)) {
    throw new AnthropicResetGrantError('upstream');
  }
  return response.body;
}

export async function readClaudeResetGrants(authIndex: string) {
  const body = await readAccount(authIndex, ANTHROPIC_RESET_GRANT_STATUS_PATH);
  const status = parseAnthropicResetGrantStatus(body.cedar_ember);
  if (!status) throw new AnthropicResetGrantError('malformed');
  return status;
}

export async function readClaudeOrganization(authIndex: string) {
  const body = await readAccount(authIndex, ANTHROPIC_PROFILE_PATH);
  const uuid = isRecord(body.organization) ? body.organization.uuid : undefined;
  if (typeof uuid !== 'string' || !ORGANIZATION_UUID_RE.test(uuid)) {
    throw new AnthropicResetGrantError('malformed');
  }
  return uuid.toLowerCase();
}

export async function claimClaudeResetGrant(
  authIndex: string,
  organization: string,
  grantId: string,
  requestId: string
): Promise<AnthropicResetSettledCode> {
  if (
    !ORGANIZATION_UUID_RE.test(organization) ||
    !ANTHROPIC_RESET_GRANT_ID_RE.test(grantId) ||
    !ANTHROPIC_RESET_REQUEST_ID_RE.test(requestId)
  )
    throw new AnthropicResetGrantError('malformed');
  try {
    const response = await apiCallApi.request(
      {
        authIndex,
        method: 'POST',
        url: `${ANTHROPIC_API_ORIGIN}/api/organizations/${organization}/reset_rate_limits`,
        header: { ...CLAUDE_REQUEST_HEADERS },
        data: JSON.stringify({
          program: ANTHROPIC_RESET_GRANT_PROGRAM,
          grant_id: grantId,
          request_id: requestId,
        }),
      },
      { timeout: ANTHROPIC_RESET_GRANT_REDEEM_TIMEOUT_MS }
    );
    if (response.statusCode === 429) return 'rate_limited';
    if (response.statusCode === 401 || response.statusCode === 403) return 'auth_error';
    const result = isRecord(response.body) ? response.body.result : undefined;
    if (
      response.statusCode >= 200 &&
      response.statusCode < 300 &&
      typeof result === 'string' &&
      (ANTHROPIC_RESET_RESULTS as readonly string[]).includes(result)
    ) {
      return result as AnthropicResetUpstreamResult;
    }
  } catch {
    /* The proxy may have sent the claim even if the management request failed. */
  }
  throw new AnthropicResetGrantUnknownOutcome();
}
