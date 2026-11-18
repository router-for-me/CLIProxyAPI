import { useCallback, useMemo, useState } from 'react';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import { getErrorMessage } from '@/utils/helpers';
import {
  getApiKeyFunUsageEndpoints,
  normalizeApiKeyFunUsagePayload,
  type ApiKeyFunUsageSummary,
} from '../../sponsor';

const DEFAULT_TIMEOUT_MS = 30_000;

export type SponsorUsageState = 'idle' | 'loading' | 'success' | 'error';

export interface SponsorUsageStatus {
  state: SponsorUsageState;
  message: string;
  summary: ApiKeyFunUsageSummary | null;
}

export interface SponsorUsageMessages {
  apiKeyRequired: string;
  emptyResponse: string;
  requestFailed: string;
}

export interface UseSponsorUsageCheckArgs {
  baseUrl: string;
  apiKey: string;
  fallbackApiKey?: string;
}

const IDLE: SponsorUsageStatus = {
  state: 'idle',
  message: '',
  summary: null,
};

interface SponsorUsageStateBucket {
  signature: string;
  status: SponsorUsageStatus;
}

export function useSponsorUsageCheck(
  args: UseSponsorUsageCheckArgs,
  messages: SponsorUsageMessages
) {
  const { baseUrl, apiKey, fallbackApiKey } = args;

  const endpoints = useMemo(() => getApiKeyFunUsageEndpoints(baseUrl), [baseUrl]);
  const signature = useMemo(
    () => [baseUrl, apiKey, fallbackApiKey ?? ''].join('||'),
    [apiKey, baseUrl, fallbackApiKey]
  );
  const [bucket, setBucket] = useState<SponsorUsageStateBucket>(() => ({
    signature,
    status: IDLE,
  }));
  const status = bucket.signature === signature ? bucket.status : IDLE;
  const setStatus = useCallback(
    (next: SponsorUsageStatus) => {
      setBucket({ signature, status: next });
    },
    [signature]
  );

  const run = useCallback(async () => {
    const key = apiKey.trim() || (fallbackApiKey ?? '').trim();
    if (!key) {
      setStatus({
        state: 'error',
        message: messages.apiKeyRequired,
        summary: null,
      });
      return;
    }

    setStatus({ state: 'loading', message: '', summary: null });
    let lastNetworkError = '';

    for (let idx = 0; idx < endpoints.length; idx += 1) {
      const endpoint = endpoints[idx];
      try {
        const result = await apiCallApi.request(
          {
            method: 'GET',
            url: endpoint,
            header: {
              Authorization: `Bearer ${key}`,
            },
          },
          { timeout: DEFAULT_TIMEOUT_MS }
        );

        if (result.statusCode < 200 || result.statusCode >= 300) {
          setStatus({
            state: 'error',
            message: getApiCallErrorMessage(result),
            summary: null,
          });
          return;
        }

        const summary = normalizeApiKeyFunUsagePayload(result.body ?? result.bodyText);
        if (!summary) {
          setStatus({
            state: 'error',
            message: messages.emptyResponse,
            summary: null,
          });
          return;
        }

        setStatus({
          state: 'success',
          message: '',
          summary,
        });
        return;
      } catch (err) {
        lastNetworkError = getErrorMessage(err, messages.requestFailed);
        if (idx < endpoints.length - 1) {
          continue;
        }
      }
    }

    setStatus({
      state: 'error',
      message: lastNetworkError || messages.requestFailed,
      summary: null,
    });
  }, [apiKey, endpoints, fallbackApiKey, messages, setStatus]);

  return {
    status,
    isLoading: status.state === 'loading',
    run,
    reset: () => setStatus(IDLE),
  };
}
