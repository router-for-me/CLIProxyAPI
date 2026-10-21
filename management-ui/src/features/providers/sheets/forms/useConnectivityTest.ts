import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { apiCallApi, getApiCallErrorMessage } from '@/services/api';
import {
  buildCodexResponsesEndpoint,
  buildClaudeMessagesEndpoint,
  buildGeminiGenerateContentEndpoint,
  buildInteractionsEndpoint,
  buildInteractionsProbePayload,
  INTERACTIONS_API_REVISION,
  buildOpenAIChatCompletionsEndpoint,
} from '@/components/providers/utils';
import { buildHeaderObject, hasHeader } from '@/utils/headers';
import { getErrorMessage } from '@/utils/helpers';
import type { ApiKeyEntryInput, ModelEntryInput, ProviderBrand } from '../../types';

const DEFAULT_TIMEOUT_MS = 30_000;
const DEFAULT_ANTHROPIC_VERSION = '2023-06-01';

export type ConnectivityState = 'idle' | 'loading' | 'success' | 'error';

export interface ConnectivityStatus {
  state: ConnectivityState;
  message: string;
}

const IDLE: ConnectivityStatus = { state: 'idle', message: '' };

const requestFailureMessage = (err: unknown, messages: ConnectivityErrorMessages): string => {
  const raw = getErrorMessage(err);
  const isTimeout =
    (typeof err === 'object' &&
      err !== null &&
      'code' in err &&
      String((err as { code?: string }).code) === 'ECONNABORTED') ||
    raw.toLowerCase().includes('timeout');

  return isTimeout ? messages.timeout(DEFAULT_TIMEOUT_MS / 1000) : raw || messages.requestFailed;
};

const pickModel = (testModel: string | undefined, models: ModelEntryInput[]): string => {
  const trimmed = (testModel ?? '').trim();
  if (trimmed) return trimmed;
  for (const m of models) {
    const name = (m.name ?? '').trim();
    if (name) return name;
  }
  return '';
};

const resolveBearerToken = (headers: Record<string, string>): string => {
  const auth = Object.entries(headers).find(([k]) => k.toLowerCase() === 'authorization')?.[1];
  if (!auth) return '';
  const match = String(auth).match(/^Bearer\s+(.+)$/i);
  return match ? match[1].trim() : '';
};

export interface UseConnectivityTestArgs {
  brand: ProviderBrand;
  baseUrl: string;
  proxyUrl?: string;
  testModel?: string;
  models: ModelEntryInput[];
  formHeaders: Array<{ key: string; value: string }>;
  apiKeyEntries?: ApiKeyEntryInput[];
  apiKey?: string;
  fallbackApiKey?: string;
  authIndex?: string;
}

export interface ConnectivityErrorMessages {
  baseUrlRequired: string;
  endpointInvalid: string;
  apiKeyRequired: string;
  modelRequired: string;
  timeout: (seconds: number) => string;
  requestFailed: string;
}

export interface UseConnectivityTestResult {
  openaiStatuses: ConnectivityStatus[];
  codexStatus: ConnectivityStatus;
  geminiStatus: ConnectivityStatus;
  claudeStatus: ConnectivityStatus;
  isTestingAny: boolean;
  runOpenAIKey: (idx: number) => Promise<boolean>;
  runOpenAIAllKeys: () => Promise<void>;
  runCodex: () => Promise<void>;
  runGemini: () => Promise<void>;
  runClaude: () => Promise<void>;
}

