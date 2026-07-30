import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";

export function usePrices(token?: string) {
  return useQuery({
    queryKey: qk.prices(),
    queryFn: () => apiGet("/api/v1/prices", undefined, token),
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
}