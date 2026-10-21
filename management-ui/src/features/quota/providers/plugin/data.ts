import type { TFunction } from 'i18next';
import type {
  AntigravityQuotaSubscription,
  AntigravityQuotaSummaryPayload,
  AuthFileItem,
  PluginListEntry,
  PluginQuotaMetric,
  PluginQuotaState,
} from '@/types';
import { apiClient } from '@/services/api/client';
import { pluginsApi } from '@/services/api/plugins';
import { normalizeAuthIndex } from '@/utils/authIndex';
import { buildAntigravityQuotaGroups, isDisabledAuthFile, isPluginQuotaFile } from '@/utils/quota';
import type { QuotaProviderData } from '../types';

type PluginQuotaPayload = AntigravityQuotaSummaryPayload & {
  subscription?: Partial<AntigravityQuotaSubscription> | null;
  summary?: Array<Partial<PluginQuotaMetric>>;
};

type PluginQuotaData = {
  groups: PluginQuotaState['groups'];
  subscription: AntigravityQuotaSubscription | null;
  summary: PluginQuotaMetric[];
};

export const normalizePluginQuotaSummary = (
  summary: PluginQuotaPayload['summary']
): PluginQuotaMetric[] => {
  if (!Array.isArray(summary)) return [];
  return summary.flatMap((metric) => {
    const key = typeof metric.key === 'string' ? metric.key.trim() : '';
    const label = typeof metric.label === 'string' ? metric.label.trim() : '';
    if (!key || !label || typeof metric.value !== 'number' || !Number.isFinite(metric.value))
      return [];
    const format =
      metric.format === 'currency' || metric.format === 'number' ? metric.format : undefined;
    const unit =
      typeof metric.unit === 'string' && metric.unit.trim() ? metric.unit.trim() : undefined;
    const currency =
      format === 'currency' &&
      typeof metric.currency === 'string' &&
      /^[A-Z]{3}$/.test(metric.currency)
        ? metric.currency
        : undefined;
    return [{ key, label, value: metric.value, unit, format, currency }];
  });
};

const normalizeSubscription = (
  subscription: PluginQuotaPayload['subscription']
): AntigravityQuotaSubscription | null => {
  if (!subscription) return null;
  const value = (field: unknown) =>
    typeof field === 'string' && field.trim() ? field.trim() : null;
  return {
    plan: value(subscription.plan),
    tierName: value(subscription.tierName),
    tierId: value(subscription.tierId),
  };
};

const normalizeProviderId = (value: unknown) =>
  typeof value === 'string' ? value.trim().toLowerCase() : '';

/**
 * Compatibility heuristic for older v8 credentials without quotaPluginId.
 * Prefer a quota identifier, plugin ID, then auth provider match. A
 * plugin matched only through DescribeQuota's supported providers is not exposed by /plugins,
 * so fall back to the sole quota plugin when exactly one is registered.
 */
export const resolveQuotaPluginId = (
  plugins: readonly PluginListEntry[],
  provider: string
): string | null => {
  const target = normalizeProviderId(provider);
  if (!target) return null;
  const candidates = plugins.filter((plugin) => plugin.registered && plugin.supportsQuota);
  const match =
    candidates.find((plugin) => normalizeProviderId(plugin.quotaProvider) === target) ??
    candidates.find((plugin) => normalizeProviderId(plugin.id) === target) ??
    candidates.find((plugin) => normalizeProviderId(plugin.oauthProvider) === target) ??
    (candidates.length === 1 ? candidates[0] : undefined);
  return match?.id ?? null;
};

const PLUGIN_LIST_TTL_MS = 30_000;
let pluginListCache: {
  revision: number;
  expiresAt: number;
  promise: Promise<PluginListEntry[]>;
} | null = null;

/** Refresh-all fans out per credential; share one plugin list per connection. */
const loadQuotaPlugins = (): Promise<PluginListEntry[]> => {
  const revision = apiClient.getConnectionRevision();
  const now = Date.now();
  if (pluginListCache && pluginListCache.revision === revision && pluginListCache.expiresAt > now) {
    return pluginListCache.promise;
  }
  const promise = pluginsApi.list().then((response) => response.plugins);
  const entry = { revision, expiresAt: now + PLUGIN_LIST_TTL_MS, promise };
  pluginListCache = entry;
  promise.catch(() => {
    if (pluginListCache === entry) pluginListCache = null;
  });
  return promise;
};

export const resetPluginQuotaRouteCache = () => {
  pluginListCache = null;
};

export const fetchPluginQuota = async (
  file: AuthFileItem,
  t: TFunction
): Promise<PluginQuotaData> => {
  const authIndex = normalizeAuthIndex(file.authIndex ?? file['auth_index']);
  if (!authIndex) throw new Error(t('plugin_quota.missing_auth_index'));

  const provider = String(file.quotaProvider ?? file['quota_provider'] ?? '').trim();
  // New v8 credentials carry the host's authoritative selection, including multi-provider
  // plugins and healthy alternatives. Do not re-resolve it against a stale plugin list.
  let pluginId = file.quotaPluginId;
  if (!pluginId) {
    // Older v8 servers may advertise declarative probes, which have no v8 quota route.
    if (!provider) throw new Error(t('plugin_quota.probe_unsupported'));
    pluginId = resolveQuotaPluginId(await loadQuotaPlugins(), provider) ?? undefined;
  }
  if (!pluginId) throw new Error(t('plugin_quota.plugin_not_found', { provider }));

  const payload = await apiClient.post<PluginQuotaPayload>(
    `/plugins/${encodeURIComponent(pluginId)}/quota`,
    { auth_index: authIndex }
  );
  return {
    groups: buildAntigravityQuotaGroups(payload),
    subscription: normalizeSubscription(payload.subscription),
    summary: normalizePluginQuotaSummary(payload.summary),
  };
};

export const PLUGIN_CONFIG: QuotaProviderData<PluginQuotaState, PluginQuotaData> = {
  type: 'plugin',
  i18nPrefix: 'plugin_quota',
  filterFn: (file) => isPluginQuotaFile(file) && !isDisabledAuthFile(file),
  fetchQuota: fetchPluginQuota,
  storeSelector: (state) => state.pluginQuota,
  storeSetter: 'setPluginQuota',
  buildLoadingState: () => ({ status: 'loading', groups: [], subscription: null, summary: [] }),
  buildSuccessState: (data) => ({ status: 'success', ...data }),
  buildErrorState: (error, errorStatus) => ({
    status: 'error',
    groups: [],
    subscription: null,
    summary: [],
    error,
    errorStatus,
  }),
};
