import { useMemo } from 'react';
import { useLocalStorage } from '@/hooks/useLocalStorage';
import type { HttpMethod, ParsedLogLine, StatusGroup } from '../model/logTypes';
import { resolveStatusGroup } from '../model/logTypes';

const PATH_FILTER_LIMIT = 12;

interface UseLogFiltersOptions {
  parsedLines: ParsedLogLine[];
}

interface UseLogFiltersReturn {
  methodFilters: HttpMethod[];
  statusFilters: StatusGroup[];
  pathFilters: string[];
  methodFilterSet: Set<HttpMethod>;
  statusFilterSet: Set<StatusGroup>;
  pathFilterSet: Set<string>;
  hasStructuredFilters: boolean;
  methodCounts: Partial<Record<HttpMethod, number>>;
  statusCounts: Partial<Record<StatusGroup, number>>;
  pathOptions: Array<{ path: string; count: number }>;
  toggleMethodFilter: (method: HttpMethod) => void;
  toggleStatusFilter: (group: StatusGroup) => void;
  togglePathFilter: (path: string) => void;
  clearStructuredFilters: () => void;
}

export function useLogFilters(options: UseLogFiltersOptions): UseLogFiltersReturn {
  const { parsedLines } = options;

  const [methodFilters, setMethodFilters] = useLocalStorage<HttpMethod[]>(
    'logsPage.methodFilters',
    []
  );
  const [statusFilters, setStatusFilters] = useLocalStorage<StatusGroup[]>(
    'logsPage.statusFilters',
    []
  );
  const [storedPathFilters, setPathFilters] = useLocalStorage<string[]>('logsPage.pathFilters', []);
  const pathFilters = useMemo(
    () => normalizeLogPathFilters(storedPathFilters),
    [storedPathFilters]
  );

  const methodFilterSet = useMemo(() => new Set(methodFilters), [methodFilters]);
  const statusFilterSet = useMemo(() => new Set(statusFilters), [statusFilters]);
  const pathFilterSet = useMemo(() => new Set(pathFilters), [pathFilters]);
  const hasStructuredFilters =
    methodFilters.length > 0 || statusFilters.length > 0 || pathFilters.length > 0;

  const methodCounts = useMemo(() => {
    const counts: Partial<Record<HttpMethod, number>> = {};
    parsedLines.forEach((line) => {
      if (!line.method) return;
      counts[line.method] = (counts[line.method] ?? 0) + 1;
    });
    return counts;
  }, [parsedLines]);

  const statusCounts = useMemo(() => {
    const counts: Partial<Record<StatusGroup, number>> = {};
    parsedLines.forEach((line) => {
      const statusGroup = resolveStatusGroup(line.statusCode);
      if (!statusGroup) return;
      counts[statusGroup] = (counts[statusGroup] ?? 0) + 1;
    });
    return counts;
  }, [parsedLines]);

  const pathOptions = useMemo(
    () => buildLogPathOptions(parsedLines, pathFilters),
    [parsedLines, pathFilters]
  );

  const toggleMethodFilter = (method: HttpMethod) => {
    setMethodFilters((prev) =>
      prev.includes(method) ? prev.filter((item) => item !== method) : [...prev, method]
    );
  };

  const toggleStatusFilter = (group: StatusGroup) => {
    setStatusFilters((prev) =>
      prev.includes(group) ? prev.filter((item) => item !== group) : [...prev, group]
    );
  };

  const togglePathFilter = (path: string) => {
    setPathFilters((stored) => {
      const prev = normalizeLogPathFilters(stored);
      return prev.includes(path) ? prev.filter((item) => item !== path) : [...prev, path];
    });
  };

  const clearStructuredFilters = () => {
    setMethodFilters([]);
    setStatusFilters([]);
    setPathFilters([]);
  };

  return {
    methodFilters,
    statusFilters,
    pathFilters,
    methodFilterSet,
    statusFilterSet,
    pathFilterSet,
    hasStructuredFilters,
    methodCounts,
    statusCounts,
    pathOptions,
    toggleMethodFilter,
    toggleStatusFilter,
    togglePathFilter,
    clearStructuredFilters,
  };
}

// Earlier UI versions persisted the formatter's surrounding path quotes.
export function normalizeLogPathFilters(paths: string[]): string[] {
  return [
    ...new Set(
      paths.map((path) => (path.startsWith('"') && path.endsWith('"') ? path.slice(1, -1) : path))
    ),
  ];
}

// Selected paths remain removable even after they leave the current log window.
export function buildLogPathOptions(parsedLines: ParsedLogLine[], selectedPaths: string[]) {
  const counts = new Map<string, number>();
  parsedLines.forEach((line) => {
    if (line.path) counts.set(line.path, (counts.get(line.path) ?? 0) + 1);
  });
  const popularPaths = Array.from(counts.entries())
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .slice(0, PATH_FILTER_LIMIT)
    .map(([path]) => path);
  return Array.from(new Set([...popularPaths, ...selectedPaths])).map((path) => ({
    path,
    count: counts.get(path) ?? 0,
  }));
}
