// developerDocs.js
//
// Endpoint definitions for the Developer docs page. Each section groups a set
// of endpoints; each endpoint carries:
//   - method, path, summary
//   - params:  [{ name, in: 'query'|'path'|'body', type, required, default, description }]
//   - examplePayload:  full JSON request body (or null)
//   - exampleCurl:      literal curl command (string)
//   - responses: [{ status, label, body }]
//
// The page renders these as cards. The data is hand-curated to match the Go
// handlers' actual query params, payloads, and response envelopes (including
// known quirks like flat vs wrapped InternalUser responses).

export const API_BASE = '/v0/management';
export const AUTH_NOTE =
  'Send the management credential as `Authorization: Bearer <secret>` or `X-Management-Key: <secret>`. ' +
  'The secret is the MANAGEMENT_PASSWORD env var (alias: NIXLLM_DASHBOARD_PASSWORD), the config ' +
  '`remote-management.secret-key`, or a management API token created via POST /api-tokens. Non-localhost ' +
  'clients require `remote-management.allow-remote: true`. 5 failed attempts from one IP ban it for 30 min.';

export const COMMON_HEADERS_NOTE =
  'Every response includes `X-CPA-VERSION`, `X-CPA-COMMIT`, `X-CPA-BUILD-DATE`, and `X-CPA-SUPPORT-PLUGIN` headers.';

export const ERROR_ENVELOPE = {
  type: 'object',
  shape: '{"error":{"type":"<type>","message":"<msg>"}}',
  types: [
    { type: 'invalid_request', status: 400, note: 'Malformed JSON or missing required field.' },
    { type: 'not_found', status: 404, note: 'Resource does not exist.' },
    { type: 'pg_store_not_configured', status: 503, note: 'PGSTORE_DSN is not set on the server.' },
    { type: 'internal_error', status: 500, note: 'Unexpected server-side failure.' },
  ],
};

