import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";
import type { SummaryFilters } from "../filters";

export function useRequests<TData = unknown>(
  filters: SummaryFilters & { cursor?: string; limit?: number },
  token?: string,
) {
  return useQuery<TData>({
    queryKey: qk.requests(filters),
    queryFn: () => apiGet("/api/v1/requests", filters, token) as Promise<TData>,
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}
