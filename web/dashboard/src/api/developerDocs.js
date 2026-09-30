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
  // 1.5 LiteLLM Compat API
  // =========================================================================
  {
    id: 'litellm-compat',
    title: 'LiteLLM Compat API',
    description:
      'A wire-compatible LiteLLM admin surface mounted at /litellm under the management base. ' +
      'These routes are RUNTIME-BACKED: users and keys created here land in the same ' +
      'internal_users / api_keys tables that serve real inference traffic, so a key minted via ' +
      'POST /litellm/key/generate works immediately against /v1/chat/completions with full ' +
      'budget/RPM/model enforcement. Payloads mirror LiteLLM\'s OpenAPI v1.102.1 field names ' +
      '(snake_case, omitempty on optionals, secret returned once on generate/regenerate, never on ' +
      'read). A LiteLLM client can integrate by pointing its base URL at {NIXLLM}/v0/management. ' +
      'All routes return 503 when the PG store is not configured.',
    endpoints: [
      {
        method: 'POST', path: '/litellm/user/new', summary: 'Create an internal user (runtime-backed). Returns the user with LiteLLM field names plus the generated key when auto_create_key is true (the default).',
        params: [
          { name: 'user_id', in: 'body', type: 'string', required: false, default: '', description: 'Optional explicit id; server generates a UUID when omitted.' },
          { name: 'user_alias', in: 'body', type: 'string', required: false, default: '', description: 'Human label.' },
          { name: 'user_email', in: 'body', type: 'string', required: false, default: '', description: 'Unique email.' },
          { name: 'user_role', in: 'body', type: 'string', required: false, default: 'internal_user', description: 'internal_user | proxy_admin | proxy_admin_viewer.' },
          { name: 'models', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Allowed model ids.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '{}', description: 'Arbitrary JSON metadata.' },
          { name: 'max_budget', in: 'body', type: 'number', required: false, default: 'null', description: 'Spend cap in USD.' },
          { name: 'budget_duration', in: 'body', type: 'string', required: false, default: '', description: 'monthly | weekly | daily reset window.' },
          { name: 'rpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: 'Requests per minute.' },
          { name: 'tpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: 'Tokens per minute.' },
          { name: 'max_parallel_requests', in: 'body', type: 'integer', required: false, default: 'null', description: 'In-flight cap.' },
          { name: 'key_alias', in: 'body', type: 'string', required: false, default: '', description: 'Alias for the auto-created key.' },
          { name: 'duration', in: 'body', type: 'string', required: false, default: '', description: 'Key lifetime, e.g. 30d. Empty or -1 means never expires.' },
          { name: 'auto_create_key', in: 'body', type: 'boolean', required: false, default: 'true', description: 'LiteLLM default true: generate a key and return it in the response.' },
        ],
        examplePayload: `{\n  "user_alias": "alice",\n  "user_email": "alice@example.com",\n  "user_role": "internal_user",\n  "max_budget": 50.0,\n  "rpm_limit": 60\n}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/litellm/user/new" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"user_alias":"alice","user_email":"alice@example.com","max_budget":50.0,"rpm_limit":60}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"user_id":"u-abc123","user_alias":"alice","user_email":"alice@example.com","user_role":"internal_user","models":["gpt-4o"],"max_budget":50.0,"spend":0,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z","key":"sk-xxxxxxxxxxxxxxxx","token_id":"k-def1"}` },
          { status: 400, label: 'Invalid request', body: `{"error":{"type":"invalid_request","message":"invalid character 'x' looking for beginning of object key string"}}` },
          { status: 503, label: 'PG store not configured', body: `{"error":{"type":"pg_store_not_configured","message":"PostgreSQL store is not configured. Set PGSTORE_DSN to enable this route."}}` },
        ],
      },
      {
        method: 'GET', path: '/litellm/user/list', summary: 'List internal users (paginated, filterable, sortable). sort_order defaults to asc.',
        params: [
          { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: 'Page.' },
          { name: 'page_size', in: 'query', type: 'integer', required: false, default: '25', description: 'Rows per page (max 200).' },
          { name: 'role', in: 'query', type: 'string', required: false, default: '', description: 'Filter by role.' },
          { name: 'search', in: 'query', type: 'string', required: false, default: '', description: 'Case-insensitive substring on alias OR email.' },
          { name: 'user_email', in: 'query', type: 'string', required: false, default: '', description: 'Narrow to a specific email.' },
          { name: 'sort_by', in: 'query', type: 'string', required: false, default: 'spend', description: 'spend | created_at | user_alias.' },
          { name: 'sort_order', in: 'query', type: 'string', required: false, default: 'asc', description: 'asc | desc (LiteLLM default asc).' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/user/list?page=1&sort_by=spend" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"users":[{"user_id":"u-abc123","user_alias":"alice","user_role":"internal_user","spend":3.42,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-22T08:30:00Z"}],"page":1,"page_size":25,"total":1,"total_pages":1}` },
        ],
      },
      {
        method: 'GET', path: '/litellm/user/info', summary: 'Get a single internal user by user_id (query param). Returns the LiteLLM UserInfoResponse envelope.',
        params: [{ name: 'user_id', in: 'query', type: 'string', required: true, default: '', description: 'User id.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/user/info?user_id=u-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"user_id":"u-abc123","user_info":{"user_id":"u-abc123","user_alias":"alice","user_email":"alice@example.com","user_role":"internal_user","spend":3.42,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-22T08:30:00Z"},"keys":[{"key":"k-def1","user_id":"u-abc123","key_alias":"prod-app","spend":0,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z"}],"teams":[]}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"user not found"}}` },
          { status: 400, label: 'Missing user_id', body: `{"error":{"type":"invalid_request","message":"user_id is required"}}` },
        ],
      },
      {
        method: 'POST', path: '/litellm/user/update', summary: 'Partial update of an internal user. user_id may come from the JSON body or the query param.',
        params: [
          { name: 'user_id', in: 'body', type: 'string', required: true, default: '', description: 'User id (required; body preferred, query fallback).' },
          { name: 'user_alias', in: 'body', type: 'string', required: false, default: '', description: '' },
          { name: 'user_email', in: 'body', type: 'string', required: false, default: '', description: '' },
          { name: 'user_role', in: 'body', type: 'string', required: false, default: '', description: '' },
          { name: 'models', in: 'body', type: 'string[]', required: false, default: '', description: '' },
          { name: 'max_budget', in: 'body', type: 'number', required: false, default: 'null', description: '' },
          { name: 'budget_duration', in: 'body', type: 'string', required: false, default: '', description: '' },
          { name: 'rpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: '' },
          { name: 'tpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: '' },
          { name: 'max_parallel_requests', in: 'body', type: 'integer', required: false, default: 'null', description: '' },
        ],
        examplePayload: `{"user_id":"u-abc123","max_budget":75.0,"rpm_limit":120}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/litellm/user/update" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"user_id":"u-abc123","max_budget":75.0,"rpm_limit":120}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"user_id":"u-abc123","max_budget":75.0,"rpm_limit":120,"spend":3.42,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-22T08:31:00Z"}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"user not found"}}` },
          { status: 400, label: 'Missing user_id', body: `{"error":{"type":"invalid_request","message":"user_id is required"}}` },
        ],
      },
      {
        method: 'POST', path: '/litellm/user/delete', summary: 'Delete internal users by id. Returns the raw count of users deleted.',
        params: [{ name: 'user_ids', in: 'body', type: 'string[]', required: true, default: '', description: 'User ids to delete (LiteLLM DeleteUserRequest). Associated keys are deleted too.' }],
        examplePayload: `{"user_ids":["u-abc123"]}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/litellm/user/delete" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"user_ids":["u-abc123"]}'`,
        responses: [
          { status: 200, label: 'OK', body: `1` },
          { status: 400, label: 'Missing user_ids', body: `{"error":{"type":"invalid_request","message":"user_ids is required"}}` },
        ],
      },
      {
        method: 'POST', path: '/litellm/key/generate', summary: 'Create a runtime API key owned by an internal user. The plaintext secret is returned ONCE. The key works immediately for real inference traffic.',
        params: [
          { name: 'user_id', in: 'body', type: 'string', required: true, default: '', description: 'Owning internal user id (must exist).' },
          { name: 'models', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Allowed model ids.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '{}', description: 'Arbitrary JSON metadata.' },
          { name: 'max_budget', in: 'body', type: 'number', required: false, default: 'null', description: 'Monthly USD cap.' },
          { name: 'budget_duration', in: 'body', type: 'string', required: false, default: '', description: 'Accepted for spec parity (not stored; the runtime policy tracks monthly budget).' },
          { name: 'rpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: 'Requests per minute cap.' },
          { name: 'tpm_limit', in: 'body', type: 'integer', required: false, default: 'null', description: 'Accepted for spec parity (not stored; the runtime policy has no TPM column).' },
          { name: 'max_parallel_requests', in: 'body', type: 'integer', required: false, default: 'null', description: 'In-flight cap.' },
          { name: 'alias', in: 'body', type: 'string', required: false, default: '', description: 'Non-secret alias.' },
          { name: 'key_alias', in: 'body', type: 'string', required: false, default: '', description: 'Non-secret alias (alias synonym).' },
          { name: 'key_name', in: 'body', type: 'string', required: false, default: 'unnamed', description: 'Human label.' },
          { name: 'name', in: 'body', type: 'string', required: false, default: 'unnamed', description: 'Human label (key_name synonym).' },
          { name: 'duration', in: 'body', type: 'string', required: false, default: '', description: 'Key lifetime, e.g. 30d. Empty or -1 means never expires.' },
          { name: 'expires', in: 'body', type: 'string', required: false, default: '', description: 'Explicit expiry (RFC3339).' },
        ],
        examplePayload: `{\n  "user_id": "u-abc123",\n  "models": ["gpt-4o"],\n  "max_budget": 5.0,\n  "alias": "prod-app"\n}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/litellm/key/generate" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"user_id":"u-abc123","models":["gpt-4o"],"alias":"prod-app"}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"key":"sk-xxxxxxxxxxxxxxxx","user_id":"u-abc123","key_alias":"prod-app","models":["gpt-4o"],"max_budget":5.0,"spend":0,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z","token_id":"k-def1","secret":"sk-xxxxxxxxxxxxxxxx"}` },
          { status: 400, label: 'Missing user_id', body: `{"error":{"type":"invalid_request","message":"user_id is required"}}` },
          { status: 404, label: 'Owner not found', body: `{"error":{"type":"not_found","message":"user not found"}}` },
        ],
      },
      {
        method: 'GET', path: '/litellm/key/list', summary: 'List runtime API keys (paginated, filterable). Returns LiteLLM KeyListResponseObject fields. The secret is never included.',
        params: [
          { name: 'user_id', in: 'query', type: 'string', required: false, default: '', description: 'Filter to keys owned by this internal user.' },
          { name: 'status', in: 'query', type: 'string', required: false, default: '', description: 'active | disabled | revoked | expired.' },
          { name: 'search', in: 'query', type: 'string', required: false, default: '', description: 'Substring on name / alias / prefix.' },
          { name: 'key_alias', in: 'query', type: 'string', required: false, default: '', description: 'Narrow to a specific alias.' },
          { name: 'sort_by', in: 'query', type: 'string', required: false, default: 'created_at', description: 'created_at | name | last_used_at.' },
          { name: 'sort_order', in: 'query', type: 'string', required: false, default: 'desc', description: 'asc | desc (LiteLLM default desc).' },
          { name: 'page', in: 'query', type: 'integer', required: false, default: '1', description: 'Page.' },
          { name: 'size', in: 'query', type: 'integer', required: false, default: '10', description: 'Rows per page (1-100, LiteLLM default 10).' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/key/list?user_id=u-abc123&status=active&size=25" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"keys":[{"key":"k-def1","user_id":"u-abc123","key_alias":"prod-app","spend":0,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z"}],"total_count":1,"current_page":1,"total_pages":1}` },
        ],
      },
      {
        method: 'GET', path: '/litellm/key/info', summary: 'Get a single runtime API key. The key param accepts a plaintext secret, sha256 hash, internal key id, or key_alias. The secret is never included.',
        params: [{ name: 'key', in: 'query', type: 'string', required: true, default: '', description: 'Plaintext sk- secret, 64-char sha256 hash, key id, or key_alias.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/key/info?key=sk-xxxxxxxxxxxxxxxx" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"key":"sk-xxxxxxxxxxxxxxxx","info":{"key":"k-def1","user_id":"u-abc123","key_alias":"prod-app","models":["gpt-4o"],"spend":0,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T10:00:00Z"}}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"key not found"}}` },
          { status: 400, label: 'Missing key', body: `{"error":{"type":"invalid_request","message":"key is required"}}` },
        ],
      },
      {
        method: 'POST', path: '/litellm/key/update', summary: 'Partial update of a runtime API key (name, alias, status, metadata, expiry). The key param accepts a plaintext secret, hash, key id, or key_alias.',
        params: [
          { name: 'key', in: 'body', type: 'string', required: false, default: '', description: 'Key identifier: plaintext sk- secret, hash, key id, or key_alias.' },
          { name: 'key_alias', in: 'body', type: 'string', required: false, default: '', description: 'Alternative identifier: a key_alias that names the key to update.' },
          { name: 'name', in: 'body', type: 'string', required: false, default: '', description: '' },
          { name: 'alias', in: 'body', type: 'string', required: false, default: '', description: '' },
          { name: 'status', in: 'body', type: 'string', required: false, default: '', description: 'active | disabled | revoked.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '', description: '' },
          { name: 'expires', in: 'body', type: 'string', required: false, default: '', description: 'Explicit expiry (RFC3339).' },
        ],
        examplePayload: `{"key":"sk-xxxxxxxxxxxxxxxx","status":"disabled"}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/litellm/key/update" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"key":"sk-xxxxxxxxxxxxxxxx","status":"disabled"}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"key":"k-def1","user_id":"u-abc123","key_alias":"prod-app","status":"disabled","spend":0,"created_at":"2026-07-01T10:00:00Z","updated_at":"2026-07-01T11:00:00Z"}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"key not found"}}` },
          { status: 400, label: 'Missing key', body: `{"error":{"type":"invalid_request","message":"key is required"}}` },
        ],
      },
      {
        method: 'POST', path: '/litellm/key/regenerate', summary: 'Rotate a key\'s secret. The key is passed as a QUERY param (the spec contract). Returns the new plaintext secret ONCE; the old secret stops working immediately.',
        params: [{ name: 'key', in: 'query', type: 'string', required: true, default: '', description: 'Key identifier: plaintext sk- secret, hash, key id, or key_alias.' }],
        examplePayload: null,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/litellm/key/regenerate?key=sk-xxxxxxxxxxxxxxxx" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{"key":"sk-yyyyyyyyyyyyyyyy","user_id":"u-abc123","token_id":"k-def1","secret":"sk-yyyyyyyyyyyyyyyy"}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"key not found"}}` },
          { status: 400, label: 'Missing key', body: `{"error":{"type":"invalid_request","message":"key is required"}}` },
        ],
      },
      {
        method: 'POST', path: '/litellm/key/delete', summary: 'Permanently delete runtime API keys by key or key_alias. Returns {"deleted_keys": [...]}.',
        params: [
          { name: 'keys', in: 'body', type: 'string[]', required: false, default: '', description: 'Key identifiers to delete (plaintext secret, hash, id, or alias).' },
          { name: 'key_aliases', in: 'body', type: 'string[]', required: false, default: '', description: 'Alternative: delete keys by their aliases.' },
        ],
        examplePayload: `{"keys":["sk-xxxxxxxxxxxxxxxx"]}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/litellm/key/delete" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"keys":["sk-xxxxxxxxxxxxxxxx"]}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"deleted_keys":["sk-xxxxxxxxxxxxxxxx"]}` },
          { status: 404, label: 'Not found', body: `{"error":{"type":"not_found","message":"key not found"}}` },
          { status: 400, label: 'Missing keys', body: `{"error":{"type":"invalid_request","message":"keys or key_aliases is required"}}` },
        ],
      },
      {
        method: 'GET', path: '/litellm/spend/logs', summary: 'Spend-log events as a DIRECT array (LiteLLM contract; no pagination envelope). Filters by user, key, request_id, and date range.',
        params: [
          { name: 'user_id', in: 'query', type: 'string', required: false, default: '', description: 'Filter to this internal user id.' },
          { name: 'api_key', in: 'query', type: 'string', required: false, default: '', description: 'Filter to this API key id.' },
          { name: 'request_id', in: 'query', type: 'string', required: false, default: '', description: 'Filter to a single request.' },
          { name: 'model', in: 'query', type: 'string', required: false, default: '', description: 'Filter by model id.' },
          { name: 'start_date', in: 'query', type: 'string', required: false, default: '', description: 'Lower bound (RFC3339 or YYYY-MM-DD).' },
          { name: 'end_date', in: 'query', type: 'string', required: false, default: '', description: 'Upper bound (RFC3339 or YYYY-MM-DD).' },
          { name: 'summarize', in: 'query', type: 'boolean', required: false, default: 'false', description: 'Accepted for spec parity (no summarization applied).' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/spend/logs?user_id=u-abc123&start_date=2026-07-01T00:00:00Z" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `[{"request_id":"req-1","api_key":"prod-app","model":"gpt-4o","call_type":"litellm_completion","spend":1.25,"total_tokens":150,"prompt_tokens":100,"completion_tokens":50,"startTime":"2026-07-01T10:00:00Z","endTime":"2026-07-01T10:00:00Z","status":200}]` },
        ],
      },
      {
        method: 'GET', path: '/litellm/global/spend/report', summary: 'LiteLLM /global/spend/report: one row per api key with a per-model breakdown. start_date and end_date are both required.',
        notes: 'Accepted params beyond the dates: api_key, internal_user_id, team_id (scoping), group_by (team|customer|api_key; only api_key grouping is applied).',
        params: [
          { name: 'start_date', in: 'query', type: 'string', required: true, default: '', description: 'YYYY-MM-DD lower bound (required).' },
          { name: 'end_date', in: 'query', type: 'string', required: true, default: '', description: 'YYYY-MM-DD upper bound, inclusive of its day (required).' },
          { name: 'api_key', in: 'query', type: 'string', required: false, default: '', description: 'Scope to one key (plaintext secret, hash, id, or alias).' },
          { name: 'internal_user_id', in: 'query', type: 'string', required: false, default: '', description: 'Scope to one internal user.' },
          { name: 'team_id', in: 'query', type: 'string', required: false, default: '', description: 'Accepted; mapped to internal_user_id scope.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/global/spend/report?start_date=2026-07-01&end_date=2026-07-31" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `[{"api_key":"k-def1","total_cost":3.42,"total_input_tokens":30000,"total_output_tokens":15000,"model_details":[{"model":"gpt-4o","total_cost":3.42,"total_input_tokens":30000,"total_output_tokens":15000}]}]` },
          { status: 400, label: 'Missing dates', body: `{"error":{"type":"invalid_request","message":"start_date and end_date are required"}}` },
        ],
      },
      {
        method: 'GET', path: '/litellm/spend/tags', summary: 'LiteLLM /spend/tags per-tag spend aggregation.',
        notes: 'The runtime usage_events table has no request_tags column, so this returns an empty array (the LiteLLM shape) rather than erroring.',
        params: [
          { name: 'start_date', in: 'query', type: 'string', required: false, default: '', description: 'YYYY-MM-DD lower bound.' },
          { name: 'end_date', in: 'query', type: 'string', required: false, default: '', description: 'YYYY-MM-DD upper bound.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/spend/tags?start_date=2026-07-01&end_date=2026-07-31" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `[]` },
        ],
      },
      {
        method: 'GET', path: '/litellm/user/spend/report', summary: 'LiteLLM /user/spend/report: spend rows scoped by internal_user_id.',
        params: [
          { name: 'start_date', in: 'query', type: 'string', required: false, default: '', description: 'YYYY-MM-DD lower bound.' },
          { name: 'end_date', in: 'query', type: 'string', required: false, default: '', description: 'YYYY-MM-DD upper bound, inclusive of its day.' },
          { name: 'internal_user_id', in: 'query', type: 'string', required: false, default: '', description: 'Scope to one internal user.' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/user/spend/report?start_date=2026-07-01&end_date=2026-07-31&internal_user_id=u-abc123" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `[{"api_key":"k-def1","total_cost":3.42,"total_input_tokens":30000,"total_output_tokens":15000,"model_details":[{"model":"gpt-4o","total_cost":3.42,"total_input_tokens":30000,"total_output_tokens":15000}]}]` },
        ],
      },
      {
        method: 'GET', path: '/litellm/key/spend/report', summary: 'LiteLLM /key/spend/report: spend rows scoped by api_key.',
        params: [
          { name: 'start_date', in: 'query', type: 'string', required: false, default: '', description: 'YYYY-MM-DD lower bound.' },
          { name: 'end_date', in: 'query', type: 'string', required: false, default: '', description: 'YYYY-MM-DD upper bound, inclusive of its day.' },
          { name: 'api_key', in: 'query', type: 'string', required: false, default: '', description: 'Scope to one key (plaintext secret, hash, id, or alias).' },
        ],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/litellm/key/spend/report?start_date=2026-07-01&end_date=2026-07-31&api_key=sk-xxxxxxxxxxxxxxxx" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `[{"api_key":"k-def1","total_cost":3.42,"total_input_tokens":30000,"total_output_tokens":15000,"model_details":[{"model":"gpt-4o","total_cost":3.42,"total_input_tokens":30000,"total_output_tokens":15000}]}]` },
          { status: 404, label: 'Key not found', body: `{"error":{"type":"not_found","message":"key not found"}}` },
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
          { name: 'secret', in: 'body', type: 'string', required: false, default: '', description: 'Optional custom secret string. When empty the server auto-generates an opaque sk-… value; when supplied it is used verbatim after validation (min 16 chars). Choose a value unique to this deployment — no collision pre-check is performed. Only its SHA-256 hash is persisted (in key_hash); the display key_prefix is derived from the first body chars (a leading sk- marker is stripped).' },
          { name: 'user_id', in: 'body', type: 'string', required: true, default: '', description: 'Owning Internal User id.' },
          { name: 'expires_at', in: 'body', type: 'string', required: false, default: 'null', description: 'RFC3339 expiry timestamp.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '{}', description: 'Arbitrary JSON metadata.' },
          { name: 'policy', in: 'body', type: 'object', required: false, default: 'null', description: 'Per-key Policy (see PUT /:id/policy). May include policy.model_group_id to attach a reusable Model Group at creation time — the group then becomes the source of truth for allowed/blocked/routes (entity fields ignored at enforcement time). May also include allowed_ips / blocked_ips to restrict the key to specific source IP addresses / CIDR ranges.' },
        ],
        examplePayload: `{\n  "name": "prod-app",\n  "secret": "sk-my-team-rotate-key-0123456789",\n  "user_id": "u-abc123",\n  "expires_at": "2026-12-31T23:59:59Z",\n  "policy": {\n    "rpm_limit": 100,\n    "allowed_models": ["gpt-4o"],\n    "model_group_id": "g-abc123",\n    "allowed_ips": ["10.0.0.0/8"],\n    "blocked_ips": ["203.0.113.0/24"]\n  }\n}`,
        exampleCurl: `# Auto-generate the secret (recommended)\ncurl -s -X POST "${'{API_BASE}'}/api-keys-pg" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"name":"prod-app","user_id":"u-abc123"}'\n\n# Or supply your own custom secret string (min 16 chars)\ncurl -s -X POST "${'{API_BASE}'}/api-keys-pg" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"name":"prod-app","secret":"sk-my-team-rotate-key-0123456789","user_id":"u-abc123"}'`,
        responses: [
          { status: 201, label: 'Created', body: `{"id":"k-def1","name":"prod-app","key_prefix":"sk-abcd","status":"active","user_id":"u-abc123","secret":"sk-xxxxxxxxxxxxxxxx","policy":{...}}` },
          { status: 400, label: 'Missing user_id', body: `{"error":{"type":"invalid_request","message":"user_id is required: every API key must be owned by an Internal User"}}` },
          { status: 400, label: 'Invalid secret', body: `{"error":{"type":"invalid_request","message":"postgres store: invalid api key secret: api key secret too short (min 16 chars)"}}` },
          { status: 400, label: 'Invalid IP pattern', body: `{"error":{"type":"invalid_request","message":"allowed_ips: invalid IP pattern: not-an-ip"}}` },
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
          { name: 'allowed_ips', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Source IP allow-list. Entries are single IPs ("10.0.0.5") or CIDR ranges ("10.0.0.0/8", "2001:db8::/32"); IPv4 and IPv6 are both supported. When non-empty the client IP must match at least one entry; empty = all IPs allowed (subject to blocked_ips). Validated at write time — malformed entries return 400.' },
          { name: 'blocked_ips', in: 'body', type: 'string[]', required: false, default: '[]', description: 'Source IP block-list (deny-first). A match always denies the request, even when the IP is also on allowed_ips. Same entry format as allowed_ips.' },
        ],
        examplePayload: `{\n  "rpm_limit": 100,\n  "budget_monthly_usd": 50.0,\n  "allowed_models": ["gpt-4o", "claude-3-5-sonnet"],\n  "allowed_ips": ["10.0.0.0/8", "2001:db8::/32"],\n  "blocked_ips": ["203.0.113.0/24"]\n}`,
        exampleCurl: `curl -s -X PUT "${'{API_BASE}'}/api-keys-pg/k-def1/policy" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"rpm_limit":100,"allowed_models":["gpt-4o"],"allowed_ips":["10.0.0.0/8"]}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"policy":{"api_key_id":"k-def1","rpm_limit":100,"allowed_ips":["10.0.0.0/8","2001:db8::/32"],"blocked_ips":["203.0.113.0/24"],...},"api_key_id":"k-def1"}` },
          { status: 400, label: 'Invalid IP pattern', body: `{"error":{"type":"invalid_request","message":"allowed_ips: invalid CIDR pattern: 10.0.0.0/33"}}` },
          { status: 403, label: 'IP blocked at runtime', body: `{"error":{"type":"forbidden","message":"API key policy blocks this source IP (203.0.113.5)"}}` },
        ],
      },
      {
        method: 'POST', path: '/api-keys-pg/:id/regenerate', summary: 'Issue a new secret — auto-generate one, or rotate to a custom string. The old secret stops working immediately; ID/policy/metadata preserved.',
        params: [
          { name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Key id.' },
          { name: 'secret', in: 'body', type: 'string', required: false, default: '', description: 'Optional custom secret to rotate to. When the body is omitted (or secret is empty) the server auto-generates a fresh opaque sk-… secret; when supplied it is used verbatim after validation (min 16 chars). Choose a value unique to this deployment — no collision pre-check is performed. Only the SHA-256 hash is persisted (key_hash) and the display prefix is rotated to match.' },
        ],
        examplePayload: `{"secret":"sk-my-custom-rotate-key-0123456789"}`,
        exampleCurl: `# Auto-generate a new secret (no body)\ncurl -s -X POST "${'{API_BASE}'}/api-keys-pg/k-def1/regenerate" \\\n  -H "Authorization: Bearer $MGMT_SECRET"\n\n# Or rotate to a custom secret string (min 16 chars)\ncurl -s -X POST "${'{API_BASE}'}/api-keys-pg/k-def1/regenerate" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"secret":"sk-my-custom-rotate-key-0123456789"}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"id":"k-def1","secret":"sk-xxxxxxxxxxxxxxxx"}` },
          { status: 400, label: 'Invalid secret', body: `{"error":{"type":"invalid_request","message":"postgres store: invalid api key secret: api key secret too short (min 16 chars)"}}` },
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
      {
        method: 'POST', path: '/api-keys-pg/import', summary: 'Bulk-import custom API-key secrets by matching each row\'s key_alias (case-insensitive). Each matched secret is applied via Regenerate, preserving the key\'s ID/policy/metadata/owner. Rows are independent — one row\'s failure never aborts the others; the response reports imported vs skipped so callers can reconcile.',
        params: [
          { name: 'keys', in: 'body', type: 'object[]', required: true, default: '', description: 'Array of { alias, key } rows. alias is the target key_alias (matched case-insensitively, trimmed); key is the plaintext custom secret to apply (trimmed, min 16 chars). Only the SHA-256 hash of the secret is stored. Empty body or >1000 rows → 400.' },
          { name: 'alias', in: 'body', type: 'string', required: true, default: '', description: 'Per-row key_alias to match against (case-insensitive).' },
          { name: 'key', in: 'body', type: 'string', required: true, default: '', description: 'Per-row plaintext custom secret (min 16 chars). Only its SHA-256 hash is persisted.' },
        ],
        examplePayload: `{\n  "keys": [\n    { "alias": "prod-app", "key": "sk-my-team-rotate-key-0123456789" },\n    { "alias": "staging-app", "key": "sk-staging-rotate-key-0123456789" }\n  ]\n}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/api-keys-pg/import" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{"keys":[{"alias":"prod-app","key":"sk-my-team-rotate-key-0123456789"}]}'`,
        responses: [
          { status: 200, label: 'OK', body: `{"total":2,"imported":1,"skipped":[{"alias":"staging-app","reason":"alias not found"}]}` },
          { status: 400, label: 'Empty body', body: `{"error":{"type":"invalid_request","message":"keys: at least one entry is required"}}` },
          { status: 400, label: 'Too many rows', body: `{"error":{"type":"invalid_request","message":"too many keys: 1001 (max 1000)"}}` },
          { status: 503, label: 'PG store not configured', body: `{"error":{"type":"pg_store_not_configured","message":"..."}}` },
        ],
        notes: 'Each skipped row carries the failing alias and one of these reasons: "alias not found" (no key has that alias), "ambiguous alias" (multiple keys share the alias), "duplicate secret" (the secret is already in use by a different key), "invalid secret" (too short, <16 chars), or "missing alias or key" (either field empty). Matching is case-insensitive; rows are processed independently, so a partial import is not all-or-nothing.',
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
      'Per-model caps live on model_routes entries: rpm_limit (requests/min per model, HTTP 429) and max_budget_usd (total lifetime spend per model, computed from usage_events, HTTP 402). ' +
      'Discounts: discount_pct (group-level default, 0-100) and per-model model_routes[].discount_pct reduce the recorded cost_usd (20 = 80% of the model pricing). Per-model overrides win over the group default. ' +
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
          { status: 200, label: 'OK', body: `{\n  "groups": [\n    {\n      "id": "g-abc123",\n      "name": "gpt-only",\n      "description": "GPT family + gpt-4o pinned to openai oauth",\n      "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n      "blocked_models": [],\n      "model_routes": [\n        { "model": "gpt-4o", "providers": ["openai"], "rpm_limit": 60, "max_budget_usd": 25.00, "discount_pct": 10 }\n      ],\n      "model_rpm_limits": { "gpt-4o": 60 },\n      "model_budget_limits": { "gpt-4o": 25.00 },\n      "discount_pct": 10,\n      "model_discount_pcts": { "gpt-4o": 10 },\n      "metadata": {},\n      "created_at": "2026-07-22T09:00:00Z",\n      "updated_at": "2026-07-22T09:05:00Z"\n    }\n  ],\n  "page": 1,\n  "page_size": 25,\n  "total": 1,\n  "total_pages": 1\n}` },
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
          { name: 'model_routes', in: 'body', type: 'object[]', required: false, default: '[]', description: 'Per-concrete-model upstream provider pinning + caps + discounts. Each entry: { model, providers: string[], strategy?, priorities?, rpm_limit?, max_budget_usd?, discount_pct? }. model must be in allowed_models; wildcard models cannot be routed or carry caps/discounts. rpm_limit caps requests/min (429 on breach); max_budget_usd caps total lifetime USD spend for that model on attached keys, computed from usage_events (402 on breach); discount_pct (0-100) reduces cost_usd (per-model wins over the group default). A cap/discount-only entry may omit providers.' },
          { name: 'discount_pct', in: 'body', type: 'number', required: false, default: 'null', description: 'Group-level default discount percentage (0-100) applied to cost_usd of every request. Per-model discount_pct on model_routes takes precedence. 0/null = no discount.' },
          { name: 'metadata', in: 'body', type: 'object', required: false, default: '{}', description: 'Arbitrary JSON metadata.' },
        ],
        examplePayload: `{\n  "name": "gpt-only",\n  "description": "GPT family + gpt-4o pinned to openai oauth with per-model caps",\n  "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n  "discount_pct": 10,\n  "model_routes": [\n    { "model": "gpt-4o", "providers": ["openai"], "rpm_limit": 60, "max_budget_usd": 25.00, "discount_pct": 20 }\n  ]\n}`,
        exampleCurl: `curl -s -X POST "${'{API_BASE}'}/model-groups" \\\n  -H "Authorization: Bearer $MGMT_SECRET" \\\n  -H "Content-Type: application/json" \\\n  -d '{\n    "name": "gpt-only",\n    "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n    "discount_pct": 10,\n    "model_routes": [{ "model": "gpt-4o", "providers": ["openai"], "rpm_limit": 60, "max_budget_usd": 25.00, "discount_pct": 20 }]\n  }'`,
        responses: [
          { status: 201, label: 'Created', body: `{"id":"g-abc123","name":"gpt-only","allowed_models":["gpt-4o","gpt-4o-mini","gpt-4*"],"model_routes":[{"model":"gpt-4o","providers":["openai"],"rpm_limit":60,"max_budget_usd":25,"discount_pct":20}],"model_rpm_limits":{"gpt-4o":60},"model_budget_limits":{"gpt-4o":25},"discount_pct":10,"model_discount_pcts":{"gpt-4o":20},"created_at":"2026-07-22T09:00:00Z","updated_at":"2026-07-22T09:00:00Z"}` },
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
          { status: 200, label: 'OK', body: `{\n  "group": {\n    "id": "g-abc123",\n    "name": "gpt-only",\n    "allowed_models": ["gpt-4o", "gpt-4o-mini", "gpt-4*"],\n    "blocked_models": [],\n    "model_routes": [{ "model": "gpt-4o", "providers": ["openai"], "rpm_limit": 60, "max_budget_usd": 25.00, "discount_pct": 10 }],\n    "model_rpm_limits": { "gpt-4o": 60 },\n    "model_budget_limits": { "gpt-4o": 25.00 },\n    "discount_pct": 10,\n    "model_discount_pcts": { "gpt-4o": 10 },\n    "metadata": {},\n    "created_at": "2026-07-22T09:00:00Z",\n    "updated_at": "2026-07-22T09:05:00Z"\n  },\n  "attachments": [\n    {\n      "entity_id": "k-def1",\n      "entity_label": "prod-app",\n      "entity_user_id": "u-abc123",\n      "entity_user_alias": "alice",\n      "entity_user_email": "alice@example.com"\n    }\n  ]\n}` },
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
          { name: 'model_routes', in: 'body', type: 'object[]', required: false, default: '—', description: 'Replace routes (incl. per-model rpm_limit/max_budget_usd/discount_pct).' },
          { name: 'discount_pct', in: 'body', type: 'number', required: false, default: '—', description: 'Replace the group-level default discount (0-100). Pass 0 to clear.' },
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
    id: 'model-routing-picker',
    title: 'Model Routing Picker',
    description:
      'Zero-downtime routing design (2026-09-18). The picker lists every configured upstream as a candidate ' +
      'for a model, partitioned into LIVE and STALE based on the runtime registry. Pinning assigns a priority ' +
      'atomically as MAX(existing pinned priorities) + 1, with a default floor of 10 when no pins exist. ' +
      'Returns 503 when PG is not configured.',
    endpoints: [
      {
        method: 'GET',
        path: '/model-routing/picker',
        summary: 'List picker candidates for a model.',
        params: [
          { name: 'model', in: 'query', required: true, type: 'string', description: 'Model id to pick upstreams for.' },
        ],
        exampleCurl: `curl -H "Authorization: Bearer $TOKEN" "$API_BASE/model-routing/picker?model=gpt-4o"`,
        responses: [
          {
            status: 200,
            label: 'OK',
            body: `{
  "model": "gpt-4o",
  "live":  [{"provider_key":"openai:42","name":"OpenAI prod","level":"provider","count":2,"live":true,"suggested_priority":11}],
  "stale": [{"provider_key":"claude:7","name":"Claude A","level":"provider","count":1,"live":false,"suggested_priority":11}],
  "pinned":[{"provider_key":"openai:42","name":"OpenAI prod","priority":10,"is_live":true}]
}`,
          },
          { status: 400, label: 'Missing model', body: `{"error":{"type":"invalid_request","message":"model query parameter is required"}}` },
          { status: 503, label: 'PG not configured', body: `{"error":{"type":"pg_not_configured","message":"PG store is not configured"}}` },
        ],
      },
      {
        method: 'POST',
        path: '/model-routing/pin',
        summary: 'Pin a single provider to a model with atomic MAX(priority)+1.',
        examplePayload: `{"model":"gpt-4o","provider_key":"openai:42","force":false}`,
        exampleCurl: `curl -X POST -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json" -d '{"model":"gpt-4o","provider_key":"openai:42"}' "$API_BASE/model-routing/pin"`,
        responses: [
          {
            status: 200,
            label: 'OK',
            body: `{"model":"gpt-4o","provider_key":"openai:42","priority":11,"was_existing":false}`,
          },
          {
            status: 200,
            label: 'Already pinned (idempotent)',
            body: `{"model":"gpt-4o","provider_key":"openai:42","priority":10,"was_existing":true}`,
          },
          { status: 400, label: 'Missing field', body: `{"error":{"type":"invalid_request","message":"model and provider_key are required"}}` },
          { status: 409, label: 'Not live', body: `{"error":{"type":"not_live","message":"provider is not LIVE for this model; pass force=true to pin anyway"}}` },
          { status: 503, label: 'PG not configured', body: `{"error":{"type":"pg_not_configured","message":"PG store is not configured"}}` },
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
          { status: 200, label: 'OK', body: `{"totals":{"request_count":1024,"failed_count":12,"total_tokens":450000,"cost_usd":12.34},"failure_rate":1.158,"success_count":1024,"total_attempts":1036}` },
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

  // =========================================================================
  // 5. Quota Share
  // =========================================================================
  {
    id: 'quota-share',
    title: 'Quota Share',
    description:
      'Live per-window headroom for upstream provider pools, aggregated from the PG usage_windows table. ' +
      'Pools are identified by the round-1 (channel:rowID) compound key (mirrors PoolBreaker / ' +
      'PoolStrategyForProviderKeys). All routes return 503 when the PG store is not configured. ' +
      'A 2s query timeout returns 200 + partial=true so a single slow PG row never blacks out the ' +
      'panel.',
    endpoints: [
      {
        method: 'GET', path: '/auths/:id/quota', summary: 'Live per-window quota for one auth.',
        params: [{ name: 'id', in: 'path', type: 'string', required: true, default: '', description: 'Auth id.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/auths/auth-1/quota" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{\n  "auth_id": "auth-1",\n  "channel": "openai",\n  "pool_strategy": "fallback",\n  "windows": [\n    { "size": "1m", "used": 12345, "limit": 1000000, "headroom_pct": 98.8, "over_limit": false },\n    { "size": "1h", "used": 200000, "limit": 5000000, "headroom_pct": 96.0, "over_limit": false },\n    { "size": "1d", "used": 1500000, "limit": 50000000, "headroom_pct": 97.0, "over_limit": false }\n  ],\n  "models": [\n    { "model": "gpt-5", "used_1h": 80000, "limit_1h": 1000000 }\n  ],\n  "partial": false\n}` },
          { status: 503, label: 'PG store not configured', body: `{"error":"PG storage not enabled"}` },
        ],
      },
      {
        method: 'GET', path: '/pools/:key/quota', summary: 'Live per-window quota summed across one pool.',
        params: [{ name: 'key', in: 'path', type: 'string', required: true, default: '', description: 'Pool key in (channel:rowID) compound form (e.g. "oauth:openai:5"). The auths[] brief lists every contributing auth id; empty when the pool has no live auths.' }],
        examplePayload: null,
        exampleCurl: `curl -s "${'{API_BASE}'}/pools/oauth%3Aopenai%3A5/quota" \\\n  -H "Authorization: Bearer $MGMT_SECRET"`,
        responses: [
          { status: 200, label: 'OK', body: `{\n  "pool_key": "oauth:openai:5",\n  "windows": [\n    { "size": "1m", "used": 60000, "limit": 1000000, "headroom_pct": 94.0, "over_limit": false },\n    { "size": "1h", "used": 800000, "limit": 5000000, "headroom_pct": 84.0, "over_limit": false }\n  ],\n  "models": [\n    { "model": "gpt-4o", "used_1h": 500000, "limit_1h": 4000000 }\n  ],\n  "auths": [\n    { "auth_id": "auth-1", "channel": "openai", "pool_strategy": "fallback" },\n    { "auth_id": "auth-2", "channel": "openai", "pool_strategy": "fallback" }\n  ],\n  "partial": false\n}` },
          { status: 200, label: 'Partial (timeout)', body: `{"pool_key":"oauth:openai:5","windows":[...],"models":[],"auths":[...],"partial":true}` },
          { status: 503, label: 'PG store not configured', body: `{"error":"PG storage not enabled"}` },
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
