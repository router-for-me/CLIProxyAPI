// managementEndpoints.js
//
// Static catalog of every registered /v0/management REST endpoint, grouped
// by resource category. Used by the EndpointMultiSelect combobox in the
// management-token policy editor so operators pick from real endpoints
// (plus useful glob patterns) instead of typing free-text signatures.
//
// Each entry is an object: { value, method, path, group, glob }.
//   - value:  the "<METHOD> <path>" signature the middleware matches against.
//   - glob:   when true, the value ends in "/*" and matches a path subtree.
//
// The list is curated from internal/api/server.go registerManagementRoutes().
// Keep it in sync when routes are added/removed.

// buildGroups returns the grouped endpoint catalog. Each group has a label and
// an array of endpoint entries. Collection endpoints that have child routes
// also emit a "<METHOD> /prefix/*" glob variant so a token can allow/deny a
// whole subtree in one selection.
export function buildEndpointGroups() {
  const groups = [];

// helper intentionally a no-op placeholder for future post-processing.
function withGlobSuffix(entries) {
  return entries;
}

  groups.push({
    label: 'API Keys',
    entries: [
      entry('GET', '/api-keys-pg'),
      entry('POST', '/api-keys-pg'),
      glob('GET', '/api-keys-pg/*'),
      entry('GET', '/api-keys-pg/:id'),
      entry('PATCH', '/api-keys-pg/:id'),
      entry('PUT', '/api-keys-pg/:id/policy'),
      entry('POST', '/api-keys-pg/:id/regenerate'),
      entry('DELETE', '/api-keys-pg/:id'),
      entry('GET', '/api-keys'),
      entry('PUT', '/api-keys'),
      entry('PATCH', '/api-keys'),
      entry('DELETE', '/api-keys'),
      entry('GET', '/api-key-usage'),
      entry('GET', '/usage-queue'),
    ],
  });

  groups.push({
    label: 'Internal Users',
    entries: [
      entry('GET', '/internal-users'),
      entry('POST', '/internal-users'),
      glob('GET', '/internal-users/*'),
      entry('GET', '/internal-users/leaderboard'),
      entry('POST', '/internal-users/reconcile-all'),
      entry('GET', '/internal-users/:id'),
      entry('PATCH', '/internal-users/:id'),
      entry('DELETE', '/internal-users/:id'),
      entry('POST', '/internal-users/:id/reset-spend'),
      entry('POST', '/internal-users/:id/reconcile-spend'),
      entry('GET', '/internal-users/:id/keys'),
      entry('POST', '/internal-users/:id/keys/:keyId/attach'),
      entry('DELETE', '/internal-users/:id/keys/:keyId'),
      entry('GET', '/internal-users/:id/totals'),
      entry('GET', '/internal-users/:id/timeseries'),
      entry('GET', '/internal-users/:id/top'),
      entry('GET', '/internal-users/:id/events'),
      entry('GET', '/internal-users/:id/errors'),
      entry('GET', '/internal-users/:id/windows'),
      entry('GET', '/internal-users/:id/model-spend'),
      entry('GET', '/internal-users/:id/models'),
    ],
  });

  groups.push({
    label: 'Manage LiteLLM',
    entries: [
      entry('GET', '/litellm/users'),
      entry('POST', '/litellm/users'),
      glob('GET', '/litellm/users/*'),
      entry('GET', '/litellm/users/:id'),
      entry('PATCH', '/litellm/users/:id'),
      entry('DELETE', '/litellm/users/:id'),
      entry('POST', '/litellm/users/:id/reset-spend'),
      entry('GET', '/litellm/users/:id/keys'),
      entry('GET', '/litellm/keys'),
      entry('POST', '/litellm/keys'),
      glob('GET', '/litellm/keys/*'),
      entry('GET', '/litellm/keys/:id'),
      entry('PATCH', '/litellm/keys/:id'),
      entry('PUT', '/litellm/keys/:id/policy'),
      entry('POST', '/litellm/keys/:id/regenerate'),
      entry('DELETE', '/litellm/keys/:id'),
      entry('GET', '/litellm/settings'),
      entry('PUT', '/litellm/settings'),
      entry('POST', '/litellm/sync/run'),
    ],
  });

  groups.push({
    label: 'Management API Tokens',
    entries: [
      entry('GET', '/api-tokens'),
      entry('POST', '/api-tokens'),
      glob('GET', '/api-tokens/*'),
      glob('DELETE', '/api-tokens/*'),
      entry('GET', '/api-tokens/audit-log'),
      entry('GET', '/api-tokens/audit-log/:id'),
      entry('GET', '/api-tokens/:id'),
      entry('PATCH', '/api-tokens/:id'),
      entry('PUT', '/api-tokens/:id/policy'),
      entry('POST', '/api-tokens/:id/regenerate'),
      entry('DELETE', '/api-tokens/:id'),
    ],
  });

  groups.push({
    label: 'Usage Stats',
    entries: [
      entry('GET', '/usage-stats'),
      glob('GET', '/usage-stats/*'),
      entry('GET', '/usage-stats/summary'),
      entry('GET', '/usage-stats/totals'),
      entry('GET', '/usage-stats/timeseries'),
      entry('GET', '/usage-stats/top'),
      entry('GET', '/usage-stats/events'),
      entry('GET', '/usage-stats/events/:id'),
      entry('GET', '/usage-stats/errors'),
      entry('GET', '/usage-stats/errors/:id'),
      entry('GET', '/usage-stats/filters'),
      entry('GET', '/usage-windows/:api_key_id'),
    ],
  });

  groups.push({
    label: 'Models Catalog & Pricing',
    entries: [
      entry('GET', '/models-catalog'),
      glob('GET', '/models-catalog/*'),
      entry('GET', '/models-catalog/count'),
      entry('GET', '/models-catalog/summary'),
      entry('GET', '/models-catalog/distinct'),
      entry('GET', '/models-catalog/sync-status'),
      entry('GET', '/models-catalog/:id/pricing'),
      entry('PUT', '/models-catalog/:id/pricing'),
      entry('POST', '/models-catalog/sync-from-v1'),
      entry('POST', '/models-catalog/sync-pricing-preview'),
      entry('POST', '/models-catalog/sync-pricing-apply'),
      entry('GET', '/models-catalog/entry/:id/:provider'),
      entry('PUT', '/models-catalog/entry/:id/:provider'),
      entry('DELETE', '/models-catalog/entry/:id/:provider'),
    ],
  });

  groups.push({
    label: 'Auto Routers',
    entries: [
      entry('GET', '/auto-routers'),
      entry('POST', '/auto-routers'),
      entry('GET', '/auto-routers/:id'),
      entry('PUT', '/auto-routers/:id'),
      entry('DELETE', '/auto-routers/:id'),
    ],
  });

  groups.push({
    label: 'Pricing Sources',
    entries: [
      entry('GET', '/pricing-sources'),
      entry('POST', '/pricing-sources'),
      entry('PUT', '/pricing-sources/:id'),
      entry('DELETE', '/pricing-sources/:id'),
      entry('POST', '/pricing-sources/refresh-all'),
      entry('POST', '/pricing-sources/:id/refresh'),
      entry('POST', '/pricing-sources/:id/upload'),
    ],
  });

  groups.push({
    label: 'Error Messages',
    entries: [
      entry('GET', '/error-messages'),
      entry('GET', '/error-messages/preview'),
      entry('GET', '/error-messages/:code'),
      entry('PUT', '/error-messages/:code'),
      entry('DELETE', '/error-messages/:code'),
    ],
  });

  groups.push({
    label: 'Auth Files',
    entries: [
      entry('GET', '/auth-files'),
      entry('GET', '/auth-files/models'),
      entry('GET', '/auth-files/download'),
      entry('POST', '/auth-files'),
      entry('DELETE', '/auth-files'),
      entry('PATCH', '/auth-files/status'),
      entry('PATCH', '/auth-files/fields'),
    ],
  });

  groups.push({
    label: 'Config & Server',
    entries: [
      entry('GET', '/config'),
      entry('GET', '/config.yaml'),
      entry('PUT', '/config.yaml'),
      entry('GET', '/latest-version'),
      entry('GET', '/debug'),
      entry('PUT', '/debug'),
      entry('PATCH', '/debug'),
      entry('GET', '/logging-to-file'),
      entry('PUT', '/logging-to-file'),
      entry('PATCH', '/logging-to-file'),
      entry('GET', '/logs-max-total-size-mb'),
      entry('PUT', '/logs-max-total-size-mb'),
      entry('PATCH', '/logs-max-total-size-mb'),
      entry('GET', '/error-logs-max-files'),
      entry('PUT', '/error-logs-max-files'),
      entry('PATCH', '/error-logs-max-files'),
      entry('GET', '/usage-statistics-enabled'),
      entry('PUT', '/usage-statistics-enabled'),
      entry('PATCH', '/usage-statistics-enabled'),
      entry('GET', '/proxy-url'),
      entry('PUT', '/proxy-url'),
      entry('PATCH', '/proxy-url'),
      entry('DELETE', '/proxy-url'),
      entry('GET', '/force-model-prefix'),
      entry('PUT', '/force-model-prefix'),
      entry('PATCH', '/force-model-prefix'),
      entry('GET', '/routing/strategy'),
      entry('PUT', '/routing/strategy'),
      entry('PATCH', '/routing/strategy'),
      entry('GET', '/branding'),
      entry('PUT', '/branding'),
      entry('PATCH', '/branding'),
      entry('GET', '/logs'),
      entry('DELETE', '/logs'),
      entry('GET', '/request-error-logs'),
      entry('GET', '/request-error-logs/:name'),
      entry('GET', '/request-log-by-id/:id'),
      entry('GET', '/request-log'),
      entry('PUT', '/request-log'),
      entry('PATCH', '/request-log'),
      entry('GET', '/request-retry'),
      entry('PUT', '/request-retry'),
      entry('PATCH', '/request-retry'),
      entry('GET', '/max-retry-interval'),
      entry('PUT', '/max-retry-interval'),
      entry('PATCH', '/max-retry-interval'),
      entry('GET', '/ws-auth'),
      entry('PUT', '/ws-auth'),
      entry('PATCH', '/ws-auth'),
      entry('POST', '/api-call'),
      entry('POST', '/remote-probe/models'),
      entry('POST', '/reset-quota'),
      entry('GET', '/quota-exceeded/switch-project'),
      entry('PUT', '/quota-exceeded/switch-project'),
      entry('PATCH', '/quota-exceeded/switch-project'),
      entry('GET', '/quota-exceeded/switch-preview-model'),
      entry('PUT', '/quota-exceeded/switch-preview-model'),
      entry('PATCH', '/quota-exceeded/switch-preview-model'),
    ],
  });

  groups.push({
    label: 'OAuth & Provider Keys',
    entries: [
      entry('GET', '/gemini-api-key'),
      entry('PUT', '/gemini-api-key'),
      entry('PATCH', '/gemini-api-key'),
      entry('DELETE', '/gemini-api-key'),
      entry('GET', '/interactions-api-key'),
      entry('PUT', '/interactions-api-key'),
      entry('PATCH', '/interactions-api-key'),
      entry('DELETE', '/interactions-api-key'),
      entry('GET', '/claude-api-key'),
      entry('PUT', '/claude-api-key'),
      entry('PATCH', '/claude-api-key'),
      entry('DELETE', '/claude-api-key'),
      entry('GET', '/codex-api-key'),
      entry('PUT', '/codex-api-key'),
      entry('PATCH', '/codex-api-key'),
      entry('DELETE', '/codex-api-key'),
      entry('GET', '/xai-api-key'),
      entry('PUT', '/xai-api-key'),
      entry('PATCH', '/xai-api-key'),
      entry('DELETE', '/xai-api-key'),
      entry('GET', '/openai-compatibility'),
      entry('PUT', '/openai-compatibility'),
      entry('PATCH', '/openai-compatibility'),
      entry('DELETE', '/openai-compatibility'),
      entry('GET', '/vertex-api-key'),
      entry('PUT', '/vertex-api-key'),
      entry('PATCH', '/vertex-api-key'),
      entry('DELETE', '/vertex-api-key'),
      entry('POST', '/vertex/import'),
      entry('GET', '/oauth-excluded-models'),
      entry('PUT', '/oauth-excluded-models'),
      entry('PATCH', '/oauth-excluded-models'),
      entry('DELETE', '/oauth-excluded-models'),
      entry('GET', '/oauth-model-alias'),
      entry('PUT', '/oauth-model-alias'),
      entry('PATCH', '/oauth-model-alias'),
      entry('DELETE', '/oauth-model-alias'),
      entry('GET', '/anthropic-auth-url'),
      entry('GET', '/codex-auth-url'),
      entry('GET', '/antigravity-auth-url'),
      entry('GET', '/kimi-auth-url'),
      entry('GET', '/xai-auth-url'),
      entry('GET', '/get-auth-status'),
      entry('DELETE', '/oauth-session'),
    ],
  });

  groups.push({
    label: 'Plugins',
    entries: [
      entry('GET', '/plugins'),
      entry('GET', '/plugin-store'),
      entry('POST', '/plugin-store/:id/install'),
      entry('DELETE', '/plugins/:id'),
      entry('PATCH', '/plugins/:id/enabled'),
      entry('GET', '/plugins/:id/config'),
      entry('PUT', '/plugins/:id/config'),
      entry('PATCH', '/plugins/:id/config'),
    ],
  });

  return groups.map((g) => ({ ...g, entries: withGlobSuffix(g.entries) }));
}

// entry builds a precise endpoint signature.
function entry(method, path) {
  return { value: `${method} ${path}`, method, path, glob: false };
}

// glob builds a subtree-matching signature ("<METHOD> /prefix/*"). The
// middleware's endpointMatches treats a trailing "/*" as a prefix match on
// "<METHOD> <path>".
function glob(method, prefix) {
  const globPath = `${prefix}/*`;
  return { value: `${method} ${globPath}`, method, path: globPath, glob: true };
}

// allEndpoints returns a flat array of every endpoint value (for quick search).
export function allEndpoints() {
  const out = [];
  for (const g of buildEndpointGroups()) {
    for (const e of g.entries) out.push({ ...e, group: g.label });
  }
  return out;
}
