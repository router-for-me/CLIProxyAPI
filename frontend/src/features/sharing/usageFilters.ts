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
 * pagination removed. Must stay identical to buildModelCandidateQuery when
 * no model filter is applied, so the page can reuse the main summary as the
 * model candidate source instead of issuing a duplicate request. */
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

/** Attribute facets describe every value present under the current scope, time
 * range and resource, so a dropdown never collapses to its own selection. */
export function buildFacetQuery(queryString: string, scope: UsageScope, me: string): string {
  const values = Object.fromEntries(new URLSearchParams(queryString));
  delete values.scope;
  delete values.page;
  delete values.page_size;
  for (const key of [
    'outcome',
    'status',
    'sort',
    'model',
    'key_id',
    'consumer',
    'provider',
    'stream',
    'endpoint',
    'effort',
    'balanced',
  ])
    delete values[key];
  if (scope === 'mine') values.consumer = me;
  if (scope === 'contributed') values.owner = me;
  return new URLSearchParams(values).toString();
}

// Model candidates must not depend on the applied model filter itself;
// otherwise applying one collapses the dropdown to just that model.
export function buildModelCandidateQuery(
  queryString: string,
  scope: UsageScope,
  me: string
): string {
  const values = Object.fromEntries(new URLSearchParams(queryString));
  delete values.scope;
  delete values.model;
  delete values.outcome;
  delete values.status;
  delete values.sort;
  delete values.page;
  delete values.page_size;
  if (scope === 'mine') values.consumer = me;
  if (scope === 'contributed') values.owner = me;
  return new URLSearchParams(values).toString();
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
  return (
    models.find((model) => normalizeAutocompleteText(model) === normalized) ??
    (appliedModel && normalizeAutocompleteText(appliedModel) === normalized ? appliedModel : null)
  );
}

export function resolveUsageKey(text: string, options: UsageKeyOption[]): UsageKeyOption | null {
  const normalized = normalizeAutocompleteText(text);
  if (!normalized) return { label: '' };
  return options.find((option) => normalizeAutocompleteText(option.label) === normalized) ?? null;
}
