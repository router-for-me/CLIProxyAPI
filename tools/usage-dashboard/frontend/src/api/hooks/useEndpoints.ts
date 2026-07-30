import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";
import type { SummaryFilters } from "../filters";

export function useEndpoints(filters: SummaryFilters, token?: string) {
  return useQuery({
    queryKey: qk.endpoints(filters),
    queryFn: () => apiGet("/api/v1/endpoints", filters, token),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}