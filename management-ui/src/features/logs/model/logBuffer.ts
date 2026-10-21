import type { LogsQuery, LogsResponse } from '@/services/api/logs';

export const LOG_PAGE_SIZE = 10000;
export const MAX_LOG_TEXT_UNITS = 8 * 1024 * 1024;
export const INITIAL_VISIBLE_LINES = 100;

export interface LogBuffer {
  buffer: string[];
  bufferStart: number;
  nextId: number;
  textUnits: number;
  evicted: number;
  cursor?: string;
}

export const emptyLogBuffer = (): LogBuffer => ({
  buffer: [],
  bufferStart: 0,
  nextId: 0,
  textUnits: 0,
  evicted: 0,
});

export function buildLogsQuery(cursor?: string): LogsQuery {
  return cursor ? { limit: LOG_PAGE_SIZE, cursor } : { limit: LOG_PAGE_SIZE };
}

/** Cursor pages contain new records, including legitimate identical adjacent lines. */
export function applyLogPage(
  current: LogBuffer,
  response: LogsResponse,
  requestedCursor?: string,
  maxLines = LOG_PAGE_SIZE,
  maxTextUnits = MAX_LOG_TEXT_UNITS
): LogBuffer {
  const append = Boolean(requestedCursor) && !response.cursorReset;
  const incoming = response.lines;
  const buffer = append ? [...current.buffer, ...incoming] : [...incoming];
  let textUnits =
    (append ? current.textUnits : 0) + incoming.reduce((n, line) => n + line.length + 1, 0);
  let drop = 0;
  // Keep one oversized line intact; never silently truncate its contents.
  while (
    buffer.length - drop > 1 &&
    (buffer.length - drop > maxLines || textUnits > maxTextUnits)
  ) {
    textUnits -= buffer[drop].length + 1;
    drop++;
  }
  const start = append ? current.bufferStart : current.nextId;
  return {
    buffer: buffer.slice(drop),
    bufferStart: start + drop,
    nextId: current.nextId + incoming.length,
    textUnits,
    evicted: (append ? current.evicted : 0) + drop,
    cursor: response.nextCursor,
  };
}

export function shouldCatchUp(response: LogsResponse, requestedCursor?: string): boolean {
  return Boolean(
    requestedCursor &&
    response.nextCursor &&
    response.nextCursor !== requestedCursor &&
    !response.cursorReset &&
    response.lines.length >= LOG_PAGE_SIZE
  );
}
