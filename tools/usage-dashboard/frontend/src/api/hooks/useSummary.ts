import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";
import type { SummaryFilters } from "../filters";

export function useSummary(filters: SummaryFilters, token?: string) {
  return useQuery({
    queryKey: qk.summary(filters),
    queryFn: () => apiGet("/api/v1/summary", filters, token),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}