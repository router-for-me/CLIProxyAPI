# Frontend Auth Exclusive Plugin Example

This example registers a frontend auth provider with `frontend_auth_provider_exclusive: true`.

When enabled and selected, this provider becomes the only request authentication provider. Built-in config API keys and other frontend auth providers do not authenticate requests while this provider is active.

The example accepts requests that include:

```http
X-Example-Frontend-Auth: exclusive
```

Build:

```bash
cd examples/plugin/frontend-auth-exclusive/go
go build -buildmode=c-shared -o /tmp/cliproxy-frontend-auth-exclusive.dylib .
```

