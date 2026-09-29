/**
 * Quota management types.
 */

// Theme types
export type ThemeColors = { bg: string; text: string; border?: string };
export type TypeColorSet = { light: ThemeColors; dark?: ThemeColors };
export type ResolvedTheme = 'light' | 'dark';

// API payload types
export interface AntigravityQuotaSummaryBucketPayload {
  bucketId?: string;
  bucket_id?: string;
  displayName?: string;
  display_name?: string;
  window?: string;
  resetTime?: string;
  reset_time?: string;
  remainingFraction?: number | string;
  remaining_fraction?: number | string;
  description?: string;
}

export interface AntigravityQuotaSummaryGroupPayload {
  displayName?: string;
  display_name?: string;
  description?: string;
  buckets?: AntigravityQuotaSummaryBucketPayload[];
}

export interface AntigravityQuotaSummaryPayload {
  groups?: AntigravityQuotaSummaryGroupPayload[];
}

export interface CodexUsageWindow {
  used_percent?: number | string;
  usedPercent?: number | string;
  limit_window_seconds?: number | string;
  limitWindowSeconds?: number | string;
  reset_after_seconds?: number | string;
  resetAfterSeconds?: number | string;
  reset_at?: number | string;
  resetAt?: number | string;
}

export interface CodexRateLimitInfo {
  allowed?: boolean;
  limit_reached?: boolean;
  limitReached?: boolean;
  primary_window?: CodexUsageWindow | null;
  primaryWindow?: CodexUsageWindow | null;
  secondary_window?: CodexUsageWindow | null;
  secondaryWindow?: CodexUsageWindow | null;
}

export interface CodexAdditionalRateLimit {
  limit_name?: string;
  limitName?: string;
  metered_feature?: string;
  meteredFeature?: string;
  rate_limit?: CodexRateLimitInfo | null;
  rateLimit?: CodexRateLimitInfo | null;
}

export interface CodexRateLimitResetCredits {
  available_count?: number | string;
  availableCount?: number | string;
  applicable_available_count?: number | string;
  applicableAvailableCount?: number | string;
}

export interface CodexRateLimitResetCredit {
  id: string;
  status: string;
  grantedAt: string;
  expiresAt: string;
}

export interface CodexUsagePayload {
  plan_type?: string;
  planType?: string;
  rate_limit?: CodexRateLimitInfo | null;
  rateLimit?: CodexRateLimitInfo | null;
  code_review_rate_limit?: CodexRateLimitInfo | null;
  codeReviewRateLimit?: CodexRateLimitInfo | null;
  additional_rate_limits?: CodexAdditionalRateLimit[] | null;
  additionalRateLimits?: CodexAdditionalRateLimit[] | null;
  rate_limit_reset_credits?: CodexRateLimitResetCredits | null;
  rateLimitResetCredits?: CodexRateLimitResetCredits | null;
}

// Claude API payload types
export interface ClaudeUsageWindow {
  utilization: number;
  resets_at: string | null;
}

export interface ClaudeUsageLimit {
  kind?: string | null;
  group?: string | null;
  percent?: number | null;
  resets_at?: string | null;
  is_active?: boolean | null;
  scope?: {
    model?: {
      id?: string | null;
      display_name?: string | null;
    } | null;
  } | null;
}

export interface ClaudeExtraUsage {
  is_enabled: boolean;
  monthly_limit: number;
  used_credits: number;
  utilization: number | null;
}

export interface ClaudeUsagePayload {
  five_hour?: ClaudeUsageWindow | null;
  seven_day?: ClaudeUsageWindow | null;
  seven_day_oauth_apps?: ClaudeUsageWindow | null;
  seven_day_opus?: ClaudeUsageWindow | null;
  seven_day_sonnet?: ClaudeUsageWindow | null;
  seven_day_cowork?: ClaudeUsageWindow | null;
  iguana_necktie?: ClaudeUsageWindow | null;
  limits?: ClaudeUsageLimit[] | null;
  extra_usage?: ClaudeExtraUsage | null;
}

export interface ClaudeProfileResponse {
  account?: {
    uuid?: string;
    full_name?: string;
    display_name?: string;
    email?: string;
    has_claude_max?: boolean;
    has_claude_pro?: boolean;
    created_at?: string;
  };
  organization?: {
    uuid?: string;
    name?: string;
    organization_type?: string;
    billing_type?: string;
    rate_limit_tier?: string;
    has_extra_usage_enabled?: boolean;
    subscription_status?: string;
    subscription_created_at?: string;
  };
}

