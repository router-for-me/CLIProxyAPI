## ADDED Requirements

### Requirement: Route zhipu via Python Agent Bridge (Default and Mandatory)
The system MUST route provider="zhipu" requests to a Python Agent Bridge (PAB) using Claude Agent SDK for Python to call GLM models. This MUST be the default behavior; configuration MUST NOT provide a legacy fallback path once cleanup is complete.

#### Scenario: Non-streaming chat via PAB
Given provider="zhipu" and model="glm-4.6"
When client POSTs /v1/chat/completions with stream=false
Then request is translated to OpenAI format and forwarded to PAB, and JSON result is returned unchanged in OpenAI format.

#### Scenario: Streaming chat via PAB
Given provider="zhipu" and model="glm-4.6" with stream=true
When client POSTs /v1/chat/completions
Then server relays SSE chunks from PAB and ends with [DONE].

### Requirement: Preserve OpenAI-compatible endpoints
The Go service MUST keep OpenAI-compatible endpoints unchanged (/v1/chat/completions), supporting streaming and non-streaming when routed through PAB.

#### Scenario: Endpoint compatibility
Given provider="zhipu"
When client uses /v1/chat/completions (stream or non-stream)
Then responses remain OpenAI-compatible in structure and semantics.

### Requirement: Bridge URL selection priority
Bridge URL selection priority MUST be: (1) config.claude-agent-sdk-for-python.baseURL, (2) env CLAUDE_AGENT_SDK_URL, (3) ensureClaudePythonBridge() default.

#### Scenario: Bridge URL precedence
Given all three sources configured with different values
When resolving the bridge URL
Then config.claude-agent-sdk-for-python.baseURL is used; if empty, fallback to env; else use ensureClaudePythonBridge default.
