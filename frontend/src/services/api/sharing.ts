import { apiClient } from './client';
import type { ApiError } from '@/types';
import type {
  Account,
  AuditEntry,
  BoardMessage,
  Channel,
  HistoryPage,
  Invitation,
  MarketplaceModel,
  ModelPerformance,
  PersonalKey,
  SharedKeyRef,
  SummaryResponse,
  UsageFacets,
  UsageEntry,
} from '@/features/sharing/types';

export interface ProviderModelInput {
  name: string;
  alias?: string;
}

export interface ProviderPayload {
  name?: string;
  label?: string;
  base_url?: string;
  api_key?: string;
  proxy_url?: string;
  models?: ProviderModelInput[];
  disabled?: boolean;
}

type PersonalKeyResponse = Omit<PersonalKey, 'blocked_models'> & {
  blocked_models?: string[] | null;
};

const normalizePersonalKey = (key: PersonalKeyResponse): PersonalKey => ({
  ...key,
  blocked_models: key.blocked_models ?? [],
});

export const sharingPath = (path: string) => `/v8/hub${path}`;

/** Backend code for "the shared usage index is still cold-building". */
export const JOURNAL_INDEX_BUILDING = 'journal_index_building';

const isIndexBuilding = (error: unknown): error is ApiError =>
  !!error &&
  typeof error === 'object' &&
  (error as ApiError).status === 503 &&
  (error as ApiError).apiCode === JOURNAL_INDEX_BUILDING;

const delayOrAbort = (ms: number, signal?: AbortSignal) =>
  new Promise<void>((resolve, reject) => {
    if (!signal) {
      setTimeout(resolve, ms);
      return;
    }
    const timer = setTimeout(() => {
      signal.removeEventListener('abort', abort);
      resolve();
    }, ms);
    const abort = () => {
      clearTimeout(timer);
      reject(signal.reason instanceof Error ? signal.reason : new Error('aborted'));
    };
    if (signal.aborted) abort();
    else signal.addEventListener('abort', abort, { once: true });
  });

/** Retries only the explicit cold-index state (503 + journal_index_building,
 * served with Retry-After: 2) every 2s. The promise stays pending while
 * retrying so callers keep showing their loading state instead of a terminal
 * error; aborting the signal cancels both the request and the pending wait.
 * No other status or code is ever retried here. */
export async function retryWhileIndexBuilding<T>(
  request: (signal?: AbortSignal) => Promise<T>,
  signal?: AbortSignal,
  delayMs = 2000
): Promise<T> {
  for (;;) {
    try {
      return await request(signal);
    } catch (error) {
      if (!isIndexBuilding(error) || signal?.aborted) throw error;
      await delayOrAbort(delayMs, signal);
    }
  }
}

const indexAwareGet = <T>(
  path: string,
  params: Record<string, string | number> | undefined,
  signal?: AbortSignal
) =>
  retryWhileIndexBuilding((attempt) => apiClient.get<T>(path, { params, signal: attempt }), signal);

