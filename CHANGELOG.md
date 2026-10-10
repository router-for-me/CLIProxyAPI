# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/).

## [Unreleased]

### Added
- **Native OpenCode Provider & Free Tier Support**:
  - Implemented OpenCode Zen reverse-engineered fingerprint engine (`internal/runtime/executor/opencode_cloaking.go`):
    - Injects dynamic 48-bit bitwise-inverted Base62 session and request IDs (`ses_` and `msg_`).
    - Supplies dummy agent tool schemas (`read`, `bash`, `write`) to completely eliminate the `FreeTierError` cloud gate restriction.
    - Implemented transparent server-side streaming aggregation to support both SSE streaming and non-streaming HTTP client calls.
  - Added native OpenCode model catalog (`internal/registry/opencode_models.go`) exposing 1M context free models (`step-5-preview-free`, `longcat-2.5-preview-free`, `mimo-v2.6-flash-free`, `ling-3.1-flash-free`, etc.) and Zen flagship models without requiring manual model entries in `config.yaml`.
  - Added native OpenRouter baseline model catalog (`internal/registry/openrouter_models.go`) enabling zero-configuration model discovery for OpenRouter endpoints.
  - Registered `opencode` as an auto-bound executor and model catalog provider in `sdk/cliproxy/service_executors.go` and `sdk/cliproxy/service_models.go`.
