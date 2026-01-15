# Voila AI Wrapper for Windmill

OpenAI-compatible API wrapper for [Voila AI](https://getvoila.ai) (ChatGPT wrapper service).

## Overview

This wrapper converts OpenAI-format API calls to Voila's proprietary format, allowing you to:
- Use Voila's ChatGPT access through standard OpenAI API format
- Integrate with CLIProxy as an `openai-compatibility` provider
- Centralize AI provider management

## Architecture

```
┌──────────┐    ┌───────────┐    ┌──────────────┐    ┌───────────┐    ┌─────────┐
│  Client  │───▶│ CLIProxy  │───▶│   Windmill   │───▶│  Voila    │───▶│ ChatGPT │
│          │    │           │    │   Wrapper    │    │   API     │    │         │
└──────────┘    └───────────┘    └──────────────┘    └───────────┘    └─────────┘
     │                │                  │                  │               │
     │                │                  │                  │               │
  OpenAI          OpenAI            Transform           Voila           OpenAI
  format          format         OpenAI↔Voila          format          (internal)
```

## Files

| File | Description | Windmill Path |
|------|-------------|---------------|
| `chat_completions.ts` | Non-streaming chat endpoint | `f/voila/chat_completions` |
| `chat_completions_stream.ts` | Streaming SSE chat endpoint | `f/voila/chat_completions_stream` |
| `models.ts` | List available models | `f/voila/models` |
| `resource_type.json` | Windmill resource type schema | - |

## Setup Instructions

### 1. Create Resource in Windmill

1. Go to Windmill Dashboard → Resources → Create Resource
2. Select type: `Custom` or create new type using `resource_type.json`
3. Name: `u/chipvn/chatgpt` (or your preferred name)
4. Fill in your Voila credentials:

```json
{
  "auth_token": "your-voila-auth-token",
  "email": "your-email@example.com",
  "user_uuid": "your-voila-user-uuid",
  "base_url": "https://chat.getvoila.ai/api/v1/prompts/chat",
  "language": "vi",
  "version": "1.7.6",
  "model": "gpt-4"
}
```

### 2. Deploy Scripts to Windmill

1. Go to Windmill Dashboard → Scripts → Create Script
2. For each `.ts` file:
   - Create new TypeScript script
   - Copy content from the file
   - Set path as indicated in the table above
   - Save and deploy

### 3. Create HTTP Triggers (Optional)

For cleaner URLs, create HTTP triggers:

```
POST /openai/v1/chat/completions → f/voila/chat_completions
GET  /openai/v1/models          → f/voila/models
```

### 4. Configure CLIProxy

Add to your `config.yaml`:

```yaml
openai-compatibility:
  - name: "voila"
    prefix: "voila"
    base-url: "https://js.chip.com.vn/api/w/chipvn/jobs/run_wait_result/p/f/voila/chat_completions"
    headers:
      Authorization: "Bearer YOUR_WINDMILL_TOKEN"
      Content-Type: "application/json"
    api-key-entries:
      - api-key: "your-cliproxy-api-key"
    models:
      - name: "gpt-4"
        alias: "voila-gpt4"
      - name: "gpt-4o"
        alias: "voila-gpt4o"
      - name: "gpt-4o-mini"
        alias: "voila-gpt4o-mini"
      - name: "gpt-3.5-turbo"
        alias: "voila-gpt35"
      - name: "o1"
        alias: "voila-o1"
      - name: "o1-mini"
        alias: "voila-o1-mini"
```

## API Endpoints

### Chat Completions (Non-streaming)

```bash
curl -X POST "https://js.chip.com.vn/api/w/chipvn/jobs/run_wait_result/p/f/voila/chat_completions" \
  -H "Authorization: Bearer YOUR_WINDMILL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "voila": "$res:u/chipvn/chatgpt",
    "request": {
      "model": "gpt-4",
      "messages": [
        {"role": "user", "content": "Hello!"}
      ],
      "stream": false
    }
  }'
```

### Chat Completions (Streaming)

```bash
curl -X POST "https://js.chip.com.vn/api/w/chipvn/jobs/run_wait_result/p/f/voila/chat_completions_stream" \
  -H "Authorization: Bearer YOUR_WINDMILL_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "voila": "$res:u/chipvn/chatgpt",
    "request": {
      "model": "gpt-4",
      "messages": [
        {"role": "user", "content": "Hello!"}
      ],
      "stream": true
    }
  }'
```

### List Models

```bash
curl -X GET "https://js.chip.com.vn/api/w/chipvn/jobs/run_wait_result/p/f/voila/models" \
  -H "Authorization: Bearer YOUR_WINDMILL_TOKEN"
```

## Request/Response Format

### OpenAI Request (Input)

```json
{
  "model": "gpt-4",
  "messages": [
    {"role": "system", "content": "You are a helpful assistant."},
    {"role": "user", "content": "Hello!"}
  ],
  "stream": false,
  "max_tokens": 1000,
  "temperature": 0.7
}
```

### OpenAI Response (Output)

```json
{
  "id": "chatcmpl-abc123",
  "object": "chat.completion",
  "created": 1700000000,
  "model": "gpt-4",
  "choices": [
    {
      "index": 0,
      "message": {
        "role": "assistant",
        "content": "Hello! How can I help you today?"
      },
      "finish_reason": "stop"
    }
  ],
  "usage": {
    "prompt_tokens": 20,
    "completion_tokens": 10,
    "total_tokens": 30
  }
}
```

## Available Models

| Model ID | Description |
|----------|-------------|
| `gpt-4` | GPT-4 base model |
| `gpt-4-turbo` | GPT-4 Turbo |
| `gpt-4o` | GPT-4 Omni |
| `gpt-4o-mini` | GPT-4 Omni Mini |
| `gpt-4.1` | GPT-4.1 |
| `gpt-3.5-turbo` | GPT-3.5 Turbo |
| `o1` | O1 Reasoning model |
| `o1-mini` | O1 Mini |
| `o1-preview` | O1 Preview |
| `o3-mini` | O3 Mini |

## Token Estimation

Since Voila API may not return exact token counts, this wrapper estimates tokens using:
- ~4 characters per token (rough approximation for English/Vietnamese)
- Actual usage may vary

## Troubleshooting

### Common Issues

1. **401 Unauthorized**: Check your Voila `auth_token` is valid
2. **Network timeout**: Voila API may be slow; increase timeout settings
3. **Empty response**: Check Voila response format in logs

### Debug Mode

Enable debug logging in Windmill to see request/response details.

## License

MIT License - See main repository for details.
