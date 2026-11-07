export interface Account {
  username: string;
  role: 'admin' | 'member';
  disabled: boolean;
  created_at: string;
}

export interface Channel {
  id: string;
  owner: string;
  slug: string;
  prefix: string;
  published: boolean;
  public: boolean;
  announcement?: string;
  announcement_updated_at?: string;
  created_at: string;
}

export interface BoardMessage {
  id: string;
  channel_id: string;
  author: string;
  content: string;
  created_at: string;
}

export interface PersonalKey {
  id: string;
  owner: string;
  name: string;
  hint: string;
  /** Full key material; absent on keys created before secrets became reusable. */
  secret?: string;
  auto_balance: boolean;
  blocked_models: string[];
  disabled: boolean;
  created_at: string;
}

/** Trimmed key reference from /keys/all; carries no secret material. */
export interface SharedKeyRef {
  id: string;
  owner: string;
  name: string;
}

export interface Invitation {
  id: string;
  code: string;
  created_by: string;
  expires_at: string;
  max_uses: number;
  used_by: string[];
  revoked: boolean;
}

export interface ThinkingSupport {
  min?: number;
  max?: number;
  zero_allowed?: boolean;
  dynamic_allowed?: boolean;
  levels?: string[];
}

export interface MarketplaceModel {
  id: string;
  base_model: string;
  display_name: string;
  description?: string;
  owner: string;
  created_by?: string;
  channel_id: string;
  channel: string;
  channel_slug: string;
  provider: string;
  kind?: 'credential' | 'provider';
  credential_label?: string;
  channel_index?: number;
  available: boolean;
  credential_count: number;
  context_length: number;
  max_completion_tokens?: number;
  thinking?: ThinkingSupport | null;
}

export interface PerformanceBucket {
  start: number;
  requests: number;
  failures: number;
  latency_ms: number;
  latency_count: number;
  ttft_ms: number;
  ttft_count: number;
  output_tokens: number;
  generation_ms: number;
}

export interface ModelPerformance {
  model: string;
  totals: PerformanceBucket;
  buckets: PerformanceBucket[];
}

export interface UsageEntry {
  id: string;
  request_id: string;
  trace_id: string;
  at: string;
  consumer: string;
  key_id: string;
  key_name: string;
  owner: string;
  channel_id: string;
  channel: string;
  credential_id: string;
  credential_name: string;
  provider: string;
  model: string;
  upstream_model: string;
  endpoint: string;
  stream: boolean;
  failed: boolean;
  status: number;
  latency_ms: number;
  ttft_ms: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  tokens_known: boolean;
  /** Translated upstream thinking level (low/high/…), empty when the model did not set one. */
  reasoning_effort?: string;
  /** Made with an even-load-balancing key; attempts share the trace ID. */
  balanced?: boolean;
  /** Upstream failure body (truncated), present on failed attempts. */
  error?: string;
  /** Total attempts in this balanced request's trace (list responses only). */
  attempts?: number;
}

export interface Summary {
  consumer?: string;
  owner?: string;
  channel?: string;
  model?: string;
  credential_id?: string;
  day?: string;
  calls: number;
  executions: number;
  failures: number;
  input_tokens: number;
  output_tokens: number;
  reasoning_tokens: number;
  cache_read_tokens: number;
  cache_creation_tokens: number;
  total_tokens: number;
  unknown_token_records: number;
  /** Successful attempts only; divide by the matching count for averages. */
  latency_ms?: number;
  latency_count?: number;
  ttft_ms?: number;
  ttft_count?: number;
  /** Earlier failed balanced attempts folded into their final attempt (total only). */
  failovers?: number;
}

/** One hour of usage (unix seconds start). */
export interface SummaryBucket {
  start: number;
  calls: number;
  executions: number;
  failures: number;
  total_tokens: number;
  input_tokens: number;
  output_tokens: number;
}

/** Distinct attribute values present under a usage filter. */
export interface UsageFacets {
  providers: string[];
  endpoints: string[];
  statuses: number[];
  /** Reasoning levels; "none" stands for records without one. */
  efforts: string[];
}

export interface SummaryResponse {
  total: Summary;
  groups: Summary[];
  buckets?: SummaryBucket[];
}

export interface AuditEntry {
  id: string;
  at: string;
  actor: string;
  action: string;
  target: string;
  status: number;
  ip: string;
}

export interface HistoryPage<T> {
  items: T[];
  total: number;
  page: number;
  page_size: number;
}
