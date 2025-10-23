## MODIFIED Requirements

### Requirement: Provider Model Inventory Exposure (Copilot Rules)
The system SHALL treat `copilot` as an independent provider whose model inventory is not mirrored from OpenAI.

#### Scenario: Copilot-only model visibility
- GIVEN provider `copilot`
- WHEN listing models via `/v1/models` or management API
- THEN the system SHALL expose only `gpt-5-mini`
- AND that model SHALL NOT appear under providers `codex`, `openai`, or any OpenAI-compat provider

#### Scenario: Provider filtering behavior
- WHEN requesting `GET /v0/management/models?provider=copilot`
- THEN results SHALL include `gpt-5-mini` with `providers` containing `copilot`
- AND `GET /v0/management/providers` SHALL include `copilot` as a distinct provider

