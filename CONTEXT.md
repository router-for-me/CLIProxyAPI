# Provider and Account Authentication Context

This context defines the provider and account terms used when the proxy exposes OpenAI subscription authentication through its Codex-compatible runtime.

## Provider surfaces

**OpenAI account**:
An OpenAI subscription account authenticated through the ChatGPT OAuth flow. It is an external provider surface that lets clients select account authentication and Codex-compatible models.
_Avoid_: OpenAI API key, OpenAI-compatible provider

**Codex provider**:
The canonical provider identity for OpenAI account credentials and Codex-compatible execution.
_Avoid_: OpenAI account when referring to the internal provider identity

**Canonical provider**:
The provider identity used by credential storage, runtime selection, and account scheduling after an external provider name is normalized.
_Avoid_: display provider, login label

## Request capabilities

**Model**:
The model identifier selected for an OpenAI account request.

**Reasoning effort**:
The requested reasoning depth for a model, expressed as a supported level such as `low`, `medium`, `high`, or `xhigh`.

**Fast mode**:
A request preference for the accelerated service tier when the selected model and account support it.
_Avoid_: a separate model, a separate account
