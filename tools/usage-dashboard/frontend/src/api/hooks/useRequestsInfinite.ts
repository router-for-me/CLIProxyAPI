import { useInfiniteQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { useFilterKey } from "@/stores/filtersStore";

export interface RequestRow {
  timestamp: string;
  local_time: string;
  account: string;
  model: string;
  provider: string;
  endpoint: string;
  failed: number;
  fail_status: number | null;
  latency_ms: number;
  ttft_ms: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cached_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  request_id: string;
  estimated_cost?: number;
}

export interface RequestsResponse {
  requests: RequestRow[];
  next_cursor: string | null;
  limit: number;
  models_filter: string[];
  accounts_filter: string[];
}

export function useRequestsInfinite(limit = 50) {
  const filters = useFilterKey();
  return useInfiniteQuery<RequestsResponse>({
    queryKey: ["requests-infinite", filters, limit],
    queryFn: ({ pageParam }) =>
      apiGet("/api/v1/requests", {
        ...filters,
        limit,
        cursor: (pageParam as string) ?? undefined,
      }) as Promise<RequestsResponse>,
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.next_cursor ?? undefined,
    // Auto-refresh so newly-collected requests appear without a manual reload.
    // Matches the summary hook cadence (30s). Pages refetch in place, so any
    // infinite-scroll position the user has loaded below is preserved.
    refetchInterval: 30_000,
  });
}