import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";
import type { SummaryFilters } from "../filters";

export function useProviders(filters: SummaryFilters, token?: string) {
  return useQuery({
    queryKey: qk.providers(filters),
    queryFn: () => apiGet("/api/v1/providers", filters, token),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}