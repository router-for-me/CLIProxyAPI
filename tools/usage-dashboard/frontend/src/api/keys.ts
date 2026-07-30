import type { SummaryFilters } from "./filters";

export const qk = {
  summary: (f: SummaryFilters) => ["summary", f] as const,
  timeseries: (f: SummaryFilters & { group_by?: string }) => ["timeseries", f] as const,
  models: (f: SummaryFilters) => ["models", f] as const,
  accounts: (f: SummaryFilters) => ["accounts", f] as const,
  requests: (f: SummaryFilters & { cursor?: string; limit?: number }) => ["requests", f] as const,
  errors: (f: SummaryFilters) => ["errors", f] as const,
  providers: (f: SummaryFilters) => ["providers", f] as const,
  endpoints: (f: SummaryFilters) => ["endpoints", f] as const,
  prices: () => ["prices"] as const,
  aliases: () => ["aliases"] as const,
  health: () => ["health"] as const,
};
