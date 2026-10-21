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
  'connectivity' | 'network' | 'logging' | 'streaming' | 'advanced' | 'payload';

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
  {
    fieldId: 'routingSessionAffinitySubagents',
    sectionId: 'network',
    labelKey: L('additions.routingSessionAffinitySubagents.label'),
    hintKey: L('additions.routingSessionAffinitySubagents.hint'),
    yamlKeys: ['routing', 'session-affinity-subagents'],
  },
  {
    fieldId: 'saveCooldownStatus',
    sectionId: 'network',
    labelKey: L('additions.saveCooldownStatus.label'),
    hintKey: L('additions.saveCooldownStatus.hint'),
    yamlKeys: ['routing', 'cooldown', 'save-cooldown-status'],
  },
  {
    fieldId: 'transientErrorCooldownSeconds',
    sectionId: 'network',
    labelKey: L('additions.transientErrorCooldownSeconds.label'),
    hintKey: L('additions.transientErrorCooldownSeconds.hint'),
    yamlKeys: ['routing', 'cooldown', 'transient-error-cooldown-seconds'],
  },
  {
    fieldId: 'videoResultAuthCacheTTL',
    sectionId: 'network',
    labelKey: L('additions.videoResultAuthCacheTTL.label'),
    hintKey: L('additions.videoResultAuthCacheTTL.hint'),
    yamlKeys: ['multimedia', 'video-result-auth-cache-ttl'],
  },
  {
    fieldId: 'claudeHeaderTimezone',
    sectionId: 'advanced',
    labelKey: L('additions.claudeHeaderTimezone.label'),
    hintKey: L('additions.claudeHeaderTimezone.hint'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'timezone'],
  },
  {
    fieldId: 'claudeModelLevelCooling',
    sectionId: 'advanced',
    labelKey: L('additions.claudeModelLevelCooling.label'),
    hintKey: L('additions.claudeModelLevelCooling.hint'),
    yamlKeys: ['upstream', 'claude', 'model-level-cooling'],
  },
  {
    fieldId: 'claudeDisableCloakMode',
    sectionId: 'advanced',
    labelKey: L('additions.claudeDisableCloakMode.label'),
    hintKey: L('additions.claudeDisableCloakMode.hint'),
    yamlKeys: ['upstream', 'claude', 'disable-claude-cloak-mode'],
  },
  {
    fieldId: 'claudeCodeDisableCloakingModelList',
    sectionId: 'advanced',
    labelKey: L('additions.claudeCodeDisableCloakingModelList.label'),
    hintKey: L('additions.claudeCodeDisableCloakingModelList.hint'),
    yamlKeys: ['upstream', 'claude', 'disable-cloaking-model-list'],
  },
  {
    fieldId: 'codexDisableCloaking',
    sectionId: 'advanced',
    labelKey: L('additions.codexDisableCloaking.label'),
    hintKey: L('additions.codexDisableCloaking.hint'),
    yamlKeys: ['upstream', 'codex', 'disable-codex-cloaking'],
  },
  {
    fieldId: 'codexModelLevelCooling',
    sectionId: 'advanced',
    labelKey: L('additions.codexModelLevelCooling.label'),
    hintKey: L('additions.codexModelLevelCooling.hint'),
    yamlKeys: ['upstream', 'codex', 'model-level-cooling'],
  },
  {
    fieldId: 'codexStreamBootstrapBuffering',
    sectionId: 'advanced',
    labelKey: L('additions.codexStreamBootstrapBuffering.label'),
    hintKey: L('additions.codexStreamBootstrapBuffering.hint'),
    yamlKeys: ['upstream', 'codex', 'stream-bootstrap-buffering'],
  },
  {
    fieldId: 'codexStreamBootstrapTimeout',
    sectionId: 'advanced',
    labelKey: L('additions.codexStreamBootstrapTimeout.label'),
    hintKey: L('additions.codexStreamBootstrapTimeout.hint'),
    yamlKeys: ['upstream', 'codex', 'stream-bootstrap-timeout'],
  },
  {
    fieldId: 'codexOptimizeMultiAgentV2',
    sectionId: 'advanced',
    labelKey: L('additions.codexOptimizeMultiAgentV2.label'),
    hintKey: L('additions.codexOptimizeMultiAgentV2.hint'),
    yamlKeys: ['client', 'codex', 'optimize-multi-agent-v2'],
  },
  {
    fieldId: 'codexOrphanDelegationCompatibility',
    sectionId: 'advanced',
    labelKey: L('additions.codexOrphanDelegationCompatibility.label'),
    hintKey: L('additions.codexOrphanDelegationCompatibility.hint'),
    yamlKeys: ['upstream', 'codex', 'orphan-delegation-compatibility'],
  },
  {
    fieldId: 'codexResponseSteering',
    sectionId: 'advanced',
    labelKey: L('additions.codexResponseSteering.label'),
    hintKey: L('additions.codexResponseSteering.hint'),
    yamlKeys: ['upstream', 'codex', 'response-steering'],
  },
  {
    fieldId: 'antigravityConnectionPoolEnabled',
    sectionId: 'advanced',
    labelKey: L('additions.antigravityConnectionPoolEnabled.label'),
    hintKey: L('additions.antigravityConnectionPoolEnabled.hint'),
    yamlKeys: ['oauth', 'providers', 'antigravity', 'connection-pool', 'enabled'],
  },
  {
    fieldId: 'antigravityConnectionPoolIdleTimeout',
    sectionId: 'advanced',
    labelKey: L('additions.antigravityConnectionPoolIdleTimeout.label'),
    hintKey: L('additions.antigravityConnectionPoolIdleTimeout.hint'),
    yamlKeys: ['oauth', 'providers', 'antigravity', 'connection-pool', 'idle-conn-timeout'],
  },
  {
    fieldId: 'antigravityConnectionPoolMaxIdleConnsPerHost',
    sectionId: 'advanced',
    labelKey: L('additions.antigravityConnectionPoolMaxIdleConnsPerHost.label'),
    hintKey: L('additions.antigravityConnectionPoolMaxIdleConnsPerHost.hint'),
    yamlKeys: ['oauth', 'providers', 'antigravity', 'connection-pool', 'max-idle-conns-per-host'],
  },
  {
    fieldId: 'xaiInjectXSearch',
    sectionId: 'advanced',
    labelKey: L('additions.xaiInjectXSearch.label'),
    hintKey: L('additions.xaiInjectXSearch.hint'),
    yamlKeys: ['upstream', 'xai', 'inject-x-search'],
  },
  {
    fieldId: 'codexLiveMediaRelayEnabled',
    sectionId: 'advanced',
    labelKey: L('additions.codexLiveMediaRelayEnabled.label'),
    hintKey: L('additions.codexLiveMediaRelayEnabled.hint'),
    yamlKeys: ['oauth', 'providers', 'codex', 'live-media-relay', 'enabled'],
  },
  {
    fieldId: 'codexLiveMediaRelayMaxSessions',
    sectionId: 'advanced',
    labelKey: L('additions.codexLiveMediaRelayMaxSessions.label'),
    hintKey: L('additions.codexLiveMediaRelayMaxSessions.hint'),
    yamlKeys: ['oauth', 'providers', 'codex', 'live-media-relay', 'max-sessions'],
  },
  {
    fieldId: 'codexLiveMediaRelayDisablePrivateRemoteIPs',
    sectionId: 'advanced',
    labelKey: L('additions.codexLiveMediaRelayDisablePrivateRemoteIPs.label'),
    hintKey: L('additions.codexLiveMediaRelayDisablePrivateRemoteIPs.hint'),
    yamlKeys: ['oauth', 'providers', 'codex', 'live-media-relay', 'disable-private-remote-ips'],
  },
  {
    fieldId: 'codexLiveMediaRelayPublicIP',
    sectionId: 'advanced',
    labelKey: L('additions.codexLiveMediaRelayPublicIP.label'),
    hintKey: L('additions.codexLiveMediaRelayPublicIP.hint'),
    yamlKeys: ['oauth', 'providers', 'codex', 'live-media-relay', 'public-ip'],
  },
  {
    fieldId: 'codexLiveMediaRelayUDPPortMin',
    sectionId: 'advanced',
    labelKey: L('additions.codexLiveMediaRelayUDPPortMin.label'),
    hintKey: L('additions.codexLiveMediaRelayUDPPortMin.hint'),
    yamlKeys: ['oauth', 'providers', 'codex', 'live-media-relay', 'udp-port-min'],
  },
  {
    fieldId: 'codexLiveMediaRelayUDPPortMax',
    sectionId: 'advanced',
    labelKey: L('additions.codexLiveMediaRelayUDPPortMax.label'),
    hintKey: L('additions.codexLiveMediaRelayUDPPortMax.hint'),
    yamlKeys: ['oauth', 'providers', 'codex', 'live-media-relay', 'udp-port-max'],
  },
  {
    fieldId: 'codexLiveMediaRelayICEServers',
    sectionId: 'advanced',
    labelKey: L('additions.codexLiveMediaRelayICEServers.label'),
    hintKey: L('additions.codexLiveMediaRelayICEServers.hint'),
    yamlKeys: ['oauth', 'providers', 'codex', 'live-media-relay', 'ice-servers'],
    keywords: ['stun', 'turn', 'urls', 'username', 'credential'],
  },

  // ── connectivity ──────────────────────────────────────────────────────────
  {
    fieldId: 'githubToken',
    sectionId: 'connectivity',
    labelKey: L('sections.server.github_token'),
    hintKey: L('sections.server.github_token_hint'),
    yamlKeys: ['server', 'github-token'],
    keywords: ['GITHUB_TOKEN', 'GitHub', 'token'],
  },
  {
    fieldId: 'trustedProxies',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.trustedProxies.label'),
    hintKey: L('serverExtras.trustedProxies.hint'),
    yamlKeys: ['server', 'trusted-proxies'],
  },
  {
    fieldId: 'discoveryEnabled',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoveryEnabled.label'),
    hintKey: L('serverExtras.discoveryEnabled.hint'),
    yamlKeys: ['server', 'discovery', 'enabled'],
  },
  {
    fieldId: 'discoveryServiceName',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoveryServiceName.label'),
    hintKey: L('serverExtras.discoveryServiceName.hint'),
    yamlKeys: ['server', 'discovery', 'service-name'],
  },
  {
    fieldId: 'discoveryServiceType',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoveryServiceType.label'),
    hintKey: L('serverExtras.discoveryServiceType.hint'),
    yamlKeys: ['server', 'discovery', 'service-type'],
  },
  {
    fieldId: 'discoverySubtypes',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoverySubtypes.label'),
    hintKey: L('serverExtras.discoverySubtypes.hint'),
    yamlKeys: ['server', 'discovery', 'subtypes'],
  },
  {
    fieldId: 'discoveryInterfacesInclude',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoveryInterfacesInclude.label'),
    hintKey: L('serverExtras.discoveryInterfacesInclude.hint'),
    yamlKeys: ['server', 'discovery', 'interfaces', 'include'],
  },
  {
    fieldId: 'discoveryInterfacesExclude',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoveryInterfacesExclude.label'),
    hintKey: L('serverExtras.discoveryInterfacesExclude.hint'),
    yamlKeys: ['server', 'discovery', 'interfaces', 'exclude'],
  },
  {
    fieldId: 'discoveryAuthRequired',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoveryAuthRequired.label'),
    hintKey: L('serverExtras.discoveryAuthRequired.hint'),
    yamlKeys: ['server', 'discovery', 'auth-required'],
  },
  {
    fieldId: 'discoveryAdvertiseManagement',
    sectionId: 'connectivity',
    labelKey: L('serverExtras.discoveryAdvertiseManagement.label'),
    hintKey: L('serverExtras.discoveryAdvertiseManagement.hint'),
    yamlKeys: ['server', 'discovery', 'advertise-management'],
  },
  {
    fieldId: 'host',
    sectionId: 'connectivity',
    labelKey: L('sections.server.host'),
    yamlKeys: ['server', 'host'],
  },
  {
    fieldId: 'port',
    sectionId: 'connectivity',
    labelKey: L('sections.server.port'),
    yamlKeys: ['server', 'port'],
  },
  {
    fieldId: 'authDir',
    sectionId: 'connectivity',
    labelKey: L('sections.auth.auth_dir'),
    hintKey: L('sections.auth.auth_dir_hint'),
    yamlKeys: ['oauth', 'auth-dir'],
  },
  {
    fieldId: 'apiKeys',
    sectionId: 'connectivity',
    labelKey: L('api_keys.label'),
    yamlKeys: ['access', 'api-keys'],
    keywords: ['api key', 'apikey', 'token'],
  },
  {
    fieldId: 'tlsEnable',
    sectionId: 'connectivity',
    labelKey: L('sections.tls.enable'),
    hintKey: L('sections.tls.enable_desc'),
    yamlKeys: ['server', 'tls'],
    keywords: ['tls', 'ssl', 'https'],
  },
  {
    fieldId: 'tlsCert',
    sectionId: 'connectivity',
    labelKey: L('sections.tls.cert'),
    yamlKeys: ['server', 'tls', 'cert'],
    keywords: ['tls', 'ssl', 'certificate'],
  },
  {
    fieldId: 'tlsKey',
    sectionId: 'connectivity',
    labelKey: L('sections.tls.key'),
    yamlKeys: ['server', 'tls', 'key'],
    keywords: ['tls', 'ssl', 'private key'],
  },
  {
    fieldId: 'rmAllowRemote',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.allow_remote'),
    hintKey: L('sections.remote.allow_remote_desc'),
    yamlKeys: ['management', 'allow-remote'],
  },
  {
    fieldId: 'rmDisableControlPanel',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.disable_panel'),
    yamlKeys: ['management', 'disable-control-panel'],
  },
  {
    fieldId: 'rmDisableAutoUpdatePanel',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.disable_auto_update_panel'),
    yamlKeys: ['management', 'disable-auto-update-panel'],
  },
  {
    fieldId: 'rmSecretKey',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.secret_key'),
    yamlKeys: ['management', 'secret-key'],
  },
  {
    fieldId: 'rmPanelRepo',
    sectionId: 'connectivity',
    labelKey: L('sections.remote.panel_repo'),
    yamlKeys: ['management', 'panel-github-repository'],
  },
  // ── network ───────────────────────────────────────────────────────────────
  {
    fieldId: 'proxyUrl',
    sectionId: 'network',
    labelKey: L('sections.network.proxy_url'),
    yamlKeys: ['requests', 'proxy-url'],
  },
  {
    fieldId: 'requestRetry',
    sectionId: 'network',
    labelKey: L('sections.network.request_retry'),
    yamlKeys: ['routing', 'retry', 'request-retry'],
  },
  {
    fieldId: 'maxRetryCredentials',
    sectionId: 'network',
    labelKey: L('sections.network.max_retry_credentials'),
    hintKey: L('sections.network.max_retry_credentials_hint'),
    yamlKeys: ['routing', 'retry', 'max-retry-credentials'],
  },
  {
    fieldId: 'maxRetryInterval',
    sectionId: 'network',
    labelKey: L('sections.network.max_retry_interval'),
    hintKey: L('sections.network.max_retry_interval_hint'),
    yamlKeys: ['routing', 'retry', 'max-retry-interval'],
  },
  {
    fieldId: 'authAutoRefreshWorkers',
    sectionId: 'network',
    labelKey: L('sections.network.auth_auto_refresh_workers'),
    hintKey: L('sections.network.auth_auto_refresh_workers_hint'),
    yamlKeys: ['oauth', 'auth-auto-refresh-workers'],
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
    yamlKeys: ['multimedia', 'disable-image-generation'],
    keywords: ['false', 'true', 'chat', 'passthrough'],
  },
  {
    fieldId: 'gptImage2BaseModel',
    sectionId: 'network',
    labelKey: L('sections.network.gpt_image_2_base_model'),
    hintKey: L('sections.network.gpt_image_2_base_model_hint'),
    yamlKeys: ['multimedia', 'gpt-image-2-base-model'],
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
    yamlKeys: ['routing', 'force-model-prefix'],
  },
  {
    fieldId: 'passthroughHeaders',
    sectionId: 'network',
    labelKey: L('sections.network.passthrough_headers'),
    hintKey: L('sections.network.passthrough_headers_desc'),
    yamlKeys: ['requests', 'passthrough-headers'],
  },
  {
    fieldId: 'disableCooling',
    sectionId: 'network',
    labelKey: L('sections.network.disable_cooling'),
    hintKey: L('sections.network.disable_cooling_desc'),
    yamlKeys: ['routing', 'cooldown', 'disable-cooling'],
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
    yamlKeys: ['oauth', 'providers', 'aistudio', 'ws-auth'],
    keywords: ['websocket'],
  },
  // ── logging ───────────────────────────────────────────────────────────────
  {
    fieldId: 'debug',
    sectionId: 'logging',
    labelKey: L('sections.system.debug'),
    hintKey: L('sections.system.debug_desc'),
    yamlKeys: ['observability', 'logs', 'debug'],
  },
  {
    fieldId: 'commercialMode',
    sectionId: 'logging',
    labelKey: L('sections.system.commercial_mode'),
    hintKey: L('sections.system.commercial_mode_desc'),
    yamlKeys: ['server', 'commercial-mode'],
  },
  {
    fieldId: 'loggingToFile',
    sectionId: 'logging',
    labelKey: L('sections.system.logging_to_file'),
    hintKey: L('sections.system.logging_to_file_desc'),
    yamlKeys: ['observability', 'logs', 'logging-to-file'],
  },
  {
    fieldId: 'logsMaxTotalSizeMb',
    sectionId: 'logging',
    labelKey: L('sections.system.logs_max_size'),
    yamlKeys: ['observability', 'logs', 'logs-max-total-size-mb'],
  },
  {
    fieldId: 'errorLogsMaxFiles',
    sectionId: 'logging',
    labelKey: L('sections.system.error_logs_max_files'),
    yamlKeys: ['observability', 'logs', 'error-logs-max-files'],
  },
  {
    fieldId: 'redisUsageQueueRetentionSeconds',
    sectionId: 'logging',
    labelKey: L('sections.system.redis_usage_retention'),
    hintKey: L('sections.system.redis_usage_retention_hint'),
    yamlKeys: ['observability', 'usage', 'redis-usage-queue-retention-seconds'],
  },
  {
    fieldId: 'usageStatisticsEnabled',
    sectionId: 'logging',
    labelKey: L('sections.system.usage_statistics_enabled'),
    hintKey: L('sections.system.usage_statistics_enabled_desc'),
    yamlKeys: ['observability', 'usage', 'usage-statistics-enabled'],
  },
  // ── streaming ─────────────────────────────────────────────────────────────
  {
    fieldId: 'streamingKeepaliveSeconds',
    sectionId: 'streaming',
    labelKey: L('sections.streaming.keepalive_seconds'),
    hintKey: L('sections.streaming.keepalive_hint'),
    yamlKeys: ['requests', 'streaming', 'keepalive-seconds'],
  },
  {
    fieldId: 'streamingBootstrapRetries',
    sectionId: 'streaming',
    labelKey: L('sections.streaming.bootstrap_retries'),
    hintKey: L('sections.streaming.bootstrap_hint'),
    yamlKeys: ['requests', 'streaming', 'bootstrap-retries'],
  },
  {
    fieldId: 'streamingNonstreamKeepalive',
    sectionId: 'streaming',
    labelKey: L('sections.streaming.nonstream_keepalive'),
    hintKey: L('sections.streaming.nonstream_keepalive_hint'),
    yamlKeys: ['requests', 'nonstream-keepalive-interval'],
  },
  // ── advanced ──────────────────────────────────────────────────────────────
  {
    fieldId: 'quotaAntigravityCredits',
    sectionId: 'advanced',
    labelKey: L('sections.advanced.antigravity_credits'),
    yamlKeys: ['oauth', 'providers', 'antigravity', 'antigravity-credits'],
  },
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
    yamlKeys: ['oauth', 'providers', 'antigravity', 'sensitive-words'],
    keywords: ['antigravity', 'obfuscate', 'zero-width'],
  },
  {
    fieldId: 'devinSensitiveWords',
    sectionId: 'advanced',
    labelKey: L('sections.system.devin_sensitive_words'),
    hintKey: L('sections.system.devin_sensitive_words_desc'),
    yamlKeys: ['oauth', 'providers', 'devin', 'sensitive-words'],
    keywords: ['devin', 'system prompt', 'remove line', 'obfuscate', 'zero-width'],
  },
  {
    fieldId: 'antigravitySignatureCacheEnabled',
    sectionId: 'advanced',
    labelKey: L('sections.system.antigravity_signature_cache'),
    hintKey: L('sections.system.antigravity_signature_cache_desc'),
    yamlKeys: ['oauth', 'providers', 'antigravity', 'signature-cache-enabled'],
  },
  {
    fieldId: 'antigravitySignatureBypassStrict',
    sectionId: 'advanced',
    labelKey: L('sections.system.antigravity_signature_strict'),
    hintKey: L('sections.system.antigravity_signature_strict_desc'),
    yamlKeys: ['oauth', 'providers', 'antigravity', 'signature-bypass-strict'],
  },
  // Claude header defaults — qualifierKey disambiguates the shared "User-Agent" label.
  {
    fieldId: 'claudeHeaderUserAgent',
    sectionId: 'advanced',
    labelKey: L('sections.headers.user_agent'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'user-agent'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderPackageVersion',
    sectionId: 'advanced',
    labelKey: L('sections.headers.package_version'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'package-version'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderRuntimeVersion',
    sectionId: 'advanced',
    labelKey: L('sections.headers.runtime_version'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'runtime-version'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderOs',
    sectionId: 'advanced',
    labelKey: L('sections.headers.os'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'os'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderArch',
    sectionId: 'advanced',
    labelKey: L('sections.headers.arch'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'arch'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderTimeout',
    sectionId: 'advanced',
    labelKey: L('sections.headers.timeout'),
    qualifierKey: L('sections.headers.claude_title'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'timeout'],
    keywords: ['claude'],
  },
  {
    fieldId: 'claudeHeaderStabilizeDeviceProfile',
    sectionId: 'advanced',
    labelKey: L('sections.headers.stabilize_device'),
    qualifierKey: L('sections.headers.claude_title'),
    hintKey: L('sections.headers.stabilize_device_desc'),
    yamlKeys: ['upstream', 'claude', 'header-defaults', 'stabilize-device-profile'],
    keywords: ['claude'],
  },
  // Codex header defaults.
  {
    fieldId: 'codexHeaderUserAgent',
    sectionId: 'advanced',
    labelKey: L('sections.headers.user_agent'),
    qualifierKey: L('sections.headers.codex_title'),
    yamlKeys: ['oauth', 'providers', 'codex', 'header-defaults', 'user-agent'],
    keywords: ['codex'],
  },
  {
    fieldId: 'codexHeaderBetaFeatures',
    sectionId: 'advanced',
    labelKey: L('sections.headers.beta_features'),
    qualifierKey: L('sections.headers.codex_title'),
    yamlKeys: ['oauth', 'providers', 'codex', 'header-defaults', 'beta-features'],
    keywords: ['codex'],
  },
  // ── payload (coarse: one entry per rule group) ──────────────────────────────
  {
    fieldId: 'payloadDefaultRules',
    yamlKeys: ['requests', 'payload', 'default'],
    sectionId: 'payload',
    labelKey: L('sections.payload.default_rules'),
    hintKey: L('sections.payload.default_rules_desc'),
    keywords: ['payload', 'rule'],
  },
  {
    fieldId: 'payloadDefaultRawRules',
    yamlKeys: ['requests', 'payload', 'default-raw'],
    sectionId: 'payload',
    labelKey: L('sections.payload.default_raw_rules'),
    hintKey: L('sections.payload.default_raw_rules_desc'),
    keywords: ['payload', 'rule', 'json'],
  },
  {
    fieldId: 'payloadOverrideRules',
    yamlKeys: ['requests', 'payload', 'override'],
    sectionId: 'payload',
    labelKey: L('sections.payload.override_rules'),
    hintKey: L('sections.payload.override_rules_desc'),
    keywords: ['payload', 'rule'],
  },
  {
    fieldId: 'payloadOverrideRawRules',
    yamlKeys: ['requests', 'payload', 'override-raw'],
    sectionId: 'payload',
    labelKey: L('sections.payload.override_raw_rules'),
    hintKey: L('sections.payload.override_raw_rules_desc'),
    keywords: ['payload', 'rule', 'json'],
  },
  {
    fieldId: 'payloadFilterRules',
    yamlKeys: ['requests', 'payload', 'filter'],
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
    const yaml = (entry.yamlKeys ?? []).join('.').toLowerCase();
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
