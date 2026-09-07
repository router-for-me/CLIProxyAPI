// ============================================================================
// Upstream provider editor — schema layer
// ============================================================================

// The declarative provider-type catalog and the per-type form schemas for
// the upstream provider editor. Extracted from UpstreamProvidersPage.jsx
// (provider-editor-page plan, Task 1) as the single source of truth for the
// catalog: the list page imports the type constants + isOAuth, the editor
// page (./index.jsx) imports the schemas.

// ============================================================================
// Provider type catalog
// ============================================================================

export const API_KEY_TYPES = [
  { value: 'gemini-api-key', label: 'Gemini (API Key)', simple: 'gemini' },
  { value: 'interactions-api-key', label: 'Interactions (API Key)', simple: 'interactions' },
  { value: 'codex-api-key', label: 'Codex (API Key)', simple: 'codex' },
  { value: 'xai-api-key', label: 'xAI (API Key)', simple: 'xai' },
  { value: 'claude-api-key', label: 'Claude (API Key)', simple: 'claude' },
  { value: 'vertex-api-key', label: 'Vertex (API Key)', simple: 'vertex' },
  { value: 'openai-compatibility', label: 'OpenAI Compatibility', simple: 'openai' },
  { value: 'opencode-go', label: 'OpenCode Go', simple: 'opencode' },
];
export const OAUTH_TYPES = [
  { value: 'oauth:claude', label: 'Claude (OAuth)', simple: 'claude' },
  { value: 'oauth:codex', label: 'Codex (OAuth)', simple: 'codex' },
  { value: 'oauth:kimi', label: 'Kimi (OAuth)', simple: 'kimi' },
  { value: 'oauth:xai', label: 'xAI (OAuth)', simple: 'xai' },
  { value: 'oauth:vertex', label: 'Vertex (OAuth)', simple: 'vertex' },
  { value: 'oauth:aistudio', label: 'AI Studio (OAuth)', simple: 'aistudio' },
  { value: 'oauth:antigravity', label: 'Antigravity (OAuth)', simple: 'antigravity' },
];
const ALL_TYPES = [...API_KEY_TYPES, ...OAUTH_TYPES];
export const TYPE_LABEL = Object.fromEntries(ALL_TYPES.map((t) => [t.value, t.label]));
export const TYPE_SIMPLE = Object.fromEntries(ALL_TYPES.map((t) => [t.value, t.simple]));

export const isOAuth = (t) => String(t || '').startsWith('oauth:');
export const isOpenAI = (t) => t === 'openai-compatibility';
export const isOpenCodeGo = (t) => t === 'opencode-go';
export const isClaude = (t) => t === 'claude-api-key' || t === 'oauth:claude';

// OPENCODE_GO_BASE_URL prefills the base URL when the operator picks the
// OpenCode Go type in the create flow.
export const OPENCODE_GO_BASE_URL = 'https://opencode.ai/zen/go/v1';

// WIRE_FORMAT_OPTIONS are the per-model upstream protocols the opencode-go
// executor dispatches on. "openai" is the default (blank falls back to it).
export const WIRE_FORMAT_OPTIONS = [
  { value: '', label: 'openai (default)' },
  { value: 'openai', label: 'openai' },
  { value: 'anthropic', label: 'anthropic' },
];

// URL_RE matches the *start* of a value: a real URL scheme, or the literal
// "direct"/"none" proxy bypass sentinels. Empty values are allowed (they
// mean "use the provider default" for base_url, or "use the global proxy"
// for proxy_url). The $ anchor is intentionally omitted so a full URL like
// "https://api.example.com" passes — only the prefix is checked.
export const URL_RE = /^(https?:\/\/|socks5h?:\/\/|direct|none)/i;

