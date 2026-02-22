# Management API

Management endpoints provide runtime inspection and administrative controls.

## Access Model

- Surface path: `/v0/management/*`
- Protected by management key.
- Disabled entirely when `remote-management.secret-key` is empty.

### Enable and Protect Management Access

```yaml
remote-management:
  allow-remote: false
  secret-key: "replace-with-strong-secret"
```

Use either header style:

- `Authorization: Bearer <management-key>`
- `X-Management-Key: <management-key>`

## Common Endpoints

- `GET /v0/management/config`
- `GET /v0/management/config.yaml`
- `GET /v0/management/auth-files`
- `GET /v0/management/logs`
- `POST /v0/management/api-call`
- `GET /v0/management/quota-exceeded/switch-project`
- `PUT|PATCH /v0/management/quota-exceeded/switch-project`
- `GET /v0/management/quota-exceeded/switch-preview-model`
- `PUT|PATCH /v0/management/quota-exceeded/switch-preview-model`

Note: some management routes are provider/tool-specific and may vary by enabled features.

## Practical Examples

Read effective config:

```bash
curl -sS http://localhost:8317/v0/management/config \
  -H "Authorization: Bearer <management-key>" | jq
```

Inspect auth file summary:

```bash
curl -sS http://localhost:8317/v0/management/auth-files \
  -H "X-Management-Key: <management-key>" | jq
```

Tail logs stream/snapshot:

```bash
curl -sS "http://localhost:8317/v0/management/logs?lines=200" \
  -H "Authorization: Bearer <management-key>"
```

Read current quota fallback toggles:

```bash
curl -sS http://localhost:8317/v0/management/quota-exceeded/switch-project \
  -H "Authorization: Bearer <management-key>" | jq
curl -sS http://localhost:8317/v0/management/quota-exceeded/switch-preview-model \
  -H "Authorization: Bearer <management-key>" | jq
```

Update quota fallback toggles:

```bash
curl -sS -X PUT http://localhost:8317/v0/management/quota-exceeded/switch-project \
  -H "Authorization: Bearer <management-key>" \
  -H "Content-Type: application/json" \
  -d '{"value":true}'
curl -sS -X PUT http://localhost:8317/v0/management/quota-exceeded/switch-preview-model \
  -H "Authorization: Bearer <management-key>" \
  -H "Content-Type: application/json" \
  -d '{"value":true}'
```

## Failure Modes

- `404` on all management routes: management disabled (empty secret key).
- `401`: invalid or missing management key.
- `403`: remote request blocked when `allow-remote: false`.
- `500`: malformed config/auth state causing handler errors.

## Operational Guidance

- Keep `allow-remote: false` unless absolutely required.
- Place management API behind private network or VPN.
- Rotate management key and avoid storing it in shell history.

## Related Docs

- [Operations API](./operations.md)
- [Troubleshooting](/troubleshooting)
