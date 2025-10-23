## MODIFIED Requirements

### Requirement: Provider Model Inventory Exposure
The system SHALL expose available models per provider via the global model registry and HTTP APIs.

#### Scenario: Copilot exposes only gpt-5-mini (BREAKING)
- GIVEN provider `copilot`
- WHEN listing models
- THEN the system SHALL expose only one model: `gpt-5-mini`
- AND the model metadata OwnedBy SHALL be `copilot` and Type SHALL be `copilot`
- AND the OpenAI provider SHALL NOT include `gpt-5-mini` in its inventory

#### Scenario: OpenAI model set unchanged otherwise
- GIVEN provider `codex` or `openai`
- WHEN listing models
- THEN the system SHALL expose the existing OpenAI models excluding `gpt-5-mini`