// ROUTING_STRATEGY_OPTIONS mirrors the backend's canonical pool strategies.
// 'failover' and 'priority' are Model-Routes-compatible aliases the backend
// canonicalizes on save; the hint documents the aliasing so the operator is
// not surprised by the round-trip value.
export const ROUTING_STRATEGY_OPTIONS = [
  { value: '', label: 'Default (global)' },
  { value: 'round-robin', label: 'Round-robin' },
  { value: 'weighted-round-robin', label: 'Weighted round-robin' },
  { value: 'fill-first', label: 'Fill-first (priority)' },
  { value: 'failover', label: 'Failover' },
];
export const ROUTING_STRATEGY_HINT = 'Empty = follow the global routing strategy. Any value enables aggressive in-pool failover: on any entry error the next entry is tried first; errors surface only after the whole pool is exhausted. Fill-first ≈ priority, failover ≈ round-robin within a priority tier.';

// MAX_ENTRY_WEIGHT matches config.MaxCredentialWeight (1,000,000). The
// scheduler normalizes anything above that out, but the editor must
// reject impossible values up front so the operator never hits a 400
// from the backend on save.
export const MAX_ENTRY_WEIGHT = 1000000;

// ============================================================================
// Schema definitions (declarative, per provider_type)
// ============================================================================

