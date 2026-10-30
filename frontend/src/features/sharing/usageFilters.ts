import { normalizeAutocompleteText } from '@/components/ui/autocompleteMatching';

type UsageScope = 'mine' | 'contributed' | 'site';

/** Query for the main summary/log requests: scope identity folded in,
 * pagination removed. Must stay identical to buildModelCandidateQuery when
 * no model filter is applied, so the page can reuse the main summary as the
 * model candidate source instead of issuing a duplicate request. */
export function buildUsageFilterQuery(
  queryString: string,
  scope: UsageScope,
  me: string
): string {
  const values = Object.fromEntries(new URLSearchParams(queryString));
  delete values.page;
  delete values.page_size;
  delete values.scope;
  if (scope === 'mine') values.consumer = me;
  if (scope === 'contributed') values.owner = me;
  return new URLSearchParams(values).toString();
}

/** Statistics describe every outcome; the outcome toggle narrows only the list. */
export function withoutOutcome(query: string): string {
  const values = new URLSearchParams(query);
  values.delete('outcome');
  return values.toString();
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

export function resolveUsageModel(text: string, models: string[], appliedModel = ''): string | null {
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
