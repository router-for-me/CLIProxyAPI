export type PayloadParamValueType = 'string' | 'number' | 'boolean' | 'json';
export type DisableImageGenerationMode = 'false' | 'true' | 'chat' | 'passthrough';
export type RoutingStrategy = 'round-robin' | 'weighted-round-robin' | 'fill-first';
export type PluginStoreAuthType = 'none' | 'bearer' | 'basic' | 'header' | 'github-token';
export type PluginStoreAuthApplyTo = 'registry' | 'metadata' | 'artifact';
export type PayloadParamValidationErrorCode =
  'payload_invalid_number' | 'payload_invalid_boolean' | 'payload_invalid_json';

export type CodexLiveICEServerDraft = {
  id: string;
  urlsText: string;
  username: string;
  credential: string;
};

export type VisualConfigFieldPath =
  | 'trustedProxies'
  | 'discoveryServiceType'
  | 'transientErrorCooldownSeconds'
  | 'videoResultAuthCacheTTL'
  | 'claudeHeaderTimezone'
  | 'codexStreamBootstrapTimeout'
  | 'antigravityConnectionPoolIdleTimeout'
  | 'antigravityConnectionPoolMaxIdleConnsPerHost'
  | 'codexLiveMediaRelayMaxSessions'
  | 'codexLiveMediaRelayPublicIP'
  | 'codexLiveMediaRelayUDPPortMin'
  | 'codexLiveMediaRelayUDPPortMax'
  | 'codexLiveMediaRelayICEServers'
  | 'port'
  | 'errorLogsMaxFiles'
  | 'logsMaxTotalSizeMb'
  | 'redisUsageQueueRetentionSeconds'
  | 'requestRetry'
  | 'maxRetryCredentials'
  | 'maxRetryInterval'
  | 'authAutoRefreshWorkers'
  | 'streaming.keepaliveSeconds'
  | 'streaming.bootstrapRetries'
  | 'streaming.nonstreamKeepaliveInterval';

export type VisualConfigValidationErrorCode =
  | 'invalid_trusted_proxies'
  | 'invalid_discovery_service_type'
  | 'invalid_duration'
  | 'positive_duration'
  | 'invalid_timezone'
  | 'invalid_ip'
  | 'udp_port_pair'
  | 'udp_port_capacity'
  | 'invalid_ice_servers'
  | 'integer_range_0_65535'
  | 'port_range'
  | 'integer'
  | 'non_negative_integer'
  | 'integer_range_1_3600';

export type VisualConfigValidationErrors = Partial<
  Record<VisualConfigFieldPath, VisualConfigValidationErrorCode>
>;

export type PayloadParamEntry = {
  id: string;
  path: string;
  valueType: PayloadParamValueType;
  value: string;
};

export type PayloadHeaderEntry = {
  id: string;
  name: string;
  value: string;
};

export type PayloadModelEntry = {
  id: string;
  name: string;
  protocol?: string;
  fromProtocol?: string;
  headers?: PayloadHeaderEntry[];
  match?: PayloadParamEntry[];
  notMatch?: PayloadParamEntry[];
  exist?: string[];
  notExist?: string[];
};

export type PayloadRule = {
  id: string;
  models: PayloadModelEntry[];
  params: PayloadParamEntry[];
};

export type PayloadFilterRule = {
  id: string;
  models: PayloadModelEntry[];
  params: string[];
};

export interface StreamingConfig {
  keepaliveSeconds: string;
  bootstrapRetries: string;
  nonstreamKeepaliveInterval: string;
}

export type PluginStoreAuthRule = {
  id: string;
  match: string;
  applyTo: PluginStoreAuthApplyTo[];
  type: PluginStoreAuthType;
  tokenEnv: string;
  usernameEnv: string;
  passwordEnv: string;
  headerName: string;
  headerValueEnv: string;
  allowInsecure: boolean;
};

