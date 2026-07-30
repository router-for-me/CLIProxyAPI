import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";
import type { SummaryFilters } from "../filters";

export function useAccounts(filters: SummaryFilters, token?: string) {
  return useQuery({
    queryKey: qk.accounts(filters),
    queryFn: () => apiGet("/api/v1/accounts", filters, token),
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}