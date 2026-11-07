import { normalizeAutocompleteText } from '@/components/ui/autocompleteMatching';

type UsageScope = 'mine' | 'contributed' | 'site';

export interface UsageCredentialRef {
  id?: unknown;
  name?: unknown;
  owner?: unknown;
}

export function buildCredentialUsagePath(
  credential: UsageCredentialRef,
  me: string,
  isAdmin: boolean
): string {
  const id = typeof credential.id === 'string' ? credential.id.trim() : '';
  if (!id) return '';
  const owner = typeof credential.owner === 'string' ? credential.owner : '';
  const scope: UsageScope = isAdmin ? 'site' : me && owner === me ? 'contributed' : 'mine';
  const params = new URLSearchParams({ credential_id: id });
  if (scope !== 'mine') params.set('scope', scope);
  return `/usage?${params.toString()}`;
}

/** Query for the main summary/log requests: scope identity folded in,
 * pagination removed. */
export function buildUsageFilterQuery(queryString: string, scope: UsageScope, me: string): string {
  const values = Object.fromEntries(new URLSearchParams(queryString));
  delete values.page;
  delete values.page_size;
  delete values.scope;
  if (scope === 'mine') values.consumer = me;
  if (scope === 'contributed') values.owner = me;
  return new URLSearchParams(values).toString();
}

/** Legacy helper kept for callers that only strip the outcome toggle. */
export function withoutOutcome(query: string): string {
  const values = new URLSearchParams(query);
  values.delete('outcome');
  return values.toString();
}

/** Statistics describe the full result set: outcome tabs, status codes and sort
 * narrow only the records list, never the KPIs, trend or ranking. */
export function withoutListParams(query: string): string {
  const values = new URLSearchParams(query);
  for (const key of ['outcome', 'status', 'sort']) values.delete(key);
  return values.toString();
}

export interface UsageKeyOption {
  label: string;
  keyId?: string;
  consumer?: string;
}

export function resolveUsageModel(
  text: string,
  models: string[],
  appliedModel = ''
): string | null {
  const normalized = normalizeAutocompleteText(text);
  if (!normalized) return '';
  const candidate = models.find((model) => normalizeAutocompleteText(model) === normalized);
  if (candidate) return candidate;
  if (appliedModel && normalizeAutocompleteText(appliedModel) === normalized) return appliedModel;
  // Serving-pinned IDs (channel/model) bypass the trimmed candidate list so a
  // rare model outside the top candidates still applies; bare-model text only
  // resolves through candidates to preserve the no-fuzzy rule.
  if (normalized.includes('/')) return normalized;
  return null;
}

export function resolveUsageKey(text: string, options: UsageKeyOption[]): UsageKeyOption | null {
  const normalized = normalizeAutocompleteText(text);
  if (!normalized) return { label: '' };
  return options.find((option) => normalizeAutocompleteText(option.label) === normalized) ?? null;
}
