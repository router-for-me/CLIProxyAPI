// Shapes returned by /prox/api/*.
// Percentages: `used_percent` is utilization and `remaining_percent` is capacity
// left, both 0–100, computed server-side so the UI never inverts a value.

export type Provider = "claude" | "codex";

export interface QuotaWindow {
  key: string;
  kind: "five_hour" | "weekly" | "monthly" | "model_weekly";
  family?: string;
  label: string;
  used_percent: number;
  remaining_percent: number;
  resets_at?: string;
  rejected?: boolean;
  source: "poll" | "passive";
  observed_at: string;
  stale: boolean;
  reset_passed: boolean;
}

export interface BurnEstimate {
  status: "ok" | "insufficient_data" | "idle";
  rate_percent_per_hour?: number;
  depletes_at?: string;
  depletes_before_reset: boolean;
  unused_at_reset_percent?: number;
  samples: number;
  span_minutes: number;
}

export interface Health {
  auth_failed: boolean;
  auth_suspect: boolean;
  rate_limited_until?: string;
  circuit_open_until?: string;
  blocked_models?: string[];
  last_success_at?: string;
  last_failure_at?: string;
  last_failure_status?: number;
}

export interface Account {
  id: string;
  display: string;
  alias?: string;
  provider: Provider;
  plan?: string;
  host_status?: string;
  host_message?: string;
  present: boolean;
  disabled: boolean;
  unavailable: boolean;
  cooldown_until?: string;
  cooldown_stale: boolean;
  host_priority: number;
  plugin_priority: number;
  polled: boolean;
  poll_error?: string;
  last_poll_at?: string;
  last_refresh_at?: string;
  last_observed_at?: string;
  next_poll_at?: string;
  windows: QuotaWindow[] | null;
  quota_state: "ready" | "exhausted" | "reset_pending" | "stale" | "unknown";
  quota_reason: string;
  routing: "eligible" | "limited" | "blocked" | "removed";
  routing_reason?: string;
  next_reset?: string;
  remaining_percent: number;
  expiring_soon: boolean;
  active_sessions: number;
  preferred: boolean;
  rank: number;
  health: Health;
  burn: Record<string, BurnEstimate>;
}

export interface Ranked {
  auth_id: string;
  score: number;
  reason: string;
  quota_state: string;
}

export interface Skip {
  auth_id: string;
  reason: string;
}

export interface NextUp {
  provider: Provider;
  model: string;
  policy: string;
  auth_id?: string;
  account?: string;
  reason: string;
  fallback?: boolean;
  ranked: Ranked[] | null;
  skipped: Skip[] | null;
}

export interface Counters {
  picks: number;
  routed: number;
  delegated: number;
  affinity: number;
  new_session: number;
  failovers: number;
  rebinds: number;
  inherited: number;
  observed: number;
}

export interface ProviderSummary {
  total: number;
  eligible: number;
  limited: number;
  exhausted: number;
  blocked: number;
  unknown: number;
  active_sessions: number;
  expiring_soon: number;
}

export interface PluginInfo {
  version: string;
  started_at: string;
  worker_running: boolean;
  worker_failed: boolean;
  state_writable: boolean;
  state_reason?: string;
  state_load: string;
  last_save_error?: string;
  last_saved_at?: string;
  config_generation: string;
  config_error?: string;
  schedule_across_priorities: boolean;
  recovered_panics: number;
}

export interface Overview {
  now: string;
  plugin: PluginInfo;
  mode: "shadow" | "active";
  policy: string;
  roster: { ok: boolean; error: string; at: string };
  counters: Counters;
  summary: Record<Provider, ProviderSummary>;
  upcoming: { auth_id: string; account: string; provider: Provider; resets_at: string; remaining_percent: number }[] | null;
  last_refresh?: string | null;
  next_up: NextUp[];
  accounts: Account[];
  affinity: { enabled: boolean; ttl: string; scope: string; inherit_parent: boolean; rebind_policy: string };
}

export interface SessionRow {
  session: string;
  parent?: string;
  client?: string;
  kind: string;
  provider: Provider;
  model: string;
  auth_id: string;
  account: string;
  actual_auth_id?: string;
  origin: string;
  applied: boolean;
  created_at: string;
  last_seen: string;
  expires_at: string;
  requests: number;
  failovers: number;
  last_failover_at?: string;
  rebinds: number;
  last_rebind_at?: string;
  last_rebind_cause?: string;
  previous_auth_id?: string;
  account_usable: boolean;
}

export interface Decision {
  id: number;
  at: string;
  provider: Provider;
  model: string;
  stream: boolean;
  mode: string;
  policy: string;
  session?: string;
  session_kind?: string;
  client?: string;
  outcome: string;
  cause?: string;
  auth_id?: string;
  bound_auth_id?: string;
  applied: boolean;
  affinity_overrode_ranking: boolean;
  fallback?: boolean;
  ranked: Ranked[] | null;
  skipped: Skip[] | null;
  result?: { at: string; auth_id: string; status: number; failed: boolean };
}

export interface Counts {
  requests: number;
  failures: number;
  rate_limited: number;
  auth_failures: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  failovers: number;
  rebinds: number;
  affinity_hits: number;
  new_sessions: number;
  delegated: number;
}

export interface Point extends Counts {
  at: string;
}

export interface Usage {
  now: string;
  range: string;
  step: string;
  providers: Record<Provider, { series: Point[]; totals: Counts; cache_read_share: number | null; cache_share_basis: string }>;
  accounts: Record<string, Counts>;
  labels: Record<string, string>;
}

export interface SchemaField {
  name: string;
  type: "enum" | "boolean" | "integer" | "number" | "duration" | "string";
  enum?: string[];
  min?: string;
  max?: string;
  default: unknown;
  group: string;
  description: string;
  effect: string;
  restart?: boolean;
}

export interface Settings {
  config: Record<string, unknown>;
  schema: SchemaField[];
  generation: string;
  patch_path: string;
}

export interface Change {
  field: string;
  from: unknown;
  to: unknown;
  effect: string;
}
