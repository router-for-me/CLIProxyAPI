import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";

export function useAliases(token?: string) {
  return useQuery({
    queryKey: qk.aliases(),
    queryFn: () => apiGet("/api/v1/aliases", undefined, token),
    staleTime: 60_000,
    refetchInterval: 60_000,
  });
}