/** UI draft keys; YAML persistence uses the v8 tree, not these flattened names. */
export type VisualConfigValues = {
  githubToken: string;
  trustedProxies: string[];
  discoveryEnabled: boolean;
  discoveryServiceName: string;
  discoveryServiceType: string;
  discoverySubtypes: string[];
  discoveryInterfacesInclude: string[];
  discoveryInterfacesExclude: string[];
  discoveryAuthRequired: boolean;
  discoveryAdvertiseManagement: boolean;

  routingSessionAffinitySubagents: boolean;
  saveCooldownStatus: boolean;
  transientErrorCooldownSeconds: string;
  videoResultAuthCacheTTL: string;
  claudeHeaderTimezone: string;
  claudeModelLevelCooling: boolean;
  claudeDisableCloakMode: boolean;
  claudeCodeDisableCloakingModelList: boolean;
  codexDisableCloaking: boolean;
  codexModelLevelCooling: boolean;
  codexStreamBootstrapBuffering: boolean;
  codexStreamBootstrapTimeout: string;
  codexOptimizeMultiAgentV2: boolean;
  codexOrphanDelegationCompatibility: boolean;
  codexResponseSteering: boolean;
  antigravityConnectionPoolEnabled: boolean;
  antigravityConnectionPoolIdleTimeout: string;
  antigravityConnectionPoolMaxIdleConnsPerHost: string;
  xaiInjectXSearch: boolean;
  codexLiveMediaRelayEnabled: boolean;
  codexLiveMediaRelayMaxSessions: string;
  codexLiveMediaRelayDisablePrivateRemoteIPs: boolean;
  codexLiveMediaRelayPublicIP: string;
  codexLiveMediaRelayUDPPortMin: string;
  codexLiveMediaRelayUDPPortMax: string;
  codexLiveMediaRelayICEServers: CodexLiveICEServerDraft[];
  host: string;
  port: string;
  tlsEnable: boolean;
  tlsCert: string;
  tlsKey: string;
  rmAllowRemote: boolean;
  rmSecretKey: string;
  rmDisableControlPanel: boolean;
  rmDisableAutoUpdatePanel: boolean;
  rmPanelRepo: string;
  authDir: string;
  /** Client authentication keys at access.api-keys (never the upstream api-keys map). */
  apiKeysText: string;
  pluginsEnabled: boolean;
  pluginStoreSources: string[];
  pluginStoreAuth: PluginStoreAuthRule[];
  debug: boolean;
  commercialMode: boolean;
  loggingToFile: boolean;
  logsMaxTotalSizeMb: string;
  errorLogsMaxFiles: string;
  usageStatisticsEnabled: boolean;
  redisUsageQueueRetentionSeconds: string;
  proxyUrl: string;
  forceModelPrefix: boolean;
  passthroughHeaders: boolean;
  requestRetry: string;
  maxRetryCredentials: string;
  maxRetryInterval: string;
  disableCooling: boolean;
  disableImageGeneration: DisableImageGenerationMode;
  gptImage2BaseModel: string;
  authAutoRefreshWorkers: string;
  /** OAuth-only: oauth.providers.antigravity.antigravity-credits. */
  quotaAntigravityCredits: boolean;
  routingStrategy: RoutingStrategy;
  routingSessionAffinity: boolean;
  routingSessionAffinityTTL: string;
  wsAuth: boolean;
  antigravitySensitiveWords: string[];
  devinSensitiveWords: string[];
  antigravitySignatureCacheEnabled: boolean;
  antigravitySignatureBypassStrict: boolean;
  claudeHeaderUserAgent: string;
  claudeHeaderPackageVersion: string;
  claudeHeaderRuntimeVersion: string;
  claudeHeaderOs: string;
  claudeHeaderArch: string;
  claudeHeaderTimeout: string;
  claudeHeaderStabilizeDeviceProfile: boolean;
  codexHeaderUserAgent: string;
  codexHeaderBetaFeatures: string;
  payloadDefaultRules: PayloadRule[];
  payloadDefaultRawRules: PayloadRule[];
  payloadOverrideRules: PayloadRule[];
  payloadOverrideRawRules: PayloadRule[];
  payloadFilterRules: PayloadFilterRule[];
  streaming: StreamingConfig;
};

