import type { AuthFileItem, MetaQuotaData } from '@/types';
import type { ApiCallRequest, ApiCallResult } from '@/services/api/apiCall';
import { parseMetaQuotaPayload } from '@/services/api/metaQuota';
import { normalizeAuthIndex } from '@/utils/authIndex';

export const META_MUSE_QUOTA_URL = 'https://api.meta.ai/muse-code/key';

export type MetaQuotaErrorCode =
  | 'missing_auth_index'
  | 'missing_file'
  | 'missing_dca_token'
  | 'invalid_auth_file'
  | 'download_failed'
  | 'stale_request'
  | 'invalid_response'
  | 'request_failed';

export class MetaQuotaError extends Error {
  readonly status?: number;

  constructor(
    public readonly code: MetaQuotaErrorCode,
    status?: number
  ) {
    super(code);
    this.name = 'MetaQuotaError';
    this.status = status;
  }
}

interface MetaQuotaDependencies {
  request: (payload: ApiCallRequest) => Promise<ApiCallResult>;
  downloadText: (name: string) => Promise<string>;
  captureCurrent: (name: string) => () => boolean;
}

/** Extract only the backend's persisted DCA field; never fall back to the LLM key. */
const readDcaToken = (text: string): string => {
  let value: unknown;
  try {
    value = JSON.parse(text);
  } catch {
    throw new MetaQuotaError('invalid_auth_file');
  }
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    throw new MetaQuotaError('invalid_auth_file');
  }
  const raw = (value as Record<string, unknown>).dca_token;
  const token = typeof raw === 'string' ? raw.trim() : '';
  if (!/^dca:[^\s]+$/.test(token)) throw new MetaQuotaError('missing_dca_token');
  return token;
};

/** Secrets remain request-local, never in quota state, errors, or browser storage. */
export function createMetaQuotaFetcher(deps: MetaQuotaDependencies) {
  return async (file: AuthFileItem): Promise<MetaQuotaData> => {
    const authIndex = normalizeAuthIndex(file.authIndex ?? file.auth_index);
    if (!authIndex) throw new MetaQuotaError('missing_auth_index');
    if (
      !file.name?.trim() ||
      file.runtimeOnly === true ||
      file.runtime_only === true ||
      file.runtime_only === 'true'
    ) {
      throw new MetaQuotaError('missing_file');
    }
    const isCurrent = deps.captureCurrent(file.name);
    const assertCurrent = () => {
      if (!isCurrent()) throw new MetaQuotaError('stale_request');
    };
    assertCurrent();
    let authText: string;
    try {
      authText = await deps.downloadText(file.name);
    } catch {
      assertCurrent();
      throw new MetaQuotaError('download_failed');
    }
    assertCurrent();
    const dcaToken = readDcaToken(authText);

    let response: ApiCallResult;
    try {
      assertCurrent();
      response = await deps.request({
        authIndex,
        method: 'POST',
        url: META_MUSE_QUOTA_URL,
        header: {
          Accept: 'application/json',
          'Content-Type': 'application/json',
          Authorization: `Bearer ${dcaToken}`,
          'x-api-version': '1.0.0',
        },
        data: '{}',
      });
    } catch (error: unknown) {
      assertCurrent();
      const status =
        error !== null &&
        typeof error === 'object' &&
        typeof (error as { status?: unknown }).status === 'number'
          ? (error as { status: number }).status
          : undefined;
      throw new MetaQuotaError('request_failed', status);
    }
    assertCurrent();

    // Never derive an error message from body/bodyText: the endpoint can echo
    // api_key and other PII even on errors.
    if (!(response.statusCode >= 200 && response.statusCode < 300)) {
      throw new MetaQuotaError('request_failed', response.statusCode);
    }

    const quota = parseMetaQuotaPayload(response.body ?? response.bodyText);
    if (!quota) throw new MetaQuotaError('invalid_response');
    return quota;
  };
}
