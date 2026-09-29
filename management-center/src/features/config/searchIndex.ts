// Search index for the visual config editor's global "jump to field" search.
//
// IMPORTANT: this index is maintained by hand and is NOT what drives field
// rendering — it only powers search. When you add, remove, or move a field in
// components/sections/*.tsx (or fields/sharedFields.tsx), update the matching
// entry here, wrap the field's JSX in <FieldAnchor fieldId="..."> with the same
// `fieldId`, and map it in constants.ts FIELD_VALUE_KEYS.
// tests/configFieldParity.test.ts enforces the three-way parity — a missing or
// extra entry anywhere fails CI.

export type VisualSectionId =
  'connectivity' | 'network' | 'logging' | 'quota' | 'streaming' | 'advanced' | 'payload';

export interface ConfigFieldSearchEntry {
  /** Stable anchor id; matches FieldAnchor's `fieldId` and the rendered DOM id. */
  fieldId: string;
  sectionId: VisualSectionId;
  /** i18n key resolved with t() at search time so matching follows the active language. */
  labelKey: string;
  /** Optional secondary i18n key shown next to the label to disambiguate duplicates
   *  (e.g. Claude vs Codex "User-Agent"). Also searchable. */
  qualifierKey?: string;
  /** Optional hint i18n key — searchable but not shown in results. */
  hintKey?: string;
  /** Backend YAML key aliases, e.g. ['proxy-url']. Static strings (language-agnostic). */
  yamlKeys?: string[];
  /** Extra synonyms to match against (language-agnostic, lowercase). */
  keywords?: string[];
}

/** DOM id for a field anchor — kept in one place so the index and the anchors agree. */
export const configFieldDomId = (fieldId: string) => `cfg-field-${fieldId}`;

type Translate = (key: string) => string;

// Compact helper: every label/hint key lives under config_management.visual.
const L = (key: string) => `config_management.visual.${key}`;

