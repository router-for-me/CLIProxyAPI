package responses

import (
	"bytes"
	"context"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ConvertCodexResponseToOpenAIResponses converts OpenAI Chat Completions streaming chunks
// to OpenAI Responses SSE events (response.*).

type ConvertCodexResponseToOpenAIResponsesParams struct {
	CurrentToolName        string
	SuppressArgumentDeltas bool
	SuppressedArguments    string
	CursorWorkspaceRoot    string
}

func ConvertCodexResponseToOpenAIResponses(_ context.Context, _ string, originalRequestRawJSON, _, rawJSON []byte, param *any) [][]byte {
	if param == nil {
		local := any(nil)
		param = &local
	}
	if *param == nil {
		*param = &ConvertCodexResponseToOpenAIResponsesParams{
			CursorWorkspaceRoot: util.ExtractCursorWorkspaceRoot(originalRequestRawJSON),
		}
	}
	if root := util.ExtractCursorWorkspaceRoot(originalRequestRawJSON); root != "" {
		(*param).(*ConvertCodexResponseToOpenAIResponsesParams).CursorWorkspaceRoot = root
	}

	prefix := []byte{}
	if bytes.HasPrefix(rawJSON, []byte("data:")) {
		prefix = []byte("data: ")
		rawJSON = bytes.TrimSpace(rawJSON[5:])
		rawJSON = normalizeCursorToolArgumentsInResponsesEvent(rawJSON, param)
		if len(rawJSON) == 0 {
			return [][]byte{}
		}
		traceOpenAIResponsesResponse(rawJSON)
		out := make([]byte, 0, len(rawJSON)+len(prefix))
		out = append(out, prefix...)
		out = append(out, rawJSON...)
		return [][]byte{out}
	}
	rawJSON = normalizeCursorToolArgumentsInResponsesEvent(rawJSON, param)
	if len(rawJSON) == 0 {
		return [][]byte{}
	}
	traceOpenAIResponsesResponse(rawJSON)
	return [][]byte{rawJSON}
}

// ConvertCodexResponseToOpenAIResponsesNonStream builds a single Responses JSON
// from a non-streaming OpenAI Chat Completions response.
func ConvertCodexResponseToOpenAIResponsesNonStream(_ context.Context, _ string, _, _, rawJSON []byte, _ *any) []byte {
	rootResult := gjson.ParseBytes(rawJSON)
	// Verify this is a response.completed event
	if rootResult.Get("type").String() != "response.completed" {
		return []byte{}
	}
	traceOpenAIResponsesResponse(rawJSON)
	responseResult := rootResult.Get("response")
	return []byte(responseResult.Raw)
}

func normalizeCursorToolArgumentsInResponsesEvent(rawJSON []byte, param *any) []byte {
	if param == nil || *param == nil {
		return rawJSON
	}
	state, ok := (*param).(*ConvertCodexResponseToOpenAIResponsesParams)
	if !ok {
		return rawJSON
	}

	rootResult := gjson.ParseBytes(rawJSON)
	switch rootResult.Get("type").String() {
	case "response.output_item.added":
		item := rootResult.Get("item")
		if !item.Exists() || !isCodexResponsesToolCall(item) {
			return rawJSON
		}
		state.CurrentToolName = item.Get("name").String()
		state.SuppressArgumentDeltas = util.ShouldNormalizeCursorToolArguments(state.CurrentToolName)
		state.SuppressedArguments = ""
		return rawJSON
	case "response.function_call_arguments.delta", "response.custom_tool_call_input.delta":
		if !state.SuppressArgumentDeltas {
			return rawJSON
		}
		state.SuppressedArguments += rootResult.Get("delta").String()
		return []byte{}
	case "response.function_call_arguments.done":
		return normalizeCursorResponsesArgumentsDone(rawJSON, state, "arguments")
	case "response.custom_tool_call_input.done":
		return normalizeCursorResponsesArgumentsDone(rawJSON, state, "input")
	case "response.output_item.done":
		item := rootResult.Get("item")
		if !item.Exists() || !isCodexResponsesToolCall(item) {
			return rawJSON
		}
		name := item.Get("name").String()
		if name == "" {
			name = state.CurrentToolName
		}
		if !util.ShouldNormalizeCursorToolArguments(name) {
			state.SuppressArgumentDeltas = false
			state.SuppressedArguments = ""
			return rawJSON
		}
		argPath := "item.arguments"
		args := item.Get("arguments").String()
		if item.Get("type").String() == "custom_tool_call" {
			argPath = "item.input"
			args = item.Get("input").String()
		}
		if args == "" {
			args = state.SuppressedArguments
		}
		args = util.NormalizeCursorToolArguments(name, args, state.CursorWorkspaceRoot)
		state.SuppressArgumentDeltas = false
		state.SuppressedArguments = ""
		if args == "" {
			return rawJSON
		}
		updated, err := sjson.SetBytes(rawJSON, argPath, args)
		if err != nil {
			return rawJSON
		}
		return updated
	default:
		return rawJSON
	}
}

func normalizeCursorResponsesArgumentsDone(rawJSON []byte, state *ConvertCodexResponseToOpenAIResponsesParams, path string) []byte {
	if !state.SuppressArgumentDeltas {
		return rawJSON
	}
	args := gjson.GetBytes(rawJSON, path).String()
	if args == "" {
		args = state.SuppressedArguments
	}
	args = util.NormalizeCursorToolArguments(state.CurrentToolName, args, state.CursorWorkspaceRoot)
	if args == "" {
		return []byte{}
	}
	updated, err := sjson.SetBytes(rawJSON, path, args)
	if err != nil {
		return rawJSON
	}
	return updated
}

func isCodexResponsesToolCall(item gjson.Result) bool {
	switch item.Get("type").String() {
	case "function_call", "custom_tool_call":
		return true
	default:
		return false
	}
}
