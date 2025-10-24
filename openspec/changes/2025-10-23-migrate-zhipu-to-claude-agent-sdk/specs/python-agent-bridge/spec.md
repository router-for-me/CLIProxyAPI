## MODIFIED Requirements: Zhipu Provider Execution

#### Requirement: Replace direct Zhipu HTTP executor with Python Agent SDK bridge
- The system MUST route all provider="zhipu" executions to a Python Agent Bridge (PAB) that uses Claude Agent SDK (Python) to call GLM models.
- The Go service MUST expose the same OpenAI-compatible endpoints (/v1/chat/completions, streaming and non-streaming) unchanged.
- The PAB MUST support both streaming and non-streaming responses and error pass-through semantics.

#### Scenario: Non-streaming chat completions
Given provider="zhipu" and model="glm-4.6"
When client POSTs /v1/chat/completions without stream=true
Then Go forwards the translated request to PAB and returns the JSON result unchanged in OpenAI format.

#### Scenario: Streaming chat completions
Given provider="zhipu" and model="glm-4.6" with stream=true
When client POSTs /v1/chat/completions
Then Go forwards to PAB using an SSE-compatible stream and relays chunks to client, ending with [DONE].

#### Scenario: Error propagation
When PAB returns an HTTP error (>=400)
Then Go MUST return the same status and a JSON error body preserving message/code when present.

## ADDED Requirements: Configuration & Rollout

#### Requirement: pythonAgent config
- Config MUST include:
  - pythonAgent.enabled (bool, default true)
  - pythonAgent.baseURL (string, default http://127.0.0.1:35331)
  - pythonAgent.env map for exporting Zhipu credentials to PAB process when managed as sidecar (optional)

#### Scenario: Rollback
When pythonAgent.enabled=false
Then provider="zhipu" MUST fallback to legacy Go ZhipuExecutor.

