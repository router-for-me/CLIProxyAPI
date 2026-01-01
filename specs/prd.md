# Product Requirements Document (PRD)

## 1. Introduction
**CLIProxyAPI-Extended** is a fork of [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) designed to improve compatibility with AI coding clients (e.g., Cursor, Copilot Chat) and simplify the codebase through a unified **Canonical Intermediate Representation (IR)** architecture.

## 2. Goals & Objectives
- **Unified Architecture**: Consolidate translation logic into a hub-and-spoke model using a single intermediate representation (IR).
- **Expanded Provider Support**: Add support for Kiro (Amazon Q), GitHub Copilot, Cline, and Ollama.
- **Enhanced Compatibility**: Better support for AI coding assistants (Cursor, Copilot Chat) via proper tool schema conversion and response handling.
- **Codebase Optimization**: Reduce complexity and duplication (achieved ~62% code reduction).

## 3. Core Features

### 3.1 Canonical IR Translator
- **Unified IR**: All input formats (OpenAI, Claude, Ollama, etc.) are converted to a single internal format before being translated to the target provider's format.
- **Hub-and-Spoke Model**:
  - `to_ir/`: Parsers for incoming requests.
  - `ir/`: Core IR definitions and utilities.
  - `from_ir/`: Emitters for outgoing requests to providers.
- **Toggle**: Configurable via `use-canonical-translator` (default: `true`).

### 3.2 New Providers
- **Ollama**: Full Ollama-compatible server implementation.
- **Kiro (Amazon Q)**: Access to Claude via Amazon Q with multiple auth methods (AWS Builder ID, Social Auth, Manual).
- **GitHub Copilot**: Access to GPT-4o, Claude Sonnet, etc., via OAuth Device Flow.
- **Cline**: Support for free models (MiniMax M2, Grok) using OpenAI-compatible format.

### 3.3 Enhanced Functionality
- **Provider Prefixes**: Visual identification of providers in model lists (e.g., `[Gemini CLI] gemini-2.5-flash`).
- **Load Balancing**: Round-robin selection for models available across multiple providers.
- **Reasoning Support**: Unified handling of thinking blocks and `reasoning_tokens`.
- **Streaming**: Support for SSE (OpenAI/Claude) and NDJSON (Gemini/Ollama).

## 4. Architecture

### 4.1 High-Level Flow
```
Any Input  →  Unified IR  →  Any Output
```

### 4.2 Directory Structure (`translator_new/`)
- **`ir/`**: Core types and utilities.
- **`to_ir/`**: Parsers (OpenAI, Claude, Gemini, Ollama, Kiro).
- **`from_ir/`**: Emitters (OpenAI, Claude, Gemini, Ollama, Kiro).

## 5. Technical Requirements
- **Language**: Go
- **Configuration**: YAML-based (`config.yaml`).
- **Authentication**:
  - OAuth Device Flow (GitHub Copilot).
  - AWS Builder ID / Social Auth (Kiro).
  - Standard OAuth (Gemini, Claude).
  - Refresh Tokens (Cline).

## 6. Success Metrics
- Codebase reduction (~62% lines of code).
- Translation path reduction (27 → 10).
- Successful integration with key clients (Cursor, Copilot Chat).
- Stability of new providers (Kiro, Copilot, Cline).

## 7. Future Scope
- [ ] Fix thinking mode for GPT-OSS models.
- [ ] Migrate remaining executors (iFlow) to Canonical IR (Qwen is done).
- [ ] Expand testing for CLI agents (Codex CLI, Aider).
