# auth Specification

## Purpose
TBD - created by archiving change add-copilot-provider-models-and-auth. Update Purpose after archive.
## Requirements
### Requirement: Copilot Device Code and Token Exchange Contract
The system SHALL implement Copilot authentication via GitHub Device Flow and Copilot token exchange with explicit headers and formats.

#### Scenario: Request device code returns JSON
- WHEN the client requests `/login/device/code` with form `client_id` and `scope`
- THEN the request MUST include header `Accept: application/json`
- AND the response SHALL be JSON containing `device_code`, `user_code`, `verification_uri`, `expires_in`, and `interval`

#### Scenario: Poll OAuth token until approved
- WHEN polling `/login/oauth/access_token` with the issued `device_code`
- THEN the client SHALL respect the `interval` value between requests
- AND upon approval the response SHALL include `access_token`

#### Scenario: Exchange Copilot token with required headers
- WHEN exchanging Copilot token at `/copilot_internal/v2/token` using the GitHub `access_token`
- THEN the request MUST include headers:
  - `Authorization: token <access_token>`
  - `Accept: application/json`
  - `User-Agent: cli-proxy-copilot`
  - `OpenAI-Intent: copilot-cli-login`
  - `Editor-Plugin-Name: cli-proxy`
  - `Editor-Plugin-Version: 1.0.0`
  - `Editor-Version: cli/1.0`
  - `X-GitHub-Api-Version: 2023-07-07`
- AND the response SHALL be JSON containing `token`, `expires_at`, and `refresh_in`

#### Scenario: Persist Copilot credentials
- WHEN the Copilot token is issued
- THEN the system SHALL persist credentials under the auth directory as a JSON file
- AND the stored metadata SHALL include provider type `copilot` and `access_token`

