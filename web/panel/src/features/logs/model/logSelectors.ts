import { parseLogLine } from '@/features/logs/model/logParsing';
import {
  resolveStatusGroup,
  type ParsedLogLine,
  type HttpMethod,
  type StatusGroup,
} from '@/features/logs/model/logTypes';
import { MANAGEMENT_API_PREFIX } from '@/utils/constants';
import type { LogBuffer } from './logBuffer';

export type LogEntry = ParsedLogLine & { id: number };

/** Cache only the current buffer. Repeated text remains separate records. */
export function createLogParserCache() {
  let cache = new Map<number, LogEntry>();
  return (buffer: LogBuffer): LogEntry[] => {
    const next = new Map<number, LogEntry>();
    const entries = buffer.buffer.map((raw, index) => {
      const id = buffer.bufferStart + index;
      const previous = cache.get(id);
      const entry = previous?.raw === raw ? previous : { ...parseLogLine(raw), id };
      next.set(id, entry);
      return entry;
    });
    cache = next;
    return entries;
  };
}

export function searchLogEntries(
  entries: LogEntry[],
  query: string,
  hideManagement: boolean
): LogEntry[] {
  const needle = query.trim().toLowerCase();
  return entries.filter((entry) => {
    const path = entry.path?.split('?')[0];
    if (
      hideManagement &&
      path &&
      (path === MANAGEMENT_API_PREFIX || path.startsWith(`${MANAGEMENT_API_PREFIX}/`))
    )
      return false;
    return !needle || entry.raw.toLowerCase().includes(needle);
  });
}

export function filterLogEntries(
  entries: LogEntry[],
  filters: {
    methods: Set<HttpMethod>;
    statuses: Set<StatusGroup>;
    paths: Set<string>;
    level: string;
  }
): LogEntry[] {
  return entries.filter((entry) => {
    if (filters.level && entry.level !== filters.level) return false;
    if (filters.methods.size && (!entry.method || !filters.methods.has(entry.method))) return false;
    const group = resolveStatusGroup(entry.statusCode);
    if (filters.statuses.size && (!group || !filters.statuses.has(group))) return false;
    return !filters.paths.size || Boolean(entry.path && filters.paths.has(entry.path));
  });
}
