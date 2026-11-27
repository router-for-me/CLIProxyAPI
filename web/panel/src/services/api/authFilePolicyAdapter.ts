import type { RequestScopedErrorRule } from '@/types/provider';

const rawRule = Symbol('credentialRawRule');
type PreservedRule = RequestScopedErrorRule & { [rawRule]?: Record<string, unknown> };

export interface CredentialAliasDraft {
  name: string;
  alias: string;
  fork?: boolean;
  displayName?: string;
  forceMapping?: boolean;
  original?: Record<string, unknown>;
}
const object = (value: unknown): value is Record<string, unknown> =>
  value !== null && typeof value === 'object' && !Array.isArray(value);
export const readCredentialPolicyValue = (json: Record<string, unknown>, key: string): unknown =>
  Object.prototype.hasOwnProperty.call(json, key) ? json[key] : json[key.replace(/_/g, '-')];

/** null means opaque existing data: retain it until explicitly replaced or cleared. */
export function readCredentialAliases(value: unknown): CredentialAliasDraft[] | null {
  if (value == null) return [];
  if (!Array.isArray(value)) return null;
  const rows: CredentialAliasDraft[] = [];
  for (const row of value) {
    if (
      !object(row) ||
      typeof row.name !== 'string' ||
      typeof row.alias !== 'string' ||
      (row.fork !== undefined && typeof row.fork !== 'boolean') ||
      (row['display-name'] !== undefined && typeof row['display-name'] !== 'string') ||
      (row['force-mapping'] !== undefined && typeof row['force-mapping'] !== 'boolean')
    )
      return null;
    rows.push({
      name: row.name,
      alias: row.alias,
      fork: row.fork as boolean | undefined,
      displayName: row['display-name'] as string | undefined,
      forceMapping: row['force-mapping'] as boolean | undefined,
      original: { ...row },
    });
  }
  return rows;
}
export function buildCredentialAliases(rows: CredentialAliasDraft[]): Record<string, unknown>[] {
  return rows.map((row) => {
    const result = { ...row.original, name: row.name, alias: row.alias };
    for (const [key, value] of Object.entries({
      fork: row.fork,
      'display-name': row.displayName,
      'force-mapping': row.forceMapping,
    })) {
      if (value !== undefined) Object.assign(result, { [key]: value });
    }
    return result;
  });
}
export function readCredentialRules(value: unknown): RequestScopedErrorRule[] | null {
  if (value == null) return [];
  if (!Array.isArray(value)) return null;
  const rows: RequestScopedErrorRule[] = [];
  for (const row of value) {
    if (
      !object(row) ||
      (row.status !== undefined && typeof row.status !== 'number') ||
      (row.action !== undefined && typeof row.action !== 'string') ||
      [row.match, row['match-regexr']].some(
        (v) => v !== undefined && (!Array.isArray(v) || !v.every((s) => typeof s === 'string'))
      )
    )
      return null;
    rows.push({ ...row, matchRegex: row['match-regexr'], [rawRule]: { ...row } } as PreservedRule);
  }
  return rows;
}
export function buildCredentialRules(
  rows: RequestScopedErrorRule[],
  originals: (RequestScopedErrorRule | undefined)[]
): Record<string, unknown>[] {
  return rows.map((row, i) => {
    const raw = (originals[i] as PreservedRule | undefined)?.[rawRule];
    const result: Record<string, unknown> = { ...raw };
    for (const key of ['status', 'action', 'match'] as const) {
      if (row[key] !== undefined) result[key] = row[key];
    }
    if (row.matchRegex !== undefined) result['match-regexr'] = row.matchRegex;
    return result;
  });
}

export function applyCredentialPolicyValues(
  json: Record<string, unknown>,
  patch: Record<string, unknown>
) {
  for (const key of ['request_retry', 'model_aliases', 'request_scoped_errors']) {
    if (patch[key] === undefined) continue;
    delete json[key.replace(/_/g, '-')];
    if (key === 'request_retry' && patch[key] === null) delete json[key];
    else json[key] = patch[key];
  }
}
