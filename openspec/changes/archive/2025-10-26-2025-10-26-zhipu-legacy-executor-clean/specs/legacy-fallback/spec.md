## ADDED Requirements

### Requirement: Remove legacy fallback to OpenAICompatExecutor
The system MUST remove the legacy fallback path that routed provider="zhipu" requests to `OpenAICompatExecutor`. All requests MUST go through the Python Agent Bridge.

#### Scenario: No fallback available
Given provider="zhipu" and any model
When the bridge is misconfigured or unavailable
Then the system MUST return a diagnostic error rather than silently routing to a legacy executor.