export interface ClaudeQuotaWindow {
  id: string;
  label: string;
  labelKey?: string;
  usedPercent: number | null;
  resetLabel: string;
  /**
   * Reset instant in epoch ms, kept alongside the display label so callers that
   * need to compute (ordering, the timeline) aren't stuck comparing formatted
   * strings. Null when the payload carried no parseable timestamp.
   */
  resetAtMs?: number | null;
  /** Window length in hours — 5 for the rolling window, 168 for the weekly ones. */
  periodHours?: number | null;
}

export interface ClaudeQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  windows: ClaudeQuotaWindow[];
  extraUsage?: ClaudeExtraUsage | null;
  planType?: string | null;
  error?: string;
  errorStatus?: number;
}

// Quota state types
export interface AntigravityQuotaGroup {
  id: string;
  label: string;
  description?: string;
  buckets: AntigravityQuotaBucket[];
}

export interface AntigravityQuotaSubscription {
  plan: string | null;
  tierName: string | null;
  tierId: string | null;
}

export interface AntigravityQuotaBucket {
  id: string;
  label: string;
  window?: string;
  remainingFraction: number;
  resetTime?: string;
  description?: string;
  /**
   * Reset instant in epoch ms, parsed from `resetTime`. Kept alongside the raw
   * string so the timeline can position a bar without re-parsing.
   *
   * Not corrected by `serverTimeOffsetMs`: that offset is applied to *now* when
   * rendering the card countdown, and every other provider's `resetAtMs` is an
   * uncorrected instant too. Keeping them consistent matters more than the few
   * seconds of clock skew it represents.
   */
  resetAtMs?: number | null;
  /** Window length in hours, from the bucket's `window` field. */
  periodHours?: number | null;
}

export interface AntigravityQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  groups: AntigravityQuotaGroup[];
  subscription?: AntigravityQuotaSubscription | null;
  serverTimeOffsetMs?: number | null;
  error?: string;
  errorStatus?: number;
}

export interface CodexQuotaWindow {
  id: string;
  label: string;
  labelKey?: string;
  labelParams?: Record<string, string | number>;
  usedPercent: number | null;
  resetLabel: string;
  /** Reset instant in epoch ms; null when the payload carried no timestamp. */
  resetAtMs?: number | null;
  /** Window length in hours, from the payload's limit_window_seconds. */
  periodHours?: number | null;
}

export interface CodexQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  windows: CodexQuotaWindow[];
  planType?: string | null;
  subscriptionActiveUntil?: string | number | null;
  rateLimitResetCreditsAvailableCount?: number | null;
  rateLimitResetCreditsApplicableAvailableCount?: number | null;
  rateLimitResetCredits?: CodexRateLimitResetCredit[];
  rateLimitResetCreditsError?: string;
  error?: string;
  errorStatus?: number;
}

export interface DevinQuotaWindow {
  id: 'daily' | 'weekly';
  label?: string;
  remainingPercent: number | null;
  resetAtMs: number | null;
  periodHours: number;
}

/** Only quota observations, never the credential-bearing refresh response. */
export interface DevinQuotaData {
  windows: DevinQuotaWindow[];
  observedAtMs: number | null;
  plan: string | null;
  planStartMs: number | null;
  planEndMs: number | null;
}

export interface DevinQuotaState extends DevinQuotaData {
  status: 'idle' | 'loading' | 'success' | 'error';
  error?: string;
  errorStatus?: number;
}

/** A whitelisted quota observation from Meta's Muse key endpoint. */
export interface MetaQuotaWindow {
  id: 'window' | 'weekly';
  usedPercent: number | null;
  /** Reset instant as Unix seconds, matching the upstream contract. */
  resetAt?: number;
  durationMinutes?: number;
}

/** Contains quota/display fields only; credential-bearing response fields are discarded. */
export interface MetaQuotaData {
  planName?: string;
  isSubscriptionActive?: boolean;
  windows: MetaQuotaWindow[];
}

export interface MetaQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  data?: MetaQuotaData;
  error?: string;
  errorStatus?: number;
}

