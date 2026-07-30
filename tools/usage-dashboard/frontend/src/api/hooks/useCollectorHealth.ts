import { useQuery } from "@tanstack/react-query";
import { apiGet } from "../client";
import { qk } from "../keys";

export type Health = {
  last_poll_at?: string;
  last_poll_ok?: boolean;
  last_poll_error?: string;
  management_configured?: boolean;
};

export function useCollectorHealth(token?: string) {
  return useQuery({
    queryKey: qk.health(),
    queryFn: () => apiGet("/api/v1/health", undefined, token) as Promise<Health>,
    staleTime: 30_000,
    refetchInterval: 30_000,
  });
}