export const sections = [
  // =========================================================================
  // 1. Internal Users
  // =========================================================================
  {
    id: 'internal-users',
    title: 'Internal Users',
    description:
      'Key owners with per-user budget, role, model access, and spend attribution (LiteLLM-style). ' +
      'All routes return 503 when the PG store is not configured.',
    endpoints: [
      {
        method: 'GET', path: '/internal-users', summary: 'List internal users (paginated, filterable).',
        params: [
          { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: '1-indexed page.' },
          { name: 'page_size', in: 'query', type: 'integer', required: false, default: '25', description: 'Rows per page (max 200).' },
          { name: 'role', in: 'query', type: 'string', required: false, default: '', description: 'Filter by role (internal_user | proxy_admin | proxy_admin_viewer).' },
          { name: 'search', in: 'query', type: 'string', required: false, default: '', description: 'Case-insensitive substring on alias OR email.' },
          { name: 'sort_by', in: 'query', type: 'string', required: false, default: 'spend', description: 'spend | created_at | user_alias.' },
          { name: 'sort_order', in: 'query', type: 'string', required: false, default: 'desc', description: 'asc | desc.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/internal-users?page=1&page_size=25&role=internal_user&sort_by=spend" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{\n  "users": [\n    {\n      "id": "u-abc123",\n      "user_alias": "alice",\n      "user_email": "alice@example.com",\n      "user_role": "internal_user",\n      "max_budget": 50.0,\n      "spend": 3.42,\n      "rpm_limit": 60,\n      "tpm_limit": 100000,\n      "max_parallel_requests": 5,\n      "created_at": "2026-07-01T10:00:00Z",\n      "updated_at": "2026-07-22T08:30:00Z",\n      "key_count": 2\n    }\n  ],\n  "page": 1,\n  "page_size": 25,\n  "total": 1,\n  "total_pages": 1\n}` },
          { status: 503, label: 'PG store not configured', body: `{"error":{"type":"pg_store_not_configured","message":"PostgreSQL store is not configured. Set PGSTORE_DSN to enable this route."}}` },
        ],
      },
      {
        method: 'POST', path: '/internal-users', summary: 'Create an internal user. auto_create_key (default true) also provisions an API key and returns its plaintext secret once.',
        params: [
          { name: 'user_alias', in: 'body', type: 'string', required: false, default: '', description: 'Human label.' },
          { name: 'user_email', in: 'body', type: 'string', required: false, default: '', description: 'Unique email.' },
          { name: 'user_role', in: 'body', type: 'string', required: false, default: 'internal_user', description: 'internal_user | proxy_admin | proxy_admin_viewer.' },
          { name: 'models', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Allowed model ids.' },
          { name: 'max_budget', in: 'body', type: 'number', required: false, default: 'null', description: 'Spend cap in USD.' },
          { name: 'budget_duration', in: 'body', type: 'string', required: false, default: '', description: 'monthly | weekly | daily reset window.' },
          { name: 'rpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: 'Requests per minute.' },
          { name: 'tpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: 'Tokens per minute.' },
          { name: 'max_parallel_requests', in: 'body', type: 'integer', required: false, default: 'null', description: 'In-flight cap.' },
          { name: 'auto_create_key', in: 'body', type: 'boolean', required: false, default: 'true', description: 'Provision a default API key bound to the user.' },
        ],
        examplePayload: `{\n  "user_alias": "alice",\n  "user_email": "alice@example.com",\n  "user_role": "internal_user",\n  "max_budget": 50.0,\n  "budget_duration": "monthly",\n  "rpm_limit": 60,\n  "tpm_limit": 100000,\n  "auto_create_key": true\n}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/internal-users" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "user_alias": "alice",\n    "user_email": "alice@example.com",\n    "max_budget": 50.0,\n    "rpm_limit": 60,\n    "auto_create_key": true\n  }'`,
        responses: [
          { status: 201, label: 'Created (with auto key)', body: `{\n  "user": { "id": "u-abc123", "user_alias": "alice", ... },\n  "api_key": { "id": "k-def1", "name": "alice-key", ... },\n  "policy": { "api_key_id": "k-def1", ... },\n  "secret": "sk-xxxxxxxxxxxxxxxx"\n}` },
          { status: 201, label: 'Created (no key)', body: `{"user":{"id":"u-abc123","user_alias":"alice",...}}` },
          { status: 400, label: 'Invalid request', body: `{"error":{"type":"invalid_request","message":"invalid character 'x' looking for beginning of object key string"}}` },
          { status: 500, label: 'Internal error', body: `{"error":{"type":"internal_error","message":"postgres store: create internal user: ..."}` },
        ],
      },
      {
        method: 'GET', path: '/internal-users/:id', summary: 'Get a single user. Returns the flat InternalUser object (no envelope key).',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'User UUID id.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/internal-users/u-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"u-abc123","user_alias":"alice","user_email":"alice@example.com","user_role":"internal_user","max_budget":50.0,"spend":3.42,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-22T08:30:00Z","key_count":2}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"internal user not found"}}` },
        ],
      },
      {
        method: 'PATCH', path: '/internal-users/:id', summary: 'Partial update. Pointer-typed fields are applied only when non-nil; pass an explicit empty string to clear scalars.',
        params: [
          { name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'User id.' },
          { name: 'body', in: 'body', type: 'object', required: true, default: '', description: 'Same shape as create; all fields optional.' },
        ],
        examplePayload: `{"max_budget": 75.0, "rpm_limit": 120}`,
        exampleCurl: `curl -s -X PATCH "${'{API_BASE}'}/internal-users/u-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"max_budget":75.0,"rpm_limit":120}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"u-abc123","max_budget":75.0,"rpm_limit":120,...}` },
          { status: 400, label: 'Invalid request', body: `{"error":{"type":"invalid_request","message":"..."}}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"internal user not found"}}` },
        ],
      },
      {
        method: 'DELETE', path: '/internal-users/:id', summary: 'Delete a user. Cascade-removes user_windows (api_keys are not deleted but lose the owner link).',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'User id.' }],
        examplePayload: null,
        exampleCurl: `curl -s -X DELETE "${'{API_BASE}'}/internal-users/u-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"u-abc123","deleted":true}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"internal user not found"}}` },
        ],
      },
      {
        method: 'POST', path: '/internal-users/:id/reset-spend', summary: 'Zero out the user\'s running spend (resets the budget window).',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'User id.' }],
        examplePayload: null,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/internal-users/u-abc123/reset-spend" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"u-abc123","spend":0,...}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"internal user not found"}}` },
        ],
      },
      {
        method: 'GET', path: '/internal-users/:id/keys', summary: 'List the API keys owned by this user (paginated).',
        params: [
          { name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'User id.' },
          { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: 'Page.' },
          { name: 'page_size', in: 'query', type: 'integer', required: false, default: '25', description: 'Rows per page (max 200).' },
          { name: 'status', in: 'query', type: 'string', required: false, default: '', description: 'Filter key status.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/internal-users/u-abc123/keys?status=active" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"api_keys":[{...}],"page":1,"page_size":25,"total":2,"total_pages":1}` },
        ],
      },
      {
        method: 'GET', path: '/internal-users/:id/totals', summary: 'Aggregate usage totals scoped to the user. Shares the usage-stats filter set.',
        params: sharedUsageFilters('user totals'),
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/internal-users/u-abc123/totals?from=2026-07-01T00:00:00Z&to=2026-07-31T23:59:59Z" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"totals":{"request_count":120,"failed_count":3,"total_tokens":45000,"cost_usd":1.23},"failure_rate":0.025,"user_id":"u-abc123"}` },
        ],
      },
      {
        method: 'GET', path: '/internal-users/:id/events', summary: 'Paginated usage-event rows scoped to the user.',
        params: [
          ...sharedUsageFilters('events', { includePagination: true }),
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/internal-users/u-abc123/events?page=1&page_size=10&include=cost_breakdown" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"events":[{...}],"page":1,"page_size":10,"total":120,"total_pages":12,"user_id":"u-abc123"}` },
        ],
      },
    ],
  },

  // =========================================================================
  // 2. API Keys
  // =========================================================================
  {
    id: 'api-keys',
    title: 'API Keys',
    description:
      'PG-backed client-facing API keys, each owned by an Internal User. Per-key policy is optional — ' +
      'unset caps fall back to the owner\'s max_budget, RPM, and model allow-list. All routes return 503 ' +
      'when the PG store is not configured.',
    endpoints: [
      {
        method: 'GET', path: '/api-keys-pg', summary: 'List API keys (paginated, filterable, sortable).',
        params: [
          { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: 'Page.' },
          { name: 'page_size', in: 'query', type: 'integer', required: false, default: '25', description: 'Rows per page (max 200).' },
          { name: 'status', in: 'query', type: 'string', required: false, default: '', description: 'active | disabled | revoked | expired.' },
          { name: 'user_id', in: 'query', type: 'string', required: false, default: '', description: 'Filter to keys owned by this internal user.' },
          { name: 'search', in: 'query', type: 'string', required: false, default: '', description: 'Substring on name / alias / prefix (case-insensitive).' },
          { name: 'sort_by', in: 'query', type: 'string', required: false, default: 'created_at', description: 'created_at | name | last_used_at | user_alias.' },
          { name: 'sort_order', in: 'query', type: 'string', required: false, default: 'desc', description: 'asc | desc.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/api-keys-pg?page=1&status=active&search=prod&sort_by=-name" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"api_keys":[{"id":"k-def1","name":"prod-app","key_prefix":"sk-abcd","status":"active","user_id":"u-abc123",...}],"page":1,"page_size":25,"total":1,"total_pages":1}` },
          { status: 503, label: 'PG store not configured', body: `{"error":{"type":"pg_store_not_configured","message":"..."}}` },
        ],
      },
      {
        method: 'POST', path: '/api-keys-pg', summary: 'Create an API key. user_id is REQUIRED (every key belongs to an Internal User).',
        params: [
          { name: 'name', in: 'body', type: 'string', required: false, default: 'unnamed', description: 'Human label.' },
          { name: 'alias', in: 'body', type: 'string', required: false, default: '', description: 'Non-secret alias for usage-stats filtering.' },
          { name: 'secret', in: 'body', type: 'string', required: false, default: '', description: 'Custom secret (min 16 chars). Auto-generated when empty.' },
          { name: 'user_id', in: 'body', type: 'string', required: true, default: '', description: 'Owning Internal User id.' },
          { name: 'expires_at', in: 'body', type: 'string', required: false, default: 'null', description: 'RFC3339 expiry timestamp.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '{}', description: 'Arbitrary JSON metadata.' },
          { name: 'policy', in: 'body', type: 'object', required: false, default: 'null', description: 'Per-key Policy (see PUT /:id/policy). May include policy.model_group_id to attach a reusable Model Group at creation time — the group then becomes the source of truth for allowed/blocked/routes (entity fields ignored at enforcement time).' },
        ],
        examplePayload: `{\n  "name": "prod-app",\n  "user_id": "u-abc123",\n  "expires_at": "2026-12-31T23:59:59Z",\n  "policy": { "rpm_limit": 100, "allowed_models": ["gpt-4o"], "model_group_id": "g-abc123" }\n}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/api-keys-pg" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"name":"prod-app","user_id":"u-abc123"}'`,
        responses: [
          { status: 201, label: 'Created', body: `{"id":"k-def1","name":"prod-app","key_prefix":"sk-abcd","status":"active","user_id":"u-abc123","secret":"sk-xxxxxxxxxxxxxxxx","policy":{...}}` },
          { status: 400, label: 'Missing user_id', body: `{"error":{"type":"invalid_request","message":"user_id is required: every API key must be owned by an Internal User"}}` },
          { status: 404, label: 'Owner not found', body: `{"error":{"type":"not_found","message":"internal user not found: create the user before assigning keys to it"}}` },
        ],
      },
      {
        method: 'GET', path: '/api-keys-pg/:id', summary: 'Get a key + its policy. The plaintext secret is never included on reads.',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Key id.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/api-keys-pg/k-def1" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"k-def1","name":"prod-app","status":"active","policy":{...}}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"API key not found"}}` },
        ],
      },
      {
        method: 'PATCH', path: '/api-keys-pg/:id', summary: 'Partial update (status, metadata, name, alias, expiry).',
        params: [
          { name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Key id.' },
          { name: 'status', in: 'body', type: 'string', required: false, default: '', description: 'active | disabled | revoked.' },
          { name: 'clear_expiry', in: 'body', type: 'boolean', required: false, default: 'false', description: 'Clear the expiry.' },
          { name: 'model_group_id', in: 'body', type: 'string', required: false, default: 'null', description: 'Attach (non-empty, group must exist) or detach (empty string) a Model Group in a single PATCH. When attached the group becomes the source of truth for this key\'s allowed/blocked lists and per-model routes at enforcement time. If the key has no policy row yet, attaching materializes an empty policy carrying only the model_group_id. Detaching a policy-less key is a no-op (200).' },
        ],
        examplePayload: `{"status":"disabled","clear_expiry":true}`,
        exampleCurl: `curl -s -X PATCH "${'{API_BASE}'}/api-keys-pg/k-def1" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"status":"disabled"}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"k-def1","status":"disabled",...}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"API key not found"}}` },
        ],
      },
      {
        method: 'PUT', path: '/api-keys-pg/:id/policy', summary: 'Create or replace the per-key policy.',
        params: [
          { name: 'rpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: 'Requests per minute.' },
          { name: 'max_parallel_requests', in: 'body', type: 'integer', required: false, default: 'null', description: 'In-flight cap.' },
          { name: 'budget_hourly_usd', in: 'body', type: 'number', required: false, default: 'null', description: 'Hourly USD cap.' },
          { name: 'budget_weekly_usd', in: 'body', type: 'number', required: false, default: 'null', description: 'Weekly USD cap.' },
          { name: 'budget_monthly_usd', in: 'body', type: 'number', required: false, default: 'null', description: 'Monthly USD cap.' },
          { name: 'allowed_models', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Allow-list (empty = all). Ignored at enforcement time when model_group_id is set (group is the source of truth).' },
          { name: 'blocked_models', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Deny-list. Ignored at enforcement time when model_group_id is set.' },
          { name: 'model_routes', in: 'body', type: 'object[]', required: false, default: '[]', description: 'Per-concrete-model upstream provider pinning. { model, providers }. Ignored at enforcement time when model_group_id is set.' },
          { name: 'model_group_id', in: 'body', type: 'string', required: false, default: 'null', description: 'Attach (non-empty) or detach (empty) a Model Group. When set, the group\'s allowed/blocked/routes OVERRIDE this policy\'s own fields at enforcement time. Existence is validated; passing null preserves the previous value.' },
        ],
        examplePayload: `{"rpm_limit":100,"budget_monthly_usd":50.0,"allowed_models":["gpt-4o","claude-3-5-sonnet"]}`,
        exampleCurl: `curl -s -X PUT "${'{API_BASE}'}/api-keys-pg/k-def1/policy" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"rpm_limit":100,"allowed_models":["gpt-4o"]}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"policy":{"api_key_id":"k-def1","rpm_limit":100,...},"api_key_id":"k-def1"}` },
        ],
      },
      {
        method: 'POST', path: '/api-keys-pg/:id/regenerate', summary: 'Issue a new secret. The old secret stops working immediately; ID/policy/metadata preserved.',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Key id.' }],
        examplePayload: null,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/api-keys-pg/k-def1/regenerate" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"k-def1","secret":"sk-xxxxxxxxxxxxxxxx"}` },
        ],
      },
      {
        method: 'DELETE', path: '/api-keys-pg/:id', summary: 'Permanently delete a key and its policy (cascade).',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Key id.' }],
        examplePayload: null,
        exampleCurl: `curl -s -X DELETE "${'{API_BASE}'}/api-keys-pg/k-def1" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"k-def1","deleted":true}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"API key not found"}}` },
        ],
      },
    ],
  },

  // =========================================================================
  // 3. Model Groups
  // =========================================================================
  {
    id: 'model-groups',
    title: 'Model Groups',
    description:
      'Reusable templates of an allowed-models grant list (with trailing-\'*\' wildcards) plus optional per-model upstream routing (model_routes). ' +
      'A group can be attached to an API-key policy in one of two ways: ' +
      '(a) via the dedicated /model-groups/:id/attach endpoint, or ' +
      '(b) by setting policy.model_group_id on POST /api-keys-pg or PUT /api-keys-pg/:id/policy, or by passing model_group_id on PATCH /api-keys-pg/:id. ' +
      'When attached, the group becomes the source of truth for that key\'s allowed/blocked lists and per-model routes at enforcement time — the key\'s own fields are overridden. ' +
      'Model groups attach ONLY to API-key policies (not to Internal Users). ' +
      'All routes return 503 when the PG store is not configured.',
    endpoints: [
      {
        method: 'GET', path: '/model-groups', summary: 'List model groups (paginated, filterable, sortable).',
        params: [
          { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: '1-indexed page.' },
          { name: 'page_size', in: 'query', type: 'integer', required: false, default: '25', description: 'Rows per page (max 200).' },
          { name: 'search', in: 'query', type: 'string', required: false, default: '', description: 'Case-insensitive substring on name OR description.' },
          { name: 'sort_by', in: 'query', type: 'string', required: false, default: 'name', description: 'name | created_at | updated_at.' },
          { name: 'sort_order', in: 'query', type: 'string', required: false, default: 'asc', description: 'asc | desc.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/model-groups?page=1&page_size=25&search=gpt" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{\n  "groups": [\n    {\n      "id": "g-abc123",\n      "name": "gpt-only",\n      "description": "GPT family + gpt-4o pinned to openai oauth",\n      "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n      "blocked_models": [],\n      "model_routes": [\n        { "model": "gpt-4o", "providers": ["openai"] }\n      ],\n      "metadata": {},\n      "created_at": "2026-07-22T09:00:00Z",\n      "updated_at": "2026-07-22T09:05:00Z"\n    }\n  ],\n  "page": 1,\n  "page_size": 25,\n  "total": 1,\n  "total_pages": 1\n}` },
          { status: 503, label: 'PG store not configured', body: `{"error":{"type":"pg_store_not_configured","message":"..."}}` },
        ],
      },
      {
        method: 'POST', path: '/model-groups', summary: 'Create a group. Name is required and unique. model_routes is validated against allowed_models.',
        params: [
          { name: 'name', in: 'body', type: 'string', required: true, default: '', description: 'Unique human label.' },
          { name: 'description', in: 'body', type: 'string', required: false, default: '', description: 'Free-form description.' },
          { name: 'allowed_models', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Allow-list (empty = all allowed). Supports trailing-\'*\' wildcards.' },
          { name: 'blocked_models', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Deny-list. Takes precedence over allowed.' },
          { name: 'model_routes', in: 'body', type: 'object[]', required: false, default: '[]', description: 'Per-concrete-model upstream provider pinning. Each entry: { model, providers: string[] }. model must be in allowed_models; wildcard models cannot be routed.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '{}', description: 'Arbitrary JSON metadata.' },
        ],
        examplePayload: `{\n  "name": "gpt-only",\n  "description": "GPT family + gpt-4o pinned to openai oauth",\n  "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n  "model_routes": [\n    { "model": "gpt-4o", "providers": ["openai"] }\n  ]\n}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/model-groups" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "name": "gpt-only",\n    "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n    "model_routes": [{ "model": "gpt-4o", "providers": ["openai"] }]\n  }'`,
        responses: [
          { status: 201, label: 'Created', body: `{"id":"g-abc123","name":"gpt-only","allowed_models":["gpt-4o","gpt-4o-mini","gpt-4*"],"model_routes":[{"model":"gpt-4o","providers":["openai"]}],"created_at":"2026-07-22T09:00:00Z","updated_at":"2026-07-22T09:00:00Z"}` },
          { status: 400, label: 'Invalid request (name missing / route out of allowed)', body: `{"error":{"type":"invalid_request","message":"postgres store: model group name is required"}}` },
          { status: 409, label: 'Name taken', body: `{"error":{"type":"conflict","message":"model group name already taken"}}` },
        ],
      },
      {
        method: 'GET', path: '/model-groups/:id', summary: 'Get a single group AND its current attachments (API-key policies that reference it).',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Group id.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/model-groups/g-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{\n  "group": {\n    "id": "g-abc123",\n    "name": "gpt-only",\n    "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n    "blocked_models": [],\n    "model_routes": [{ "model": "gpt-4o", "providers": ["openai"] }],\n    "metadata": {},\n    "created_at": "2026-07-22T09:00:00Z",\n    "updated_at": "2026-07-22T09:05:00Z"\n  },\n  "attachments": [\n    {\n      "entity_id": "k-def1",\n      "entity_label": "prod-app",\n      "entity_user_id": "u-abc123",\n      "entity_user_alias": "alice",\n      "entity_user_email": "alice@example.com"\n    }\n  ]\n}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"model group not found"}}` },
        ],
      },
      {
        method: 'PUT', path: '/model-groups/:id', summary: 'Partial update. Pointer-typed fields apply only when non-nil.',
        params: [
          { name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Group id.' },
          { name: 'name', in: 'body', type: 'string', required: false, default: '', description: 'Unique name.' },
          { name: 'description', in: 'body', type: 'string', required: false, default: '', description: 'Description.' },
          { name: 'allowed_models', in: 'body', type: 'string[]', required: false, default: '—', description: 'Replace the allow-list.' },
          { name: 'blocked_models', in: 'body', type: 'string[]', required: false, default: '—', description: 'Replace the deny-list.' },
          { name: 'model_routes', in: 'body', type: 'object[]', required: false, default: '—', description: 'Replace routes.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '—', description: 'Replace metadata.' },
        ],
        examplePayload: `{"allowed_models":["gpt-4o"]}`,
        exampleCurl: `curl -s -X PUT "${'{API_BASE}'}/model-groups/g-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"allowed_models":["gpt-4o"]}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"g-abc123","name":"gpt-only","allowed_models":["gpt-4o"],...}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"model group not found"}}` },
          { status: 409, label: 'Name taken', body: `{"error":{"type":"conflict","message":"model group name already taken"}}` },
        ],
      },
      {
        method: 'DELETE', path: '/model-groups/:id', summary: 'Permanently delete a group. Rejected with 409 while any API-key policy still references it.',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Group id.' }],
        examplePayload: null,
        exampleCurl: `curl -s -X DELETE "${'{API_BASE}'}/model-groups/g-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"g-abc123","deleted":true}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"model group not found"}}` },
          { status: 409, label: 'In use (still attached)', body: `{"error":{"type":"in_use","message":"model group is still attached to one or more entities: attached to 1 api_key_policies"}}` },
        ],
      },
      {
        method: 'POST', path: '/model-groups/:id/attach', summary: 'Attach the group to an API-key policy. The group becomes the source of truth for that key\'s model access (allowed/blocked + routes).',
        params: [
          { name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Group id.' },
          { name: 'api_key_id', in: 'body', type: 'string', required: true, default: '', description: 'The API key id whose policy row will carry the model_group_id. The key\'s policy row must already exist (PUT /api-keys-pg/:id/policy) — attach is an UPDATE, not an upsert.' },
        ],
        examplePayload: `{"api_key_id":"k-def1"}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/model-groups/g-abc123/attach" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"api_key_id":"k-def1"}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"group_id":"g-abc123","api_key_id":"k-def1","attached":true}` },
          { status: 404, label: 'Group or API key not found', body: `{"error":{"type":"not_found","message":"model group not found"}} or {"error":{"type":"not_found","message":"api key not found"}}` },
        ],
      },
      {
        method: 'POST', path: '/model-groups/:id/detach', summary: 'Clear the model_group_id on an API-key policy so the policy\'s own allowed/blocked/routes fields apply again. Idempotent — detaching a policy-less key is a no-op.',
        params: [
          { name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Group id (informational; not validated on detach).' },
          { name: 'api_key_id', in: 'body', type: 'string', required: true, default: '', description: 'The API key id whose policy should be detached.' },
        ],
        examplePayload: `{"api_key_id":"k-def1"}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/model-groups/g-abc123/detach" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"api_key_id":"k-def1"}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"group_id":"g-abc123","api_key_id":"k-def1","detached":true}` },
          { status: 404, label: 'API key not found', body: `{"error":{"type":"not_found","message":"api key not found"}}` },
        ],
      },
    ],
  },

  // =========================================================================
  // 4. Available Models
  // =========================================================================
  {
    id: 'models',
    title: 'Available Models',
    description:
      'The PG-backed model catalog (mirrored from the registry updater) plus per-model pricing. ' +
      'All routes return 503 when the PG store is not configured.',
    endpoints: [
      {
        method: 'GET', path: '/models-catalog', summary: 'List catalog models (paginated, filterable, with live/stale availability).',
        params: [
          { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: 'Page.' },
          { name: 'page_size', in: 'query', type: 'integer', required: false, default: '25', description: 'Rows per page (max 200).' },
          { name: 'provider', in: 'query', type: 'string', required: false, default: '', description: 'Filter by provider.' },
          { name: 'official_provider', in: 'query', type: 'string', required: false, default: '', description: 'Filter by official provider.' },
          { name: 'available_only', in: 'query', type: 'string', required: false, default: '', description: '1/true → restrict to in-memory registry live ids.' },
          { name: 'q', in: 'query', type: 'string', required: false, default: '', description: 'ILIKE substring on id/name/display_name/provider.' },
          { name: 'sort', in: 'query', type: 'string', required: false, default: '', description: 'Column, optional - prefix for desc (id|provider|context_length|...).' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/models-catalog?available_only=1&sort=-context_length" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"models":[{...}],"page":1,"page_size":25,"total":42,"total_pages":2,"live_ids":{"gpt-4o":true}}` },
        ],
      },
      {
        method: 'GET', path: '/models-catalog/summary', summary: 'Header stat cards: total / live / stale / priced / unpriced.',
        params: [],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/models-catalog/summary" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"total":42,"live":38,"stale":4,"priced":30,"unpriced":12}` },
        ],
      },
      {
        method: 'GET', path: '/models-catalog/:id/pricing', summary: 'Get per-model unit pricing (USD per 1M tokens).',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Model id.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/models-catalog/gpt-4o/pricing" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"gpt-4o","input_per_1m_usd":2.5,"output_per_1m_usd":10.0,"cached_input_per_1m_usd":1.25,"cached_read_per_1m_usd":0.5,"reasoning_per_1m_usd":0}` },
        ],
      },
      {
        method: 'PUT', path: '/models-catalog/:id/pricing', summary: 'Create or replace per-model pricing. The id from the URL overrides any body id.',
        params: [
          { name: 'input_per_1m_usd', in: 'body', type: 'number', required: false, default: '0', description: 'Input token price.' },
          { name: 'output_per_1m_usd', in: 'body', type: 'number', required: false, default: '0', description: 'Output token price.' },
          { name: 'reasoning_per_1m_usd', in: 'body', type: 'number', required: false, default: '0', description: 'Reasoning token price.' },
        ],
        examplePayload: `{"input_per_1m_usd":2.5,"output_per_1m_usd":10.0}`,
        exampleCurl: `curl -s -X PUT "${'{API_BASE}'}/models-catalog/gpt-4o/pricing" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"input_per_1m_usd":2.5,"output_per_1m_usd":10.0}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"gpt-4o","input_per_1m_usd":2.5,"output_per_1m_usd":10.0,...}` },
        ],
      },
      {
        method: 'POST', path: '/models-catalog/sync-from-v1', summary: 'Sync the catalog from the live /v1/models list. Optionally pass a caller_key to probe as that key.',
        params: [{ name: 'caller_key', in: 'body', type: 'string', required: false, default: '', description: 'Plaintext secret to probe /v1/models as. Auto-picks the first configured key when omitted.' }],
        examplePayload: `{"caller_key":"sk-xxxxxxxx"}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/models-catalog/sync-from-v1" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"synced":38,"caller_key_source":"auto","caller_key_prefix":"sk-abcd…"}` },
          { status: 412, label: 'No caller key', body: `{"error":{"type":"no_caller_key","message":"..."}}` },
          { status: 502, label: 'Probe failed', body: `{"error":{"type":"v1_models_probe_failed","message":"..."}}` },
        ],
      },
    ],
  },

  // =========================================================================
  // 4. Usage Stats
  // =========================================================================
  {
    id: 'usage-stats',
    title: 'Usage Stats',
    description:
      'Aggregate usage analytics powered by the PG usage_events table. All routes accept a shared filter ' +
      'set and return 503 when the PG store is not configured. Timestamps are RFC3339 (UTC).',
    endpoints: [
      {
        method: 'GET', path: '/usage-stats/totals', summary: 'Aggregate totals (request count, tokens, cost) + failure rate for the filter window.',
        params: sharedUsageFilters('totals'),
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/usage-stats/totals?from=2026-07-22T00:00:00Z&to=2026-07-22T23:59:59Z&provider=openai" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"totals":{"request_count":1024,"failed_count":12,"total_tokens":450000,"cost_usd":12.34},"failure_rate":0.012}` },
        ],
      },
      {
        method: 'GET', path: '/usage-stats/timeseries', summary: 'Time-bucketed points for charting.',
        params: [
          ...sharedUsageFilters('timeseries'),
          { name: 'interval', in: 'query', type: 'string', required: false, default: 'hour', description: 'minute | hour | day.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/usage-stats/timeseries?interval=hour&from=2026-07-22T00:00:00Z&to=2026-07-22T23:59:59Z" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"points":[{"bucket":"2026-07-22T08:00:00Z","request_count":120,"total_tokens":45000,"cost_usd":1.23},...],"interval":"hour"}` },
          { status: 400, label: 'Invalid interval', body: `{"error":{"type":"invalid_request","message":"interval must be minute, hour, or day"}}` },
        ],
      },
      {
        method: 'GET', path: '/usage-stats/top', summary: 'Top-N breakdown by dimension × metric.',
        params: [
          ...sharedUsageFilters('top'),
          { name: 'dimension', in: 'query', type: 'string', required: false, default: 'model', description: 'model | provider | api_key_id | api_key_principal.' },
          { name: 'metric', in: 'query', type: 'string', required: false, default: 'request_count', description: 'request_count | total_tokens | cost_usd.' },
          { name: 'limit', in: 'query', type: 'integer', required: false, default: '10', description: 'Top N.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/usage-stats/top?dimension=model&metric=cost_usd&limit=5" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"entries":[{"key":"gpt-4o","value":4.20},...],"dimension":"model","metric":"cost_usd"}` },
        ],
      },
      {
        method: 'GET', path: '/usage-stats/events', summary: 'Paginated raw usage-event rows.',
        params: [
          ...sharedUsageFilters('events', { includePagination: true }),
          { name: 'include', in: 'query', type: 'string', required: false, default: '', description: 'Comma-separated; cost_breakdown adds per-segment token costs.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/usage-stats/events?page=1&page_size=10&include=cost_breakdown&model=gpt-4o" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"events":[{...}],"page":1,"page_size":10,"total":120}` },
        ],
      },
      {
        method: 'GET', path: '/usage-stats/events/:id', summary: 'Single event detail (always includes full cost breakdown).',
        params: [{ name: 'id', in: 'path', type: 'integer', required: true, default: '', description: 'Event id (positive int64).' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/usage-stats/events/42" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"event":{"id":42,"provider":"openai","model":"gpt-4o","input_tokens":1000,"output_tokens":500,"cost_usd":0.0125,...}}` },
          { status: 400, label: 'Invalid id', body: `{"error":{"type":"invalid_request","message":"id must be a positive integer"}}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"usage event not found"}}` },
        ],
      },
      {
        method: 'GET', path: '/usage-stats/errors', summary: 'Paginated failed-attempt rows (upstream errors, stream errors).',
        params: [
          ...sharedUsageFilters('errors', { includePagination: true }),
          { name: 'include', in: 'query', type: 'string', required: false, default: '', description: 'cost_breakdown.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/usage-stats/errors?page=1&errors_only=1" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"errors":[{...}],"page":1,"page_size":25,"total":3}` },
        ],
      },
      {
        method: 'GET', path: '/usage-stats/filters', summary: 'Distinct filter options (api_keys, providers, models) for the window — populates dashboard dropdowns.',
        params: sharedUsageFilters('filter options'),
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/usage-stats/filters?from=2026-07-22T00:00:00Z&to=2026-07-22T23:59:59Z" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"api_keys":[{"id":"k-def1","alias":"prod-app"}],"providers":["openai","anthropic"],"models":["gpt-4o","claude-3-5-sonnet"]}` },
        ],
      },
    ],
  },
  // NOTE: Management API Token endpoints (/api-tokens, /api-tokens/audit-log,
  // etc.) are intentionally omitted from the public Developer docs. Token
  // lifecycle is managed via the dashboard's "API Management" page, which
  // calls those routes directly. The token-auth + policy + audit enforcement
  // remains active in the middleware regardless.
];

// sharedUsageFilters returns the common filter params shared by the usage-stats
// endpoints. `label` is used in the parameter descriptions; when
// `includePagination` is true, page/page_size params are appended.
function sharedUsageFilters(label, opts = {}) {
  const params = [
    { name: 'api_key_id', in: 'query', type: 'string', required: false, default: '', description: 'Filter to this API key id.' },
    { name: 'api_key_principal', in: 'query', type: 'string', required: false, default: '', description: 'Filter to this key principal (alias).' },
    { name: 'user_id', in: 'query', type: 'string', required: false, default: '', description: 'Filter to this internal user id.' },
    { name: 'provider', in: 'query', type: 'string', required: false, default: '', description: 'Filter by provider.' },
    { name: 'model', in: 'query', type: 'string', required: false, default: '', description: 'Filter by model id.' },
    { name: 'from', in: 'query', type: 'string', required: false, default: '', description: 'RFC3339 lower bound (inclusive).' },
    { name: 'to', in: 'query', type: 'string', required: false, default: '', description: 'RFC3339 upper bound (inclusive).' },
  ];
  if (opts.includePagination) {
    params.push(
      { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: 'Page (1-indexed).' },
      { name: 'page_size', in: 'query', type: 'integer', required: false, default: '25', description: 'Rows per page (max 200).' },
    );
  }
  return params;
}
