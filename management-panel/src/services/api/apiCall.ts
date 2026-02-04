/**
 * Generic API call helper (proxied via management API).
 */

import type { AxiosRequestConfig } from 'axios';
import { apiClient } from './client';

export interface ApiCallRequest {
  authIndex?: string;
  method: string;
  url: string;
  header?: Record<string, string>;
  data?: string;
}

export interface ApiCallResult<T = any> {
  statusCode: number;
  header: Record<string, string[]>;
  bodyText: string;
  body: T | null;
}

const normalizeBody = (input: unknown): { bodyText: string; body: any | null } => {
  if (input === undefined || input === null) {
    return { bodyText: '', body: null };
  }

  if (typeof input === 'string') {
    const text = input;
    const trimmed = text.trim();
    if (!trimmed) {
      return { bodyText: text, body: null };
    }
    try {
      return { bodyText: text, body: JSON.parse(trimmed) };
    } catch {
      return { bodyText: text, body: text };
    }
  }

  try {
    return { bodyText: JSON.stringify(input), body: input };
  } catch {
    return { bodyText: String(input), body: input };
  }
};

export const getApiCallErrorMessage = (result: ApiCallResult): string => {
  const status = result.statusCode;
  const body = result.body;
  const bodyText = result.bodyText;
  let message = '';

  if (body && typeof body === 'object') {
    message = body?.error?.message || body?.error || body?.message || '';
  } else if (typeof body === 'string') {
    message = body;
  }

  if (!message && bodyText) {
    message = bodyText;
  }

  if (status && message) return `${status} ${message}`.trim();
  if (status) return `HTTP ${status}`;
  return message || 'Request failed';
};

export const apiCallApi = {
  request: async (
    payload: ApiCallRequest,
    config?: AxiosRequestConfig
  ): Promise<ApiCallResult> => {
    const response = await apiClient.post('/api-call', payload, config);
    const statusCode = Number(response?.status_code ?? response?.statusCode ?? 0);
    const header = (response?.header ?? response?.headers ?? {}) as Record<string, string[]>;
    const { bodyText, body } = normalizeBody(response?.body);

    return {
      statusCode,
      header,
      bodyText,
      body
    };
  }
};