// Kimi API payload types
export interface KimiUsageDetail {
  used?: number | string;
  limit?: number | string;
  remaining?: number | string;
  name?: string;
  title?: string;
  resetAt?: string;
  reset_at?: string;
  resetTime?: string;
  reset_time?: string;
  resetIn?: number | string;
  reset_in?: number | string;
  ttl?: number | string;
}

export interface KimiLimitWindow {
  duration?: number | string;
  timeUnit?: string;
}

export interface KimiLimitItem {
  name?: string;
  title?: string;
  scope?: string;
  detail?: KimiUsageDetail;
  window?: KimiLimitWindow;
  used?: number | string;
  limit?: number | string;
  remaining?: number | string;
  duration?: number | string;
  timeUnit?: string;
  resetAt?: string;
  reset_at?: string;
  resetIn?: number | string;
  reset_in?: number | string;
  ttl?: number | string;
}

export interface KimiUsagePayload {
  usage?: KimiUsageDetail;
  limits?: KimiLimitItem[];
}

export interface KimiQuotaRow {
  id: string;
  label?: string;
  labelKey?: string;
  labelParams?: Record<string, string | number>;
  used: number;
  limit: number;
  resetHint?: string;
  /** Reset instant in epoch ms; null when only a relative hint was available. */
  resetAtMs?: number | null;
  /** Window length in hours, derived from explicit duration metadata or the limit scope. */
  periodHours?: number | null;
}

export interface KimiQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  rows: KimiQuotaRow[];
  error?: string;
  errorStatus?: number;
}

// xAI/Grok API payload types
export interface XaiBillingCent {
  val?: number | string;
}

export interface XaiBillingPeriod {
  type?: string;
  start?: string;
  end?: string;
}

export interface XaiBillingProductUsage {
  product?: string;
  usagePercent?: number | string | null;
  usage_percent?: number | string | null;
}

export interface XaiBillingConfig {
  currentPeriod?: XaiBillingPeriod | null;
  current_period?: XaiBillingPeriod | null;
  creditUsagePercent?: number | string | null;
  credit_usage_percent?: number | string | null;
  productUsage?: XaiBillingProductUsage[] | null;
  product_usage?: XaiBillingProductUsage[] | null;
  monthlyLimit?: XaiBillingCent | number | string | null;
  monthly_limit?: XaiBillingCent | number | string | null;
  used?: XaiBillingCent | number | string | null;
  onDemandCap?: XaiBillingCent | number | string | null;
  on_demand_cap?: XaiBillingCent | number | string | null;
  onDemandUsed?: XaiBillingCent | number | string | null;
  on_demand_used?: XaiBillingCent | number | string | null;
  billingPeriodStart?: string;
  billing_period_start?: string;
  billingPeriodEnd?: string;
  billing_period_end?: string;
}

export interface XaiBillingPayload {
  config?: XaiBillingConfig | null;
}

export type XaiBillingPeriodType = 'weekly' | 'monthly' | 'unknown';

export interface XaiProductUsageSummary {
  product: string;
  usagePercent: number | null;
}

export interface XaiBillingSummary {
  mode: 'billing' | 'paid-health';
  source?: 'cli-chat-proxy' | 'api.x.ai-fallback';
  planType?: 'paid';
  healthStatus?: 'chat-ok';
  userId?: string;
  teamId?: string;
  periodType: XaiBillingPeriodType;
  usagePercent: number | null;
  periodStart?: string;
  periodEnd?: string;
  productUsage: XaiProductUsageSummary[];
  monthlyLimitCents: number | null;
  usedCents: number | null;
  includedUsedCents: number | null;
  onDemandCapCents: number | null;
  onDemandUsedCents: number | null;
  onDemandUsedPercent: number | null;
  billingPeriodStart?: string;
  billingPeriodEnd?: string;
  usedPercent: number | null;
  /**
   * Reset instant of the *active* period (`periodEnd`) in epoch ms.
   *
   * Only meaningful as a quota window when `periodType` is 'weekly' — for a
   * monthly summary this is the billing cycle rollover, which is a spend cap
   * resetting, not rate-limited capacity coming back.
   */
  resetAtMs?: number | null;
  /** Active period length in hours, derived from `periodStart` → `periodEnd`. */
  periodHours?: number | null;
}

export interface XaiQuotaState {
  status: 'idle' | 'loading' | 'success' | 'error';
  billing: XaiBillingSummary | null;
  error?: string;
  errorStatus?: number;
}
