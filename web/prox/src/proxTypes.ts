// Shapes returned by the prox-only routes under /prox/api.
import type { Account } from "./types";

export interface PoolWindow {
  kind: string;
  remaining_percent: number;
  min_remaining_percent: number;
  max_remaining_percent: number;
  accounts: number;
  next_reset?: string;
  exhausted: number;
}

export interface PoolProvider {
  provider: string;
  accounts: number;
  usable: number;
  total_weight: number;
  windows: PoolWindow[] | null;
  next_session_account?: string;
  cost_today_usd: number;
  cost_7d_usd: number;
  tokens_today: number;
  requests_today: number;
}

export interface Pool {
  now: string;
  providers: PoolProvider[];
}

export interface Tokens {
  input: number;
  output: number;
  reasoning: number;
  cache_read: number;
  cache_write: number;
}

export interface CostRow {
  start: string;
  provider: string;
  auth_id: string;
  model: string;
  client: string;
  requests: number;
  failures: number;
  tokens: Tokens;
  cost_usd: number;
  unpriced: number;
}

export interface Costs {
  now: string;
  rows: CostRow[];
  total: CostRow;
  cache_read_share: number | null;
}

export interface IgnitionPlan {
  auth_id: string;
  provider: string;
  display: string;
  status: string;
  reason?: string;
  next_at?: string;
  resets_at?: string;
  model?: string;
}

export interface IgnitionEvent {
  at: string;
  auth_id: string;
  provider: string;
  kind: string;
  model?: string;
  detail?: string;
}

export interface Ignition {
  now?: string;
  enabled?: boolean;
  plans: IgnitionPlan[];
  events: IgnitionEvent[];
}

export interface NotifyEntry {
  at: string;
  title: string;
  level: string;
  delivered: boolean;
  error?: string;
}

export interface Monitor {
  now: string;
  accounts: Account[];
}

export interface SinkStatus {
  enabled: boolean;
  ready: boolean;
  sent: number;
  dropped: number;
  queued: number;
  error?: string;
}

export const providerLabel = (p: string) =>
  ({ claude: "Claude", codex: "Codex", kimi: "Kimi", xai: "Grok" })[p] ?? p;

export function usd(v: number | undefined | null): string {
  if (v === undefined || v === null) return "—";
  if (v === 0) return "$0";
  if (v < 0.01) return "<$0.01";
  if (v < 100) return `$${v.toFixed(2)}`;
  if (v < 10000) return `$${Math.round(v).toLocaleString()}`;
  return `$${(v / 1000).toFixed(1)}k`;
}

export function totalTokens(t: Tokens): number {
  return t.input + t.output + t.cache_read + t.cache_write;
}
