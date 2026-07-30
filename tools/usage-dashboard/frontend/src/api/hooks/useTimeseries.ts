import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";
import type { SummaryFilters } from "../filters";

export function useTimeseries(filters: SummaryFilters & { group_by?: string }, token?: string) {
  return useQuery({
    queryKey: qk.timeseries(filters),
    queryFn: () => apiGet("/api/v1/timeseries", filters, token),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}