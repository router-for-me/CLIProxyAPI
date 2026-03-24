package claude

import "testing"

var benchmarkClaudeToCodexRequest = []byte(`{
  "model": "claude-3-opus",
  "system": [
    {"type": "text", "text": "You are a careful coding assistant."},
    {"type": "text", "text": "Return concise answers."}
  ],
  "tool_choice": {"disable_parallel_tool_use": true},
  "tools": [
    {
      "name": "list_directory",
      "description": "List files",
      "input_schema": {
        "type": "object",
        "properties": {
          "path": {"type": "string"}
        },
        "required": ["path"]
      }
    },
    {
      "name": "read_file",
      "description": "Read file contents",
      "input_schema": {
        "type": "object",
        "properties": {
          "path": {"type": "string"}
        },
        "required": ["path"]
      }
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "Inspect the repository and summarize the issue."}
      ]
    },
    {
      "role": "assistant",
      "content": [
        {
          "type": "tool_use",
          "id": "toolu_123",
          "name": "list_directory",
          "input": {"path": "/workspace"}
        }
      ]
    },
    {
      "role": "user",
      "content": [
        {
          "type": "tool_result",
          "tool_use_id": "toolu_123",
          "content": [
            {"type": "text", "text": "file-a\nfile-b\nfile-c"}
          ]
        }
      ]
    }
  ]
}`)

var benchmarkClaudeToCodexRequestDefault = []byte(`{
  "model": "claude-3-opus",
  "system": [
    {"type": "text", "text": "You are a careful coding assistant."},
    {"type": "text", "text": "Return concise answers."}
  ],
  "tools": [
    {
      "name": "list_directory",
      "description": "List files",
      "input_schema": {
        "type": "object",
        "properties": {
          "path": {"type": "string"}
        },
        "required": ["path"]
      }
    },
    {
      "name": "read_file",
      "description": "Read file contents",
      "input_schema": {
        "type": "object",
        "properties": {
          "path": {"type": "string"}
        },
        "required": ["path"]
      }
    }
  ],
  "messages": [
    {
      "role": "user",
      "content": [
        {"type": "text", "text": "Inspect the repository and summarize the issue."}
      ]
    }
  ]
}`)

func BenchmarkConvertClaudeRequestToCodex(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ConvertClaudeRequestToCodex("gpt-5.4", benchmarkClaudeToCodexRequest, false)
	}
}

func BenchmarkConvertClaudeRequestToCodex_DefaultToolParallelism(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = ConvertClaudeRequestToCodex("gpt-5.4", benchmarkClaudeToCodexRequestDefault, false)
	}
}