export function useConnectivityTest(
  args: UseConnectivityTestArgs,
  messages: ConnectivityErrorMessages
): UseConnectivityTestResult {
  const {
    brand,
    baseUrl,
    proxyUrl,
    testModel,
    models,
    formHeaders,
    apiKeyEntries,
    apiKey,
    fallbackApiKey,
    authIndex,
  } = args;

  const entriesCount = apiKeyEntries?.length ?? 0;

  const [openaiStatuses, setOpenaiStatuses] = useState<ConnectivityStatus[]>(() =>
    Array.from({ length: entriesCount }, () => IDLE)
  );
  const [codexStatus, setCodexStatus] = useState<ConnectivityStatus>(IDLE);
  const [geminiStatus, setGeminiStatus] = useState<ConnectivityStatus>(IDLE);
  const [claudeStatus, setClaudeStatus] = useState<ConnectivityStatus>(IDLE);
  const [inFlight, setInFlight] = useState(0);

  const entrySignatures = useMemo(
    () =>
      (apiKeyEntries ?? []).map((entry) =>
        [
          entry.apiKey ?? '',
          entry.existingApiKey ?? '',
          entry.authIndex ?? '',
          entry.proxyUrl ?? '',
        ].join('||')
      ),
    [apiKeyEntries]
  );

  const lastEntrySignaturesRef = useRef<string[]>(entrySignatures);
  useEffect(() => {
    const prev = lastEntrySignaturesRef.current;
    const curr = entrySignatures;
    lastEntrySignaturesRef.current = curr;

    setOpenaiStatuses((statuses) => {
      const nextLen = curr.length;
      let mutated = statuses.length !== nextLen;
      const next = statuses.slice(0, nextLen);
      while (next.length < nextLen) next.push(IDLE);
      for (let i = 0; i < nextLen; i++) {
        if (prev[i] !== undefined && prev[i] !== curr[i] && next[i].state !== 'idle') {
          next[i] = IDLE;
          mutated = true;
        }
      }
      return mutated ? next : statuses;
    });
  }, [entrySignatures]);

  const signature = useMemo(() => {
    const h = formHeaders.map((it) => `${it.key}:${it.value}`).join('|');
    const m = models.map((it) => `${it.name}:${it.alias ?? ''}`).join('|');
    return [
      baseUrl,
      proxyUrl ?? '',
      (testModel ?? '').trim(),
      apiKey ?? '',
      fallbackApiKey ?? '',
      authIndex ?? '',
      h,
      m,
    ].join('||');
  }, [apiKey, authIndex, baseUrl, proxyUrl, fallbackApiKey, testModel, formHeaders, models]);

  const lastSignatureRef = useRef(signature);
  useEffect(() => {
    if (lastSignatureRef.current === signature) return;
    lastSignatureRef.current = signature;
    setOpenaiStatuses((prev) => prev.map(() => IDLE));
    setCodexStatus(IDLE);
    setGeminiStatus(IDLE);
    setClaudeStatus(IDLE);
  }, [signature]);

  const updateOpenaiStatus = useCallback((idx: number, value: ConnectivityStatus) => {
    setOpenaiStatuses((prev) => {
      const next = [...prev];
      next[idx] = value;
      return next;
    });
  }, []);

  const runOpenAIKey = useCallback(
    async (idx: number): Promise<boolean> => {
      if (brand !== 'openaiCompatibility') return false;

      const trimmedBase = baseUrl.trim();
      if (!trimmedBase) {
        updateOpenaiStatus(idx, {
          state: 'error',
          message: messages.baseUrlRequired,
        });
        return false;
      }
      const endpoint = buildOpenAIChatCompletionsEndpoint(trimmedBase);
      if (!endpoint) {
        updateOpenaiStatus(idx, {
          state: 'error',
          message: messages.endpointInvalid,
        });
        return false;
      }
      const entry = apiKeyEntries?.[idx];
      const entryKey = (entry?.apiKey ?? '').trim() || (entry?.existingApiKey ?? '').trim();
      const resolvedAuthIndex =
        (entry?.authIndex ?? '').trim() || (authIndex ?? '').trim() || undefined;
      if (!entryKey && !resolvedAuthIndex) {
        updateOpenaiStatus(idx, {
          state: 'error',
          message: messages.apiKeyRequired,
        });
        return false;
      }
      const model = pickModel(testModel, models);
      if (!model) {
        updateOpenaiStatus(idx, {
          state: 'error',
          message: messages.modelRequired,
        });
        return false;
      }

      const headerObj: Record<string, string> = {
        'Content-Type': 'application/json',
        ...buildHeaderObject(formHeaders),
      };
      if (!hasHeader(headerObj, 'authorization')) {
        if (entryKey) {
          headerObj.Authorization = `Bearer ${entryKey}`;
        } else if (resolvedAuthIndex) {
          headerObj.Authorization = 'Bearer $TOKEN$';
        }
      }

      updateOpenaiStatus(idx, { state: 'loading', message: '' });
      setInFlight((n) => n + 1);
      try {
        const result = await apiCallApi.request(
          {
            authIndex: resolvedAuthIndex,
            proxy_url: entry?.proxyUrl?.trim() || undefined,
            method: 'POST',
            url: endpoint,
            header: headerObj,
            data: JSON.stringify({
              model,
              messages: [{ role: 'user', content: 'Hi' }],
              stream: false,
              max_tokens: 5,
            }),
          },
          { timeout: DEFAULT_TIMEOUT_MS }
        );
        if (result.statusCode < 200 || result.statusCode >= 300) {
          throw new Error(getApiCallErrorMessage(result));
        }
        updateOpenaiStatus(idx, { state: 'success', message: '' });
        return true;
      } catch (err) {
        updateOpenaiStatus(idx, {
          state: 'error',
          message: requestFailureMessage(err, messages),
        });
        return false;
      } finally {
        setInFlight((n) => n - 1);
      }
    },
    [
      apiKeyEntries,
      authIndex,
      baseUrl,
      brand,
      formHeaders,
      messages,
      models,
      testModel,
      updateOpenaiStatus,
    ]
  );

  const runOpenAIAllKeys = useCallback(async (): Promise<void> => {
    if (brand !== 'openaiCompatibility') return;
    const entries = apiKeyEntries ?? [];
    if (!entries.length) return;
    await Promise.all(entries.map((_, idx) => runOpenAIKey(idx)));
  }, [apiKeyEntries, brand, runOpenAIKey]);

  const runCodex = useCallback(async (): Promise<void> => {
    if (brand !== 'codex' && brand !== 'meta' && brand !== 'xai') return;

    const trimmedBase = baseUrl.trim();
    if (!trimmedBase) {
      setCodexStatus({ state: 'error', message: messages.baseUrlRequired });
      return;
    }

    const endpoint = buildCodexResponsesEndpoint(trimmedBase);
    if (!endpoint) {
      setCodexStatus({ state: 'error', message: messages.endpointInvalid });
      return;
    }

    const model = pickModel(testModel, models);
    if (!model) {
      setCodexStatus({ state: 'error', message: messages.modelRequired });
      return;
    }

    const customHeaders = buildHeaderObject(formHeaders);
    const explicitKey = (apiKey ?? '').trim();
    const persistedKey = (fallbackApiKey ?? '').trim();
    const hasAuthorization = hasHeader(customHeaders, 'authorization');
    const resolvedKey = explicitKey || persistedKey;
    const resolvedAuthIndex = (authIndex ?? '').trim() || undefined;

    if (!resolvedKey && !hasAuthorization && !resolvedAuthIndex) {
      setCodexStatus({ state: 'error', message: messages.apiKeyRequired });
      return;
    }

    const headerObj: Record<string, string> = {
      'Content-Type': 'application/json',
      ...customHeaders,
    };
    if (!hasHeader(headerObj, 'authorization')) {
      if (resolvedKey) {
        headerObj.Authorization = `Bearer ${resolvedKey}`;
      } else if (resolvedAuthIndex) {
        headerObj.Authorization = 'Bearer $TOKEN$';
      }
    }

    setCodexStatus({ state: 'loading', message: '' });
    setInFlight((n) => n + 1);
    try {
      const result = await apiCallApi.request(
        {
          authIndex: resolvedAuthIndex,
          proxy_url: proxyUrl?.trim() || undefined,
          method: 'POST',
          url: endpoint,
          header: headerObj,
          data: JSON.stringify({
            model,
            input: 'Hi',
            stream: false,
          }),
        },
        { timeout: DEFAULT_TIMEOUT_MS }
      );
      if (result.statusCode < 200 || result.statusCode >= 300) {
        throw new Error(getApiCallErrorMessage(result));
      }
      setCodexStatus({ state: 'success', message: '' });
    } catch (err) {
      setCodexStatus({
        state: 'error',
        message: requestFailureMessage(err, messages),
      });
    } finally {
      setInFlight((n) => n - 1);
    }
  }, [
    apiKey,
    authIndex,
    baseUrl,
    brand,
    fallbackApiKey,
    formHeaders,
    messages,
    models,
    testModel,
    proxyUrl,
  ]);

  const runGemini = useCallback(async (): Promise<void> => {
    if (brand !== 'gemini' && brand !== 'interactions') return;

    const model = pickModel(testModel, models);
    if (!model) {
      setGeminiStatus({ state: 'error', message: messages.modelRequired });
      return;
    }

    const endpoint =
      brand === 'interactions'
        ? buildInteractionsEndpoint(baseUrl ?? '')
        : buildGeminiGenerateContentEndpoint(baseUrl ?? '', model);
    if (!endpoint) {
      setGeminiStatus({ state: 'error', message: messages.endpointInvalid });
      return;
    }

    const customHeaders = buildHeaderObject(formHeaders);
    const explicitKey = (apiKey ?? '').trim();
    const persistedKey = (fallbackApiKey ?? '').trim();
    const hasApiKeyHeader = hasHeader(customHeaders, 'x-goog-api-key');
    const resolvedKey = explicitKey || persistedKey;
    const resolvedAuthIndex = (authIndex ?? '').trim() || undefined;

    if (!resolvedKey && !hasApiKeyHeader && !resolvedAuthIndex) {
      setGeminiStatus({ state: 'error', message: messages.apiKeyRequired });
      return;
    }

    const headerObj: Record<string, string> = {
      'Content-Type': 'application/json',
      ...customHeaders,
    };
    if (!hasHeader(headerObj, 'x-goog-api-key')) {
      if (resolvedKey) {
        headerObj['x-goog-api-key'] = resolvedKey;
      } else if (resolvedAuthIndex) {
        headerObj['x-goog-api-key'] = '$TOKEN$';
      }
    }
    if (brand === 'interactions' && !hasHeader(headerObj, 'api-revision')) {
      headerObj['Api-Revision'] = INTERACTIONS_API_REVISION;
    }

    setGeminiStatus({ state: 'loading', message: '' });
    setInFlight((n) => n + 1);
    try {
      const result = await apiCallApi.request(
        {
          authIndex: resolvedAuthIndex,
          proxy_url: proxyUrl?.trim() || undefined,
          method: 'POST',
          url: endpoint,
          header: headerObj,
          data: JSON.stringify(
            brand === 'interactions'
              ? buildInteractionsProbePayload(model)
              : {
                  contents: [{ parts: [{ text: 'Hi' }] }],
                  generationConfig: { maxOutputTokens: 8 },
                }
          ),
        },
        { timeout: DEFAULT_TIMEOUT_MS }
      );
      if (result.statusCode < 200 || result.statusCode >= 300) {
        throw new Error(getApiCallErrorMessage(result));
      }
      setGeminiStatus({ state: 'success', message: '' });
    } catch (err) {
      setGeminiStatus({
        state: 'error',
        message: requestFailureMessage(err, messages),
      });
    } finally {
      setInFlight((n) => n - 1);
    }
  }, [
    apiKey,
    authIndex,
    baseUrl,
    brand,
    fallbackApiKey,
    formHeaders,
    messages,
    models,
    testModel,
    proxyUrl,
  ]);

  const runClaude = useCallback(async (): Promise<void> => {
    if (brand !== 'claude') return;

    const endpoint = buildClaudeMessagesEndpoint(baseUrl ?? '');
    if (!endpoint) {
      setClaudeStatus({ state: 'error', message: messages.endpointInvalid });
      return;
    }
    const model = pickModel(testModel, models);
    if (!model) {
      setClaudeStatus({ state: 'error', message: messages.modelRequired });
      return;
    }

    const customHeaders = buildHeaderObject(formHeaders);
    const explicitKey = (apiKey ?? '').trim();
    const persistedKey = (fallbackApiKey ?? '').trim();
    const headerKey = resolveBearerToken(customHeaders);
    const hasApiKeyHeader = hasHeader(customHeaders, 'x-api-key');
    const resolvedKey = explicitKey || persistedKey || headerKey;
    const resolvedAuthIndex = (authIndex ?? '').trim() || undefined;

    if (!resolvedKey && !hasApiKeyHeader && !resolvedAuthIndex) {
      setClaudeStatus({ state: 'error', message: messages.apiKeyRequired });
      return;
    }

    const headerObj: Record<string, string> = {
      'Content-Type': 'application/json',
      ...customHeaders,
    };
    if (!hasHeader(headerObj, 'anthropic-version')) {
      headerObj['anthropic-version'] = DEFAULT_ANTHROPIC_VERSION;
    }
    if (!hasApiKeyHeader && resolvedKey) {
      headerObj['x-api-key'] = resolvedKey;
    } else if (!hasApiKeyHeader && resolvedAuthIndex) {
      headerObj['x-api-key'] = '$TOKEN$';
    }

    setClaudeStatus({ state: 'loading', message: '' });
    setInFlight((n) => n + 1);
    try {
      const result = await apiCallApi.request(
        {
          authIndex: resolvedAuthIndex,
          proxy_url: proxyUrl?.trim() || undefined,
          method: 'POST',
          url: endpoint,
          header: headerObj,
          data: JSON.stringify({
            model,
            max_tokens: 8,
            messages: [{ role: 'user', content: 'Hi' }],
          }),
        },
        { timeout: DEFAULT_TIMEOUT_MS }
      );
      if (result.statusCode < 200 || result.statusCode >= 300) {
        throw new Error(getApiCallErrorMessage(result));
      }
      setClaudeStatus({ state: 'success', message: '' });
    } catch (err) {
      setClaudeStatus({
        state: 'error',
        message: requestFailureMessage(err, messages),
      });
    } finally {
      setInFlight((n) => n - 1);
    }
  }, [
    apiKey,
    authIndex,
    baseUrl,
    brand,
    fallbackApiKey,
    formHeaders,
    messages,
    models,
    testModel,
    proxyUrl,
  ]);

  return {
    openaiStatuses,
    codexStatus,
    geminiStatus,
    claudeStatus,
    isTestingAny: inFlight > 0,
    runOpenAIKey,
    runOpenAIAllKeys,
    runCodex,
    runGemini,
    runClaude,
  };
}
