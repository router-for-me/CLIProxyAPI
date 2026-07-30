import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";
import type { SummaryFilters } from "../filters";

export function useModels(filters: SummaryFilters, token?: string) {
  return useQuery({
    queryKey: qk.models(filters),
    queryFn: () => apiGet("/api/v1/models", filters, token),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}