export const CONFIG_FIELD_SEARCH_INDEX: ConfigFieldSearchEntry[] = [
  // ── connectivity ──────────────────────────────────────────────────────────
  {
    fieldId: 'host',
    sectionId: 'connectivity',
    labelKey: L('sections.server.host'),
    yamlKeys: ['host'],
  },
  {
    fieldId: 'port',
    sectionId: 'connectivity',
    labelKey: L('sections.server.port'),
    yamlKeys: ['port'],
  },
  {
    fieldId: 'authDir',
    sectionId: 'connectivity',
    labelKey: L('sections.auth.auth_dir'),
    hintKey: L('sections.auth.auth_dir_hint'),
    yamlKeys: ['auth-dir'],
  },
  {
    fieldId: 'apiKeys',
    sectionId: 'connectivity',
    labelKey: L('api_keys.label'),
    yamlKeys: ['api-keys'],
    keywords: ['api key', 'apikey', 'token'],
  },
  {
    fieldId: 'tlsEnable',
    sectionId: 'connectivity',
    labelKey: L('sections.tls.enable'),
    hintKey: L('sections.tls.enable_desc'),
    yamlKeys: ['tls'],
    keywords: ['tls', 'ssl', 'https'],
  },
  {
    fieldId: 'tlsCert',
    sectionId: 'connectivity',
    labelKey: L('sections.tls.cert'),
    yamlKeys: ['tls', 'cert'],
    keywords: ['tls', 'ssl', 'certificate'],
  },
  {
    fieldId: 'tlsKey',
    sectionId: 'connectivity',
    labelKey: L('sections.tls.key'),
    yamlKeys: ['tls', 'key'],
    keywords: ['tls', 'ssl', 'private key'],
  },
  {
    fieldId: 'rmAllowRemote',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.allow_remote'),
    hintKey: L('sections.remote.allow_remote_desc'),
    yamlKeys: ['remote-management', 'allow-remote'],
  },
  {
    fieldId: 'rmDisableControlPanel',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.disable_panel'),
    yamlKeys: ['remote-management', 'disable-control-panel'],
  },
  {
    fieldId: 'rmDisableAutoUpdatePanel',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.disable_auto_update_panel'),
    yamlKeys: ['remote-management', 'disable-auto-update-panel'],
  },
  {
    fieldId: 'rmSecretKey',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.secret_key'),
    yamlKeys: ['remote-management', 'secret-key'],
  },
  {
    fieldId: 'rmPanelRepo',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.panel_repo'),
    yamlKeys: ['remote-management', 'panel-github-repository'],
  },
  // ── network ───────────────────────────────────────────────────────────────
  {
    fieldId: 'proxyUrl',
    sectionId: 'network',
    labelKey: L('sections.network.proxy_url'),
    yamlKeys: ['proxy-url'],
  },
  {
    fieldId: 'requestRetry',
    sectionId: 'network',
    labelKey: L('sections.network.request_retry'),
    yamlKeys: ['request-retry'],
  },
  {
    fieldId: 'maxRetryCredentials',
    sectionId: 'network',
    labelKey: L('sections.network.max_retry_credentials'),
    hintKey: L('sections.network.max_retry_credentials_hint'),
    yamlKeys: ['max-retry-credentials'],
  },
  {
    fieldId: 'maxRetryInterval',
    sectionId: 'network',
    labelKey: L('sections.network.max_retry_interval'),
    hintKey: L('sections.network.max_retry_interval_hint'),
    yamlKeys: ['max-retry-interval'],
  },
  {
    fieldId: 'authAutoRefreshWorkers',
    sectionId: 'network',
    labelKey: L('sections.network.auth_auto_refresh_workers'),
    hintKey: L('sections.network.auth_auto_refresh_workers_hint'),
    yamlKeys: ['auth-auto-refresh-workers'],
  },
  {
    fieldId: 'routingStrategy',
    sectionId: 'network',
    labelKey: L('sections.network.routing_strategy'),
    hintKey: L('sections.network.routing_strategy_hint'),
    yamlKeys: ['routing', 'strategy'],
    keywords: ['round-robin', 'weighted-round-robin', 'wrr', 'fill-first'],
  },
  {
    fieldId: 'disableImageGeneration',
    sectionId: 'network',
    labelKey: L('sections.network.disable_image_generation'),
    hintKey: L('sections.network.disable_image_generation_hint'),
    yamlKeys: ['disable-image-generation'],
    keywords: ['false', 'true', 'chat', 'passthrough'],
  },
  {
    fieldId: 'gptImage2BaseModel',
    sectionId: 'network',
    labelKey: L('sections.network.gpt_image_2_base_model'),
    hintKey: L('sections.network.gpt_image_2_base_model_hint'),
    yamlKeys: ['gpt-image-2-base-model'],
  },
  {
    fieldId: 'routingSessionAffinityTTL',
    sectionId: 'network',
    labelKey: L('sections.network.session_affinity_ttl'),
    yamlKeys: ['routing', 'session-affinity-ttl'],
  },
  {
    fieldId: 'forceModelPrefix',
    sectionId: 'network',
    labelKey: L('sections.network.force_model_prefix'),
    hintKey: L('sections.network.force_model_prefix_desc'),
    yamlKeys: ['force-model-prefix'],
  },
  {
    fieldId: 'passthroughHeaders',
    sectionId: 'network',
    labelKey: L('sections.network.passthrough_headers'),
    hintKey: L('sections.network.passthrough_headers_desc'),
    yamlKeys: ['passthrough-headers'],
  },
  {
    fieldId: 'disableCooling',
    sectionId: 'network',
    labelKey: L('sections.network.disable_cooling'),
    hintKey: L('sections.network.disable_cooling_desc'),
    yamlKeys: ['disable-cooling'],
  },
  {
    fieldId: 'routingSessionAffinity',
    sectionId: 'network',
    labelKey: L('sections.network.session_affinity'),
    yamlKeys: ['routing', 'session-affinity'],
  },
  {
    fieldId: 'wsAuth',
    sectionId: 'network',
    labelKey: L('sections.network.ws_auth'),
    hintKey: L('sections.network.ws_auth_desc'),
    yamlKeys: ['ws-auth'],
    keywords: ['websocket'],
  },
  // ── logging ───────────────────────────────────────────────────────────────
  {
    fieldId: 'debug',
    sectionId: 'logging',
    labelKey: L('sections.system.debug'),
    hintKey: L('sections.system.debug_desc'),
    yamlKeys: ['debug'],
  },
  {
    fieldId: 'commercialMode',
    sectionId: 'logging',
    labelKey: L('sections.system.commercial_mode'),
    hintKey: L('sections.system.commercial_mode_desc'),
    yamlKeys: ['commercial-mode'],
  },
  {
    fieldId: 'loggingToFile',
    sectionId: 'logging',
    labelKey: L('sections.system.logging_to_file'),
    hintKey: L('sections.system.logging_to_file_desc'),
    yamlKeys: ['logging-to-file'],
  },
  {
    fieldId: 'logsMaxTotalSizeMb',
    sectionId: 'logging',
    labelKey: L('sections.system.logs_max_size'),
    yamlKeys: ['logs-max-total-size-mb'],
  },
  {
    fieldId: 'errorLogsMaxFiles',
    sectionId: 'logging',
    labelKey: L('sections.system.error_logs_max_files'),
    yamlKeys: ['error-logs-max-files'],
  },
  {
    fieldId: 'redisUsageQueueRetentionSeconds',
    sectionId: 'logging',
    labelKey: L('sections.system.redis_usage_retention'),
    hintKey: L('sections.system.redis_usage_retention_hint'),
    yamlKeys: ['redis-usage-queue-retention-seconds'],
  },
  {
    fieldId: 'usageStatisticsEnabled',
    sectionId: 'logging',
    labelKey: L('sections.system.usage_statistics_enabled'),
    hintKey: L('sections.system.usage_statistics_enabled_desc'),
    yamlKeys: ['usage-statistics-enabled'],
  },
  // ── quota ─────────────────────────────────────────────────────────────────
  {
    fieldId: 'quotaSwitchProject',
    sectionId: 'quota',
    labelKey: L('sections.quota.switch_project'),
    hintKey: L('sections.quota.switch_project_desc'),
    yamlKeys: ['quota-exceeded', 'switch-project'],
  },
  {
    fieldId: 'quotaSwitchPreviewModel',
    sectionId: 'quota',
    labelKey: L('sections.quota.switch_preview_model'),
    hintKey: L('sections.quota.switch_preview_model_desc'),
    yamlKeys: ['quota-exceeded', 'switch-preview-model'],
  },
  {
    fieldId: 'quotaAntigravityCredits',
    sectionId: 'quota',
    labelKey: L('sections.quota.antigravity_credits'),
    yamlKeys: ['quota-exceeded', 'antigravity-credits'],
  },
  // ── streaming ─────────────────────────────────────────────────────────────
  {
    fieldId: 'streamingKeepaliveSeconds',
    sectionId: 'streaming',
    labelKey: L('sections.streaming.keepalive_seconds'),
    hintKey: L('sections.streaming.keepalive_hint'),
    yamlKeys: ['streaming', 'keepalive-seconds'],
  },
  {
    fieldId: 'streamingBootstrapRetries',
    sectionId: 'streaming',
    labelKey: L('sections.streaming.bootstrap_retries'),
    hintKey: L('sections.streaming.bootstrap_hint'),
    yamlKeys: ['streaming', 'bootstrap-retries'],
  },
  {
    fieldId: 'streamingNonstreamKeepalive',
    sectionId: 'streaming',
    labelKey: L('sections.streaming.nonstream_keepalive'),
    hintKey: L('sections.streaming.nonstream_keepalive_hint'),
    yamlKeys: ['streaming', 'nonstream-keepalive-interval'],
  },
  // ── advanced ──────────────────────────────────────────────────────────────
  {
    fieldId: 'pluginsEnabled',
    sectionId: 'advanced',
    labelKey: L('sections.system.plugins_enabled'),
    hintKey: L('sections.system.plugins_enabled_desc'),
    yamlKeys: ['plugins'],
  },
  {
    fieldId: 'pluginStoreSources',
    sectionId: 'advanced',
    labelKey: L('sections.system.plugin_store_sources'),
    hintKey: L('sections.system.plugin_store_sources_hint'),
    yamlKeys: ['plugins', 'store-sources'],
  },
  {
    fieldId: 'pluginStoreAuth',
    sectionId: 'advanced',
    labelKey: L('sections.system.plugin_store_auth'),
    hintKey: L('sections.system.plugin_store_auth_hint'),
    yamlKeys: ['plugins', 'store-auth'],
  },
  {
    fieldId: 'antigravitySensitiveWords',
    sectionId: 'advanced',
    labelKey: L('sections.system.antigravity_sensitive_words'),
    hintKey: L('sections.system.antigravity_sensitive_words_desc'),
    yamlKeys: ['antigravity', 'sensitive-words'],
    keywords: ['antigravity', 'obfuscate', 'zero-width'],
  },
  {
    fieldId: 'devinSensitiveWords',
    sectionId: 'advanced',
    labelKey: L('sections.system.devin_sensitive_words'),
    hintKey: L('sections.system.devin_sensitive_words_desc'),
    yamlKeys: ['devin', 'sensitive-words'],
    keywords: ['devin', 'system prompt', 'remove line', 'obfuscate', 'zero-width'],
  },
  {
    fieldId: 'antigravitySignatureCacheEnabled',
    sectionId: 'advanced',
    labelKey: L('sections.system.antigravity_signature_cache'),
    hintKey: L('sections.system.antigravity_signature_cache_desc'),
    yamlKeys: ['antigravity-signature-cache-enabled'],
  },
  {
    fieldId: 'antigravitySignatureBypassStrict',
    sectionId: 'advanced',
    labelKey: L('sections.system.antigravity_signature_strict'),
    hintKey: L('sections.system.antigravity_signature_strict_desc'),
    yamlKeys: ['antigravity-signature-bypass-strict'],
  },
  // Claude header defaults — qualifierKey disambiguates the shared "User-Agent" label.
  {
    fieldId: 'claudeHeaderUserAgent',
    sectionId: 'advanced',
    labelKey: L('sections.headers.user_agent'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['claude-header-defaults', 'user-agent'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderPackageVersion',
    sectionId: 'advanced',
    labelKey: L('sections.headers.package_version'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['claude-header-defaults', 'package-version'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderRuntimeVersion',
    sectionId: 'advanced',
    labelKey: L('sections.headers.runtime_version'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['claude-header-defaults', 'runtime-version'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderOs',
    sectionId: 'advanced',
    labelKey: L('sections.headers.os'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['claude-header-defaults', 'os'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderArch',
    sectionId: 'advanced',
    labelKey: L('sections.headers.arch'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['claude-header-defaults', 'arch'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderTimeout',
    sectionId: 'advanced',
    labelKey: L('sections.headers.timeout'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['claude-header-defaults', 'timeout'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderStabilizeDeviceProfile',
    sectionId: 'advanced',
    labelKey: L('sections.headers.stabilize_device'),
    qualifierKey: L('sections.headers.claude_title'),
    hintKey: L('sections.headers.stabilize_device_desc'),
    yamlKeys: ['claude-header-defaults', 'stabilize-device-profile'],
    keywords: ['claude'],
  },
  // Codex header defaults.
  {
    fieldId: 'codexHeaderUserAgent',
    sectionId: 'advanced',
    labelKey: L('sections.headers.user_agent'),
    qualifierKey: L('sections.headers.codex_title'),
    yamlKeys: ['codex-header-defaults', 'user-agent'],
    keywords: ['codex'],
  },
  {
    fieldId: 'codexHeaderBetaFeatures',
    sectionId: 'advanced',
    labelKey: L('sections.headers.beta_features'),
    qualifierKey: L('sections.headers.codex_title'),
    yamlKeys: ['codex-header-defaults', 'beta-features'],
    keywords: ['codex'],
  },
  // ── payload (coarse: one entry per rule group) ──────────────────────────────
  {
    fieldId: 'payloadDefaultRules',
    sectionId: 'payload',
    labelKey: L('sections.payload.default_rules'),
    hintKey: L('sections.payload.default_rules_desc'),
    keywords: ['payload', 'rule'],
  },
  {
    fieldId: 'payloadDefaultRawRules',
    sectionId: 'payload',
    labelKey: L('sections.payload.default_raw_rules'),
    hintKey: L('sections.payload.default_raw_rules_desc'),
    keywords: ['payload', 'rule', 'json'],
  },
  {
    fieldId: 'payloadOverrideRules',
    sectionId: 'payload',
    labelKey: L('sections.payload.override_rules'),
    hintKey: L('sections.payload.override_rules_desc'),
    keywords: ['payload', 'rule'],
  },
  {
    fieldId: 'payloadOverrideRawRules',
    sectionId: 'payload',
    labelKey: L('sections.payload.override_raw_rules'),
    hintKey: L('sections.payload.override_raw_rules_desc'),
    keywords: ['payload', 'rule', 'json'],
  },
  {
    fieldId: 'payloadFilterRules',
    sectionId: 'payload',
    labelKey: L('sections.payload.filter_rules'),
    hintKey: L('sections.payload.filter_rules_desc'),
    keywords: ['payload', 'rule', 'filter'],
  },
];

const MAX_RESULTS = 8;

export function findConfigFieldById(
  fieldId: string | null | undefined
): ConfigFieldSearchEntry | undefined {
  if (!fieldId) return undefined;
  return CONFIG_FIELD_SEARCH_INDEX.find((entry) => entry.fieldId === fieldId);
}

/**
 * Lowercase substring search over label + qualifier + hint + YAML keys + keywords.
 * Returns the best ~8 matches, label/qualifier hits ranked above alias-only hits.
 */
export function searchConfigFields(query: string, t: Translate): ConfigFieldSearchEntry[] {
  const q = query.trim().toLowerCase();
  if (!q) return [];

  const scored: { entry: ConfigFieldSearchEntry; score: number }[] = [];

  for (const entry of CONFIG_FIELD_SEARCH_INDEX) {
    const label = t(entry.labelKey).toLowerCase();
    const qualifier = entry.qualifierKey ? t(entry.qualifierKey).toLowerCase() : '';
    const hint = entry.hintKey ? t(entry.hintKey).toLowerCase() : '';
    const yaml = (entry.yamlKeys ?? []).join(' ').toLowerCase();
    const keywords = (entry.keywords ?? []).join(' ').toLowerCase();

    let score = Number.POSITIVE_INFINITY;
    if (label.startsWith(q)) score = 0;
    else if (label.includes(q)) score = 1;
    else if (qualifier.includes(q) || keywords.includes(q)) score = 2;
    else if (yaml.includes(q)) score = 3;
    else if (hint.includes(q)) score = 4;

    if (Number.isFinite(score)) scored.push({ entry, score });
  }

  scored.sort((a, b) => a.score - b.score);
  return scored.slice(0, MAX_RESULTS).map((item) => item.entry);
}
