/** Matches the Muse key response shape; credentials and identity are synthetic. */
export const metaQuotaResponse = {
  api_key: 'LLM|fixture-only|not-a-real-key',
  base_url: 'https://api.meta.ai/v1',
  has_payment_method: false,
  require_payment: false,
  is_subs_active: true,
  can_subscribe: false,
  show_subs_upsell: true,
  user_full_name: 'Fixture User',
  user_email: 'fixture@example.invalid',
  payment_method: null,
  action_url: null,
  subs_tier_id: 'fixture-tier',
  subs_tier_name: 'Muse Code Everyday Usage',
  is_subs_upgrade_available: true,
  subs_usage: {
    window: { used_percent: 2, window_duration_mins: 300, resets_at: 1789678120 },
    weekly: { used_percent: 0, resets_at: 1789948800 },
    tier: 'fixture-tier',
  },
};
