/** Shapes returned by /prox/api. */

export interface ChartPoint {
  at: string;
  value: number;
}

export interface ChartEvent {
  at: string;
  gain: number;
  projected: boolean;
}

export interface ChartAccount {
  id: string;
  label: string;
  remaining_percent: number;
  resets_at?: string;
  rate_percent_per_hour: number;
  empty_at?: string;
}

export interface PoolChart {
  provider: string;
  window: string;
  now: string;
  capacity_percent: number;
  current_percent: number;
  accounts: ChartAccount[] | null;
  recorded: ChartPoint[] | null;
  projected: ChartPoint[] | null;
  events: ChartEvent[] | null;
  drain: ChartPoint[] | null;
  drain_peak_percent_per_hour: number;
  burn_last_3h_percent_per_hour: number;
  pool_empty_at?: string;
  horizon: string;
  first_empty?: { account: string; label: string; at: string };
  next_reset?: ChartEvent;
}

export interface PoolCharts {
  now: string;
  charts: PoolChart[];
}

export interface PoolWindow {
  kind: string;
  remaining_percent: number;
  accounts: number;
  next_reset?: string;
  exhausted: number;
}

export interface PoolProvider {
  provider: string;
  accounts: number;
  usable: number;
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
  rows: CostRow[] | null;
  total: CostRow;
  cache_read_share: number | null;
  labels?: Record<string, string>;
}

export interface NextUp {
  provider: string;
  model: string;
  policy: string;
  auth_id?: string;
  account?: string;
  reason: string;
  fallback?: boolean;
}

export interface Overview {
  now: string;
  mode: 'shadow' | 'active';
  policy: string;
  counters: Record<string, number>;
  next_up: NextUp[] | null;
  affinity: { enabled: boolean; ttl: string; scope: string; rebind_policy: string };
}

export interface Ranked {
  auth_id: string;
  score: number;
  reason: string;
  quota_state: string;
}

export interface Decision {
  id: number;
  at: string;
  provider: string;
  model: string;
  mode: string;
  policy: string;
  session?: string;
  session_kind?: string;
  client?: string;
  outcome: string;
  cause?: string;
  auth_id?: string;
  applied: boolean;
  ranked: Ranked[] | null;
  result?: { status: number; failed: boolean };
}

export interface Activity {
  now: string;
  decisions: Decision[] | null;
  labels: Record<string, string>;
}

export interface SessionRow {
  session: string;
  client?: string;
  kind: string;
  provider: string;
  model: string;
  auth_id: string;
  account: string;
  origin: string;
  last_seen: string;
  expires_at: string;
  requests: number;
  failovers: number;
  rebinds: number;
}

export interface Sessions {
  now: string;
  sessions: SessionRow[] | null;
  ttl: string;
  enabled: boolean;
  mode: string;
}

export interface UsageCounts {
  requests: number;
  failures: number;
  input_tokens: number;
  output_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  failovers: number;
  affinity_hits: number;
  new_sessions: number;
}

export interface Usage {
  now: string;
  providers: Record<string, { totals: UsageCounts; cache_read_share: number | null }>;
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
  plans: IgnitionPlan[] | null;
  events: IgnitionEvent[] | null;
}

export interface NotifyEntry {
  at: string;
  title: string;
  body?: string;
  level: string;
  delivered: boolean;
  error?: string;
}

export interface SinkStatus {
  enabled: boolean;
  ready: boolean;
  sent: number;
  dropped: number;
  queued: number;
  error?: string;
}
