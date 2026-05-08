# GitLab Duo guide

CLIProxyAPI can now use GitLab Duo as a first-class provider instead of treating it as a plain text wrapper.

It supports:

- OAuth login
- personal access token login
- automatic refresh of GitLab `direct_access` metadata
- dynamic model discovery from GitLab metadata
- native GitLab AI gateway routing for Anthropic and OpenAI/Codex managed models
- Claude-compatible and OpenAI-compatible downstream APIs

## What this means

If GitLab Duo returns an Anthropic-managed model, CLIProxyAPI routes requests through the GitLab AI gateway Anthropic proxy and uses the existing Claude executor path.

If GitLab Duo returns an OpenAI-managed model, CLIProxyAPI routes requests through the GitLab AI gateway OpenAI proxy and uses the existing Codex/OpenAI executor path.

That gives GitLab Duo much closer runtime behavior to the built-in `codex` provider:

- Claude-compatible clients can use GitLab Duo models through `/v1/messages`
- OpenAI-compatible clients can use GitLab Duo models through `/v1/chat/completions`
- OpenAI Responses clients can use GitLab Duo models through `/v1/responses`

The model list is not hardcoded. CLIProxyAPI reads the current model metadata from GitLab `direct_access` and registers:

- a stable alias: `gitlab-duo`
- any discovered managed model names, such as `claude-sonnet-4-5` or `gpt-5-codex`

## Login

OAuth login:

```bash
./CLIProxyAPI -gitlab-login
```

PAT login:

```bash
./CLIProxyAPI -gitlab-token-login
```

You can also provide inputs through environment variables:

```bash
export GITLAB_BASE_URL=https://gitlab.com
export GITLAB_OAUTH_CLIENT_ID=your-client-id
export GITLAB_OAUTH_CLIENT_SECRET=your-client-secret
export GITLAB_PERSONAL_ACCESS_TOKEN=glpat-...
```

Notes:

- OAuth requires a GitLab OAuth application.
- PAT login requires a personal access token that can call the GitLab APIs used by Duo. In practice, `api` scope is the safe baseline.
- Self-managed GitLab instances are supported through `GITLAB_BASE_URL`.

## Using the models

After login, start CLIProxyAPI normally and point your client at the local proxy.

You can select:

- `gitlab-duo` to use the current Duo-managed model for that account
- the discovered provider model name if you want to pin it explicitly

Examples:

```bash
curl http://127.0.0.1:8080/v1/models
```

```bash
curl http://127.0.0.1:8080/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "gitlab-duo",
    "messages": [
      {"role": "user", "content": "Write a Go HTTP middleware for request IDs."}
    ]
  }'
```

If the GitLab account is currently mapped to an Anthropic model, Claude-compatible clients can use the same account through the Claude handler path. If the account is currently mapped to an OpenAI/Codex model, OpenAI-compatible clients can use `/v1/chat/completions` or `/v1/responses`.

## How model freshness works

CLIProxyAPI does not ship a fixed GitLab Duo model catalog.

Instead, it refreshes GitLab `direct_access` metadata and uses the returned `model_details` and any discovered model list entries to keep the local registry aligned with the current GitLab-managed model assignment.

This matches GitLab's current public contract better than hardcoding model names.

## Current scope

The GitLab Duo provider now has:

- OAuth and PAT auth flows
- runtime refresh of Duo gateway credentials
- native Anthropic gateway routing
- native OpenAI/Codex gateway routing
- handler-level smoke tests for Claude-compatible and OpenAI-compatible paths

Still out of scope today:

- websocket or session-specific parity beyond the current HTTP APIs
- GitLab-specific IDE features that are not exposed through the public gateway contract

## References

- GitLab Code Suggestions API: https://docs.gitlab.com/api/code_suggestions/
- GitLab Agent Assistant and managed credentials: https://docs.gitlab.com/user/duo_agent_platform/agent_assistant/
- GitLab Duo model selection: https://docs.gitlab.com/user/gitlab_duo/model_selection/
