import { isRecord } from '@/utils/helpers';

export interface ParsedApiErrorResponse {
  message: string;
  apiCode?: string;
}

const readString = (value: unknown): string =>
  typeof value === 'string' ? value.trim() : '';

/**
 * Parse the Management API's error envelope.
 *
 * Newer endpoints use `error` as a stable machine-readable code and `message`
 * as the human-readable detail. Older endpoints may put the only useful text
 * directly in `error`, so that remains a fallback.
 */
export const parseApiErrorResponse = (
  responseData: unknown,
  fallbackMessage: string
): ParsedApiErrorResponse => {
  if (!isRecord(responseData)) {
    return {
      message: readString(responseData) || readString(fallbackMessage) || 'Request failed',
    };
  }

  const errorValue = responseData.error;
  const errorRecord = isRecord(errorValue) ? errorValue : null;
  const stringError = readString(errorValue);
  const apiCode = stringError || readString(errorRecord?.code) || undefined;
  const message =
    readString(responseData.message) ||
    readString(errorRecord?.message) ||
    stringError ||
    readString(fallbackMessage) ||
    'Request failed';

  return { message, apiCode };
};