export const sharingApi = {
  status: () => apiClient.get<{ needs_setup: boolean }>(sharingPath('/status')),
  me: () => apiClient.get<{ user: Account }>(sharingPath('/me')),
  login: (username: string, password: string) =>
    apiClient.post<{ user: Account; token: string }>(sharingPath('/login'), { username, password }),
  register: (body: { username: string; password: string; invite: string }) =>
    apiClient.post<{ user: Account }>(sharingPath('/register'), body),
  logout: () => apiClient.post(sharingPath('/logout')),
  channels: (signal?: AbortSignal) =>
    apiClient.get<{ channels: Channel[] }>(sharingPath('/channels'), { signal }),
  createChannel: (body: { slug: string; public?: boolean }) =>
    apiClient.post<{ channel: Channel }>(sharingPath('/channels'), body),
  updateChannel: (
    id: string,
    body: { published?: boolean; announcement?: string; public?: boolean }
  ) => apiClient.patch(sharingPath(`/channels/${encodeURIComponent(id)}`), body),
  deleteChannel: (id: string) =>
    apiClient.delete(sharingPath(`/channels/${encodeURIComponent(id)}`)),
  credentialApiCall: (
    routeID: string,
    body: { method: string; url: string; header?: Record<string, string>; data?: string }
  ) =>
    apiClient.post<Record<string, unknown>>(
      sharingPath(`/credentials/${encodeURIComponent(routeID)}/api-call`),
      body
    ),
  boardMessages: (channelID: string, beforeID?: string, limit?: number) =>
    apiClient.get<{ messages: BoardMessage[] }>(
      sharingPath(`/channels/${encodeURIComponent(channelID)}/messages`),
      { params: { ...(beforeID ? { before_id: beforeID } : {}), ...(limit ? { limit } : {}) } }
    ),
  postBoardMessage: (channelID: string, content: string) =>
    apiClient.post<{ message: BoardMessage }>(
      sharingPath(`/channels/${encodeURIComponent(channelID)}/messages`),
      { content }
    ),
  deleteBoardMessage: (channelID: string, messageID: string) =>
    apiClient.delete(
      sharingPath(
        `/channels/${encodeURIComponent(channelID)}/messages/${encodeURIComponent(messageID)}`
      )
    ),
  createProvider: (channelID: string, body: ProviderPayload) =>
    apiClient.post<{ status: string; provider_id: string; prefix: string }>(
      sharingPath('/providers'),
      body,
      { params: { channel_id: channelID } }
    ),
  updateProvider: (routeID: string, body: ProviderPayload) =>
    apiClient.patch(sharingPath(`/providers/${encodeURIComponent(routeID)}`), body),
  discoverProviderModels: (base_url: string, api_key: string, proxy_url?: string) =>
    apiClient.post<{ models: string[] }>(sharingPath('/providers/discover'), {
      base_url,
      api_key,
      ...(proxy_url ? { proxy_url } : {}),
    }),
  marketplace: (signal?: AbortSignal) =>
    apiClient.get<{ models: MarketplaceModel[]; channels: Channel[] }>(
      sharingPath('/marketplace'),
      {
        signal,
      }
    ),
  marketplacePerformance: (signal?: AbortSignal) =>
    indexAwareGet<{ models: ModelPerformance[] }>(
      sharingPath('/marketplace/performance'),
      undefined,
      signal
    ),
  keys: async (signal?: AbortSignal) => {
    const response = await apiClient.get<{ keys: PersonalKeyResponse[] }>(sharingPath('/keys'), {
      signal,
    });
    return { keys: response.keys.map(normalizePersonalKey) };
  },
  allKeys: (signal?: AbortSignal) =>
    apiClient.get<{ keys: SharedKeyRef[] }>(sharingPath('/keys/all'), { signal }),
  createKey: async (name: string) => {
    const response = await apiClient.post<{ key: PersonalKeyResponse; secret: string }>(
      sharingPath('/keys'),
      { name }
    );
    return { ...response, key: normalizePersonalKey(response.key) };
  },
  updateKey: (
    id: string,
    body: { name?: string; disabled?: boolean; auto_balance?: boolean; blocked_models?: string[] }
  ) => apiClient.patch(sharingPath(`/keys/${encodeURIComponent(id)}`), body),
  revokeKey: (id: string) => apiClient.delete(sharingPath(`/keys/${encodeURIComponent(id)}`)),
  users: (signal?: AbortSignal) =>
    apiClient.get<{ users: Account[] }>(sharingPath('/users'), { signal }),
  updateUser: (id: string, body: { disabled?: boolean; role?: string }) =>
    apiClient.patch(sharingPath(`/users/${encodeURIComponent(id)}`), body),
  invites: () => apiClient.get<{ invites: Invitation[] }>(sharingPath('/invites')),
  createInvite: (max_uses: number, days: number) =>
    apiClient.post<{ invite: Invitation }>(sharingPath('/invites'), { max_uses, days }),
  revokeInvite: (id: string) => apiClient.delete(sharingPath(`/invites/${encodeURIComponent(id)}`)),
  password: (current_password: string, password: string) =>
    apiClient.post(sharingPath('/me/password'), { current_password, password }),
  usage: (params: Record<string, string | number>, signal?: AbortSignal) =>
    indexAwareGet<HistoryPage<UsageEntry>>(sharingPath('/usage'), params, signal),
  summary: (params: Record<string, string | number>, signal?: AbortSignal) =>
    indexAwareGet<SummaryResponse>(sharingPath('/usage/summary'), params, signal),
  usageFacets: (params: Record<string, string | number>, signal?: AbortSignal) =>
    indexAwareGet<UsageFacets>(sharingPath('/usage/facets'), params, signal),
  usageAttempts: (trace: string, signal?: AbortSignal) =>
    indexAwareGet<{ items: UsageEntry[] }>(sharingPath('/usage/attempts'), { trace }, signal),
  audits: (params: Record<string, string | number>, signal?: AbortSignal) =>
    indexAwareGet<HistoryPage<AuditEntry>>(sharingPath('/audit'), params, signal),
  credential: (routeID: string, body: Record<string, unknown>) =>
    apiClient.patch(sharingPath(`/credentials/${encodeURIComponent(routeID)}`), body),
  claim: (routeID: string, channel_id: string) =>
    apiClient.post(sharingPath(`/credentials/${encodeURIComponent(routeID)}/claim`), {
      channel_id,
    }),
};
