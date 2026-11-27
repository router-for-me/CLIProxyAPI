import { isRecord } from '@/utils/helpers';
import type { ApiCallResult } from './apiCall';

export class AntigravityValidationError extends Error {
  readonly status = 403;

  constructor(
    message: string,
    readonly url?: string
  ) {
    super(message);
  }
}

export function googleAccountsUrl(value: unknown): string | undefined {
  if (typeof value !== 'string') return undefined;
  try {
    const url = new URL(value);
    if (
      url.protocol === 'https:' &&
      url.host === 'accounts.google.com' &&
      !url.username &&
      !url.password
    ) {
      return url.href;
    }
  } catch {
    // Never expose malformed or untrusted links as recovery actions.
  }
  return undefined;
}

export function getAntigravityValidationError(
  { statusCode, body }: ApiCallResult,
  fallbackMessage: string
): AntigravityValidationError | undefined {
  if (statusCode !== 403 || !isRecord(body) || !isRecord(body.error)) return;
  const { details, message } = body.error;
  if (!Array.isArray(details)) return;
  let required = false;
  let url: string | undefined;
  for (const detail of details) {
    if (!isRecord(detail)) continue;
    if (detail.reason === 'VALIDATION_REQUIRED') required = true;
    if (isRecord(detail.metadata)) {
      url ??=
        googleAccountsUrl(detail.metadata.validation_url) ??
        googleAccountsUrl(detail.metadata.validationUrl);
    }
  }
  if (required || url) {
    return new AntigravityValidationError(
      typeof message === 'string' && message.trim() ? message.trim() : fallbackMessage,
      url
    );
  }
}