// buildSchemas returns the per-provider_type form schema map consumed by
// the routed editor page (./index.jsx); also exercised directly by
// ./editor.test.js.
export function buildSchemas() {
  const commonEndpoint = [
    { name: 'base_url', label: 'Base URL', type: 'text', placeholder: 'https://api.example.com',
      hint: 'Upstream API base URL. Leave blank to use the provider default.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
    { name: 'proxy_url', label: 'Proxy URL', type: 'text', placeholder: 'socks5://user:pass@host:1080',
      hint: 'Per-key proxy override. Use "direct"/"none" to bypass global proxy.',
      validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
    { name: 'proxy_pool_id', label: 'Proxy pool', type: 'proxy_pool_id',
      hint: 'Bind this provider to a named proxy pool (Proxy Pools page). Applies to entries without their own pool binding; overrides the manual proxy URL.' },
    { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
      hint: 'Optional namespace prepended to every model this entry serves.' },
  ];

  const commonRouting = [
    { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
      hint: 'Higher value is preferred when multiple credentials match.' },
    { name: 'models', label: 'Models', type: 'models',
      hint: 'Map client-facing aliases to upstream model names.' },
    { name: 'excluded_models', label: 'Excluded models', type: 'chips',
      placeholder: 'model-id or wildcard', hint: 'Models that should never route through this entry. Supports * wildcards.' },
  ];

  const commonBehavior = [
    { name: 'headers', label: 'Custom headers', type: 'headers',
      hint: 'Extra HTTP headers attached to every request using this entry.' },
    { name: 'disabled', label: 'Disabled', type: 'toggle',
      hint: 'When on, this entry is excluded from routing without removing it.' },
    { name: 'disable_cooling', label: 'Disable cooldown', type: 'toggle',
      hint: 'Skip the cooldown schedule when this credential hits an error.' },
  ];

  // API-key provider schemas share most structure. The optional
  // `extraIdentity` fields are prepended to the Identity section (used to add
  // the per-type Identifier field, e.g. for Claude).
  const apiKeyBase = (extraBehavior = [], extraSections = [], extraIdentity = []) => ({
    sections: [
      { title: 'Identity', fields: [
        ...extraIdentity,
        { name: 'api_key', label: 'API key', type: 'password', placeholder: 'sk-…', required: true,
          hint: 'The credential that authenticates requests to this provider.' },
      ]},
      { title: 'Endpoint', fields: commonEndpoint },
      { title: 'Routing', fields: commonRouting, fetchModels: true },
      { title: 'Behavior', fields: [...commonBehavior, ...extraBehavior] },
      ...extraSections,
    ],
  });

  // Identifier field for API-key upstreams (all keyed types). Maps to the
  // generic `name` column, which the dashboard already lists as the
  // first-choice identifier in the provider list; the proxy lower-cases it
  // into the executor/routing provider key (see util.UpstreamProviderKey).
  // Optional — leave blank to keep the auto-derived key (empty name →
  // path-based fallback).
  const identifierField = {
    name: 'name', label: 'Identifier', type: 'text', placeholder: 'team-a-gemini',
    hint: 'Optional stable identifier for this upstream. Lower-cased to form its routing key; shown in the provider list.',
  };

  // routingStrategyField is shared by the two entry-bearing providers
  // (claude-api-key, openai-compatibility): it selects the in-pool selection
  // strategy for the api_key_entries pool.
  const routingStrategyField = {
    name: 'routing_strategy', label: 'Routing strategy', type: 'select',
    options: ROUTING_STRATEGY_OPTIONS, hint: ROUTING_STRATEGY_HINT,
  };

  const claudeCloakSection = {
    title: 'Cloak',
    hint: 'Disguise API requests to appear as the official Claude Code CLI (Claude only).',
    fields: [
      { name: 'cloak_mode', label: 'Cloak mode', type: 'select',
        options: [
          { value: '', label: 'auto (default)' },
          { value: 'always', label: 'always' },
          { value: 'never', label: 'never' },
        ],
        hint: 'auto: cloak only for non-Claude-Code clients. always: always cloak. never: never cloak.' },
      { name: 'cloak_strict_mode', label: 'Strict mode', type: 'toggle',
        hint: 'Strip all user system messages; keep only the Claude Code prompt.' },
      { name: 'cloak_sensitive_words', label: 'Sensitive words', type: 'chips',
        placeholder: 'word to obfuscate', hint: 'Words obfuscated with zero-width characters to bypass content filters.' },
      { name: 'cloak_cache_user_id', label: 'Cache user ID', type: 'toggle',
        hint: 'Cache Claude user_id per API key. Off = fresh random per request.' },
    ],
  };

  const schemaMap = {
    'gemini-api-key': apiKeyBase([], [], [identifierField]),
    'interactions-api-key': apiKeyBase([], [], [identifierField]),
    'codex-api-key': apiKeyBase([
      { name: 'websockets', label: 'WebSockets', type: 'toggle',
        hint: 'Use the Responses API websocket transport for this entry.' },
    ], [], [identifierField]),
    'xai-api-key': apiKeyBase([
      { name: 'websockets', label: 'WebSockets', type: 'toggle',
        hint: 'Use the Responses API websocket transport for this entry.' },
    ], [], [identifierField]),
    'claude-api-key': {
      sections: [
        { title: 'Identity', fields: [
          identifierField,
          // Claude (API Key) shares the multi-row editor with OpenAI
          // Compatibility. Each entry authenticates a single key and is
          // round-robined by the auth manager; the row-level proxy still
          // serves as the default for entries that leave the per-entry
          // override blank.
          { name: 'api_key_entries', label: 'API key entries', type: 'api_key_entries',
            hint: 'Multiple keys form a round-robin pool for this provider. Per-entry proxy overrides the row-level proxy when set.' },
          // Governs the entry pool above — kept adjacent to the editor it
          // controls.
          routingStrategyField,
        ]},
        { title: 'Endpoint', fields: commonEndpoint },
        { title: 'Routing', fields: commonRouting, fetchModels: true },
        { title: 'Behavior', fields: [
          ...commonBehavior,
          { name: 'rebuild_mid_system_message', label: 'Rebuild mid system message', type: 'toggle',
            hint: 'Move role=system messages into the top-level system field.' },
          { name: 'experimental_cch_signing', label: 'Experimental CCH signing', type: 'toggle',
            hint: 'Opt-in final-body cch signing for cloaked Claude /v1/messages requests.' },
        ]},
        claudeCloakSection,
      ],
    },
    'vertex-api-key': apiKeyBase([], [], [identifierField]),
    'openai-compatibility': {
      sections: [
        { title: 'Identity', fields: [
          { name: 'name', label: 'Provider name', type: 'text', placeholder: 'openrouter', required: true,
            hint: 'Unique identifier for this OpenAI-compatible provider.' },
          { name: 'base_url', label: 'Base URL', type: 'text', placeholder: 'https://openrouter.ai/api/v1',
            required: true, hint: 'The external OpenAI-compatible API endpoint. Include /v1 if your upstream requires it; otherwise the server auto-inserts /v1 before /chat/completions and /images/...',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, or socks5://.' : '') },
        ]},
        { title: 'Endpoint', fields: [
          { name: 'proxy_url', label: 'Proxy URL', type: 'text', placeholder: 'direct',
            hint: 'Per-provider proxy override.',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
          { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
            hint: 'Optional namespace prepended to every model this provider serves.' },
        ]},
        { title: 'Routing', fields: [
          { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
            hint: 'Higher value is preferred when multiple providers match.' },
          // In-pool selection strategy for the Behavior api_key_entries pool.
          routingStrategyField,
          { name: 'models', label: 'Models', type: 'openai_models',
            hint: 'Map client-facing aliases to upstream model names. Supports image + modality flags.' },
          { name: 'excluded_models', label: 'Excluded models', type: 'chips',
            placeholder: 'model-id or wildcard', hint: 'Models that should never route through this provider.' },
        ], fetchModels: true },
        { title: 'Behavior', fields: [
          { name: 'headers', label: 'Custom headers', type: 'headers',
            hint: 'Extra HTTP headers attached to every request to this provider.' },
          { name: 'disabled', label: 'Disabled', type: 'toggle',
            hint: 'When on, this provider is excluded from routing.' },
          { name: 'disable_cooling', label: 'Disable cooldown', type: 'toggle',
            hint: 'Skip the cooldown schedule when this provider hits an error.' },
          { name: 'api_key_entries', label: 'API key entries', type: 'api_key_entries',
            hint: 'Multiple keys form a round-robin pool for this provider.' },
        ]},
      ],
    },
    // OpenCode Go mirrors the entry-bearing openai-compatibility shape but
    // always carries a base_url (the OpenCode Zen Go endpoint) and a
    // per-model wire format. The models editor type 'opencode_models' adds
    // the wire-format select; seed/refresh/quota actions are rendered by
    // the editor page for this type.
    'opencode-go': {
      sections: [
        { title: 'Identity', fields: [
          { name: 'name', label: 'Provider name', type: 'text', placeholder: 'opencode-go-1', required: true,
            hint: 'Unique identifier for this OpenCode Go account.' },
          { name: 'base_url', label: 'Base URL', type: 'text', placeholder: OPENCODE_GO_BASE_URL,
            required: true, hint: 'The OpenCode Zen Go endpoint. Models are dispatched per wire format (openai /chat/completions, anthropic /v1/messages).',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, or socks5://.' : '') },
        ]},
        { title: 'Endpoint', fields: [
          { name: 'proxy_url', label: 'Proxy URL', type: 'text', placeholder: 'direct',
            hint: 'Per-provider proxy override.',
            validate: (v) => (v && !URL_RE.test(v) ? 'Must start with http://, https://, socks5://, or be "direct"/"none".' : '') },
          { name: 'proxy_pool_id', label: 'Proxy pool', type: 'proxy_pool_id',
            hint: 'Bind this provider to a named proxy pool (Proxy Pools page).' },
          { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
            hint: 'Optional namespace prepended to every model this provider serves.' },
        ]},
        { title: 'Routing', fields: [
          { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
            hint: 'Higher value is preferred when multiple providers match.' },
          routingStrategyField,
          { name: 'models', label: 'Models', type: 'opencode_models',
            hint: 'Upstream models. Each row picks its wire format; anthropic models execute via /v1/messages.' },
          { name: 'excluded_models', label: 'Excluded models', type: 'chips',
            placeholder: 'model-id or wildcard', hint: 'Models that should never route through this provider.' },
        ], fetchModels: true },
        { title: 'Behavior', fields: [
          { name: 'headers', label: 'Custom headers', type: 'headers',
            hint: 'Extra HTTP headers attached to every request to this provider.' },
          { name: 'disabled', label: 'Disabled', type: 'toggle',
            hint: 'When on, this provider is excluded from routing.' },
          { name: 'disable_cooling', label: 'Disable cooldown', type: 'toggle',
            hint: 'Skip the cooldown schedule when this provider hits an error.' },
          { name: 'quota_url', label: 'Quota URL', type: 'text', placeholder: 'https://opencode.ai/zen/go/v1/quota',
            hint: 'Override for the manual per-entry quota probe. Default uses the OpenCode Zen Go quota endpoint (currently not live upstream — the probe fails open with an explanation).' },
          { name: 'api_key_entries', label: 'API key entries', type: 'api_key_entries',
            hint: 'Multiple keys form a round-robin pool. Each entry gets a manual Quota check button.' },
        ]},
      ],
    },
  };

  // OAuth provider schemas — share a common identity/token/behavior shape,
  // with cloak + identifier added for oauth:claude.
  for (const oauthType of OAUTH_TYPES.map((t) => t.value)) {
    // For oauth:claude an Identifier is also surfaced — it populates the
    // generic `name` column shown in the provider list (the executor key for
    // oauth:* is fixed to the channel, so it stays "claude" regardless).
    const identityFields = oauthType === 'oauth:claude' ? [identifierField] : [];
    const sections = [
      { title: 'Identity', fields: [
        ...identityFields,
        { name: 'email', label: 'Account email', type: 'text', placeholder: 'user@example.com',
          hint: 'The OAuth account email (read from the auth file).' },
        { name: 'file_name', label: 'File name', type: 'text', placeholder: 'claude-account-1', required: !false,
          hint: 'Auth JSON file id (without .json). Used to match the auth-dir file.' },
        { name: 'label', label: 'Label', type: 'text', placeholder: 'Work account',
          hint: 'Optional human-readable label.' },
      ]},
      { title: 'OAuth token', hint: 'These fields are normally managed automatically by the token refresh pipeline. Edit only when manually seeding or correcting a token.',
        fields: [
          { name: 'token_access_token', label: 'Access token', type: 'password',
            hint: 'Current OAuth access token. Refreshed automatically.' },
          { name: 'token_refresh_token', label: 'Refresh token', type: 'password',
            hint: 'OAuth refresh token (used to obtain new access tokens).' },
          { name: 'token_expiry', label: 'Token expiry', type: 'text', placeholder: '2026-08-01T12:00:00Z',
            hint: 'RFC3339 timestamp. Leave blank if unknown.' },
          { name: 'token_expired', label: 'Token expired', type: 'toggle',
            hint: 'Mark the token as expired to force a refresh on next use.' },
        ]},
      { title: 'Routing', fields: [
        { name: 'priority', label: 'Priority', type: 'number', min: 0, placeholder: '0',
          hint: 'Higher value is preferred when multiple credentials match.' },
        { name: 'models', label: 'Model aliases', type: 'models',
          hint: 'Map client-facing aliases to upstream model names. These are written to the auth file as per-account model_aliases.' },
        { name: 'excluded_models', label: 'Excluded models', type: 'chips',
          placeholder: 'model-id or wildcard', hint: 'Models never routed through this auth. Supports * wildcards.' },
      ]},
      { title: 'Behavior', fields: [
        { name: 'disabled', label: 'Disabled', type: 'toggle',
          hint: 'When on, this auth is excluded from routing.' },
        { name: 'prefix', label: 'Model prefix', type: 'text', placeholder: 'teamA/',
          hint: 'Optional namespace prepended to every model this auth serves.' },
      ]},
    ];
    if (oauthType === 'oauth:claude') sections.push(claudeCloakSection);
    schemaMap[oauthType] = { sections };
  }

  return schemaMap;
}
