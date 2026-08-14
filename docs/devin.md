# Devin (Codeium Cascade) guide

CLIProxyAPI can use Devin's Codeium Cascade backend as a provider, exposing Devin's free models through the standard OpenAI chat completions API.

It supports:

- streaming and non-streaming chat completions
- system prompts
- multi-turn conversations
- tool calling (function calling) with multi-turn tool results
- reasoning/thinking tokens (exposed as `reasoning` in the response)

## Available models

| Model ID | Description |
|----------|-------------|
| `glm-5-2` | GLM-5.2 High (default) |
| `glm-5-2-1m` | GLM-5.2 High with 1M token context |
| `glm-5-2-max` | GLM-5.2 Max |
| `swe-1-7` | SWE-1.7 |
| `swe-1-7-medium` | SWE-1.7 Medium |
| `swe-1-6` | SWE-1.6 |

## Auth configuration

The Devin provider uses the same session token as the Devin CLI. The token is found in `~/.local/share/devin/credentials.toml` under the `session_token` field.

Create a JSON file in the auth directory (e.g., `auths/devin.json`):

```json
{
  "type": "devin",
  "devin_session_token": "devin-session-token$eyJhbGci..."
}
```

## Usage

### Basic chat completion

```bash
curl http://127.0.0.1:18499/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": false
  }'
```

### Streaming

```bash
curl http://127.0.0.1:18499/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

### System prompt

```bash
curl http://127.0.0.1:18499/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [
      {"role": "system", "content": "You are a helpful assistant that always replies in one sentence."},
      {"role": "user", "content": "Tell me about Go."}
    ]
  }'
```

### Tool calling

The Devin provider supports OpenAI-compatible tool calling. Tool definitions are translated to Devin's `ChatToolDefinition` protobuf format, and tool call deltas in the response are converted to OpenAI's streaming tool call format.

```bash
curl http://127.0.0.1:18499/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [
      {"role": "user", "content": "What is the weather in San Francisco?"}
    ],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get the current weather in a given location",
        "parameters": {
          "type": "object",
          "properties": {
            "location": {"type": "string", "description": "The city and state, e.g. San Francisco, CA"}
          },
          "required": ["location"]
        }
      }
    }]
  }'
```

Response:

```json
{
  "id": "chatcmpl-bot-...",
  "object": "chat.completion",
  "model": "glm-5-2",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": null,
      "tool_calls": [{
        "id": "chatcmpl-tool-...",
        "type": "function",
        "function": {
          "name": "get_weather",
          "arguments": "{\"location\": \"San Francisco, CA\"}"
        }
      }]
    },
    "finish_reason": "stop"
  }]
}
```

### Multi-turn with tool results

After receiving a tool call, execute the function and feed the result back as a `tool` role message:

```bash
curl http://127.0.0.1:18499/v1/chat/completions \
  -H "Authorization: Bearer YOUR_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "glm-5-2",
    "messages": [
      {"role": "user", "content": "What is the weather in San Francisco?"},
      {"role": "assistant", "content": null, "tool_calls": [{"id": "call_abc123", "type": "function", "function": {"name": "get_weather", "arguments": "{\"location\": \"San Francisco, CA\"}"}}]},
      {"role": "tool", "tool_call_id": "call_abc123", "content": "The weather in San Francisco, CA is 62 degrees and foggy."}
    ],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get the current weather in a given location",
        "parameters": {"type": "object", "properties": {"location": {"type": "string"}}, "required": ["location"]}
      }
    }]
  }'
```

Response:

```json
{
  "id": "chatcmpl-bot-...",
  "object": "chat.completion",
  "model": "glm-5-2",
  "choices": [{
    "index": 0,
    "message": {
      "role": "assistant",
      "content": "The current weather in San Francisco is **62°F** and **foggy**. Classic San Francisco! 🌁"
    },
    "finish_reason": "stop"
  }]
}
```

## How it works

The Devin executor translates OpenAI chat completion requests into Devin's Connect-RPC protobuf format (`GetChatMessageRequest`) and sends them to `server.codeium.com`. Streaming responses are parsed from Connect frames (`GetChatMessageResponse`) and converted to OpenAI SSE chunks.

| OpenAI field | Devin protobuf field |
|--------------|---------------------|
| `messages[system]` | `prompt` (field 2) |
| `messages[user/assistant/tool]` | `chat_message_prompts` (field 3, repeated) |
| `tools` | `tools` (field 10, repeated) |
| `temperature` | `configuration.temperature` (field 8.5) |
| `max_tokens` | `configuration.max_tokens` (field 8.2) |
| `top_p` | `configuration.top_p` (field 8.8) |
| response `content` | `delta_text` (field 3) |
| response `reasoning` | `delta_thinking` (field 9) |
| response `tool_calls` | `delta_tool_calls` (field 6, repeated) |
| response `finish_reason` | `stop_reason` (field 5) |
| response `usage` | `usage` (field 7) |