export const makeClientId = () => {
  if (typeof globalThis.crypto?.randomUUID === 'function') return globalThis.crypto.randomUUID();
  return `${Date.now().toString(36)}_${Math.random().toString(36).slice(2, 10)}`;
};

export const DEFAULT_VISUAL_VALUES: VisualConfigValues = {
  githubToken: '',
  trustedProxies: [],
  discoveryEnabled: false,
  discoveryServiceName: '',
  discoveryServiceType: '',
  discoverySubtypes: [],
  discoveryInterfacesInclude: [],
  discoveryInterfacesExclude: [],
  discoveryAuthRequired: true,
  discoveryAdvertiseManagement: false,

  routingSessionAffinitySubagents: true,
  saveCooldownStatus: false,
  transientErrorCooldownSeconds: '',
  videoResultAuthCacheTTL: '',
  claudeHeaderTimezone: '',
  claudeModelLevelCooling: false,
  claudeDisableCloakMode: false,
  claudeCodeDisableCloakingModelList: false,
  codexDisableCloaking: false,
  codexModelLevelCooling: false,
  codexStreamBootstrapBuffering: false,
  codexStreamBootstrapTimeout: '',
  codexOptimizeMultiAgentV2: false,
  codexOrphanDelegationCompatibility: false,
  codexResponseSteering: false,
  antigravityConnectionPoolEnabled: false,
  antigravityConnectionPoolIdleTimeout: '',
  antigravityConnectionPoolMaxIdleConnsPerHost: '',
  xaiInjectXSearch: false,
  codexLiveMediaRelayEnabled: false,
  codexLiveMediaRelayMaxSessions: '',
  codexLiveMediaRelayDisablePrivateRemoteIPs: false,
  codexLiveMediaRelayPublicIP: '',
  codexLiveMediaRelayUDPPortMin: '',
  codexLiveMediaRelayUDPPortMax: '',
  codexLiveMediaRelayICEServers: [],
  host: '',
  port: '',
  tlsEnable: false,
  tlsCert: '',
  tlsKey: '',
  rmAllowRemote: false,
  rmSecretKey: '',
  rmDisableControlPanel: false,
  rmDisableAutoUpdatePanel: false,
  rmPanelRepo: '',
  authDir: '',
  apiKeysText: '',
  pluginsEnabled: false,
  pluginStoreSources: [],
  pluginStoreAuth: [],
  debug: false,
  commercialMode: false,
  loggingToFile: false,
  logsMaxTotalSizeMb: '',
  errorLogsMaxFiles: '',
  usageStatisticsEnabled: false,
  redisUsageQueueRetentionSeconds: '',
  proxyUrl: '',
  forceModelPrefix: false,
  passthroughHeaders: false,
  requestRetry: '',
  maxRetryCredentials: '',
  maxRetryInterval: '',
  disableCooling: false,
  disableImageGeneration: 'false',
  gptImage2BaseModel: '',
  authAutoRefreshWorkers: '',
  quotaAntigravityCredits: false,
  routingStrategy: 'round-robin',
  routingSessionAffinity: false,
  routingSessionAffinityTTL: '',
  wsAuth: true,
  antigravitySensitiveWords: [],
  devinSensitiveWords: [],
  antigravitySignatureCacheEnabled: true,
  antigravitySignatureBypassStrict: false,
  claudeHeaderUserAgent: '',
  claudeHeaderPackageVersion: '',
  claudeHeaderRuntimeVersion: '',
  claudeHeaderOs: '',
  claudeHeaderArch: '',
  claudeHeaderTimeout: '',
  claudeHeaderStabilizeDeviceProfile: false,
  codexHeaderUserAgent: '',
  codexHeaderBetaFeatures: '',
  payloadDefaultRules: [],
  payloadDefaultRawRules: [],
  payloadOverrideRules: [],
  payloadOverrideRawRules: [],
  payloadFilterRules: [],
  streaming: {
    keepaliveSeconds: '',
    bootstrapRetries: '',
    nonstreamKeepaliveInterval: '',
  },
};
