import { describe, expect, spyOn, test } from 'bun:test';
import { AxiosError, AxiosHeaders, type AxiosInstance } from 'axios';
import { apiClient } from '@/services/api/client';
import { logsApi, responseDataToText } from '@/services/api/logs';
import type { ApiError } from '@/types';
import { LOGS_TIMEOUT_MS } from '@/utils/constants';

describe('logs domain response normalization', () => {
  test('normalizes lines and timestamps without altering opaque cursors', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({
      lines: [' first ', null, 2, ''],
      'latest-timestamp': '2026-01-01T00:00:00Z',
      'next-cursor': ' opaque cursor ',
      'cursor-reset': 'TRUE',
    });
    try {
      expect(await logsApi.fetchLogs()).toEqual({
        lines: [' first ', ''],
        latestAfter: 1767225600,
        nextCursor: ' opaque cursor ',
        cursorReset: true,
      });
      get.mockResolvedValue({ lines: null, 'latest-timestamp': 'invalid' });
      expect(await logsApi.fetchLogs()).toEqual({
        lines: [],
        latestAfter: undefined,
        nextCursor: undefined,
        cursorReset: false,
      });
      get.mockResolvedValue(null);
      expect(await logsApi.fetchLogs()).toEqual({ lines: [] });
    } finally {
      get.mockRestore();
    }
  });

  test('preserves explicit empty cursor on reset and forwards cursor queries and cancellation', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({
      lines: [],
      'latest-timestamp': 0,
      'next-cursor': '',
      'cursor-reset': true,
    });
    try {
      const signal = new AbortController().signal;
      const params = { cursor: 'previous', after: 123, limit: 100 };
      expect(await logsApi.fetchLogs(params, { signal })).toEqual({
        lines: [],
        latestAfter: undefined,
        nextCursor: '',
        cursorReset: true,
      });
      expect(get).toHaveBeenCalledWith('/observability/logs', {
        params,
        signal,
        timeout: LOGS_TIMEOUT_MS,
      });
    } finally {
      get.mockRestore();
    }
  });

  test('normalizes malformed error-file lists without changing filenames', async () => {
    const get = spyOn(apiClient, 'get').mockResolvedValue({
      files: [
        null,
        {},
        { name: '' },
        { name: 123 },
        { name: ' file.log ', size: 0, modified: '123' },
        { name: 'bad.log', size: -1, modified: 'invalid' },
      ],
    });
    try {
      expect(await logsApi.fetchErrorLogs()).toEqual({
        files: [
          { name: ' file.log ', size: 0, modified: 123 },
          { name: 'bad.log', size: undefined, modified: undefined },
        ],
      });
      for (const data of [null, {}, { files: 'invalid' }]) {
        get.mockResolvedValue(data);
        expect(await logsApi.fetchErrorLogs()).toEqual({ files: [] });
      }
    } finally {
      get.mockRestore();
    }
  });
});

describe('log downloads', () => {
  test('preserves raw successful downloads, encoded paths, timeout and signal', async () => {
    const response = { data: new Blob(['{"error":"this is log content"}']) };
    const raw = spyOn(apiClient, 'getRaw').mockResolvedValue(response as never);
    try {
      const signal = new AbortController().signal;
      expect(await logsApi.downloadErrorLog('a/b #.log', { signal })).toBe(response);
      expect(raw).toHaveBeenLastCalledWith('/observability/logs/errors/a%2Fb%20%23.log', {
        signal,
        responseType: 'blob',
        timeout: LOGS_TIMEOUT_MS,
      });
      expect(await logsApi.downloadRequestLogById('a/b', { signal })).toBe(response);
      expect(raw).toHaveBeenLastCalledWith('/observability/logs/requests/a%2Fb', {
        signal,
        responseType: 'blob',
        timeout: LOGS_TIMEOUT_MS,
      });
    } finally {
      raw.mockRestore();
    }
  });

  test('decodes Blob JSON from the actual client interceptor and emits unauthorized exactly once', async () => {
    const instance = (apiClient as unknown as { instance: AxiosInstance }).instance;
    const adapter = instance.defaults.adapter;
    const windowDescriptor = Object.getOwnPropertyDescriptor(globalThis, 'window');
    const events: string[] = [];
    Object.defineProperty(globalThis, 'window', {
      configurable: true,
      value: { dispatchEvent: (event: Event) => events.push(event.type) },
    });
    const body = new Blob([JSON.stringify({ error: 'unauthorized', message: 'Key rejected' })], {
      type: 'application/json',
    });
    instance.defaults.adapter = async (config) => {
      throw new AxiosError(
        'Request failed with status code 401',
        'ERR_BAD_REQUEST',
        config,
        undefined,
        { data: body, status: 401, statusText: 'Unauthorized', headers: new AxiosHeaders(), config }
      );
    };
    try {
      const error = await logsApi.downloadErrorLog('error.log').catch((err: ApiError) => err);
      expect(error).toMatchObject({
        name: 'ApiError',
        message: 'Key rejected',
        status: 401,
        code: 'ERR_BAD_REQUEST',
        apiCode: 'unauthorized',
        data: body,
        details: body,
      });
      expect('response' in error).toBe(false);
      expect(events).toEqual(['unauthorized']);
    } finally {
      instance.defaults.adapter = adapter;
      if (windowDescriptor) Object.defineProperty(globalThis, 'window', windowDescriptor);
      else Reflect.deleteProperty(globalThis, 'window');
    }
  });

  test('preserves error identity/status and supports details-only nested JSON errors', async () => {
    const error = Object.assign(new Error('transport fallback'), {
      status: 404,
      code: 'ERR_BAD_REQUEST',
      details: new Blob(['{"error":{"code":"not_found","message":"Log missing"}}']),
    });
    const raw = spyOn(apiClient, 'getRaw').mockRejectedValue(error);
    try {
      await expect(logsApi.downloadRequestLogById('missing')).rejects.toBe(error);
      expect(error).toMatchObject({
        message: 'Log missing',
        apiCode: 'not_found',
        status: 404,
        code: 'ERR_BAD_REQUEST',
      });
    } finally {
      raw.mockRestore();
    }
  });

  test('does not mask cancellation, network errors or unreadable/non-JSON failure bodies', async () => {
    const unreadable = new Blob(['ignored']);
    unreadable.text = async () => {
      throw new Error('read failed');
    };
    const raw = spyOn(apiClient, 'getRaw');
    try {
      for (const data of [
        undefined,
        new Blob(['<html>Bad gateway</html>']),
        new Blob(['']),
        unreadable,
      ]) {
        const error = Object.assign(new Error('original'), { status: 502, data });
        raw.mockRejectedValue(error);
        await expect(logsApi.downloadErrorLog('x')).rejects.toBe(error);
        expect(error.message).toBe('original');
      }
      const canceled = Object.assign(new Error('canceled'), { code: 'ERR_CANCELED' });
      raw.mockRejectedValue(canceled);
      await expect(logsApi.downloadErrorLog('x')).rejects.toBe(canceled);
    } finally {
      raw.mockRestore();
    }
  });

  test('decodes reusable text, Blob and binary bodies', async () => {
    const bytes = new TextEncoder().encode('日志');
    for (const body of ['日志', new Blob([bytes]), bytes.buffer, bytes]) {
      expect(await responseDataToText(body)).toBe('日志');
    }
    expect(await responseDataToText(null)).toBe('');
    expect(await responseDataToText(undefined)).toBe('');
    expect(await responseDataToText({ message: 'ok' })).toBe('{\n  "message": "ok"\n}');
  });
});
