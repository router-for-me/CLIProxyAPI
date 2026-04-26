package executor

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

type codexStreamFieldMode uint8

const (
	codexStreamFieldKeep codexStreamFieldMode = iota
	codexStreamFieldTrue
	codexStreamFieldFalse
	codexStreamFieldDelete
)

type codexFinalUpstreamRequestKind uint8

const (
	codexFinalUpstreamResponses codexFinalUpstreamRequestKind = iota
	codexFinalUpstreamCompact
)

type codexFinalUpstreamBodyOptions struct {
	requestKind                codexFinalUpstreamRequestKind
	streamMode                 codexStreamFieldMode
	preservePreviousResponseID bool
}

func codexFinalUpstreamRequestKindForURL(rawURL string) codexFinalUpstreamRequestKind {
	trimmed := strings.TrimSpace(rawURL)
	path := trimmed
	if parsed, err := url.Parse(trimmed); err == nil && strings.TrimSpace(parsed.Path) != "" {
		path = parsed.Path
	}
	path = strings.TrimSuffix(strings.TrimSpace(path), "/")
	if strings.HasSuffix(path, "/responses/compact") {
		return codexFinalUpstreamCompact
	}
	return codexFinalUpstreamResponses
}

var codexAllowedResponsesFinalUpstreamFields = map[string]struct{}{
	"model":               {},
	"instructions":        {},
	"input":               {},
	"tools":               {},
	"tool_choice":         {},
	"parallel_tool_calls": {},
	"reasoning":           {},
	"store":               {},
	"stream":              {},
	"include":             {},
	"service_tier":        {},
	"prompt_cache_key":    {},
	"text":                {},
	"client_metadata":     {},
}

var codexAllowedCompactFinalUpstreamFields = map[string]struct{}{
	"model":               {},
	"instructions":        {},
	"input":               {},
	"tools":               {},
	"parallel_tool_calls": {},
	"reasoning":           {},
	"text":                {},
}

func codexEnsureFinalUpstreamBodyDefaults(body []byte, opts codexFinalUpstreamBodyOptions) []byte {
	edits := make([]helps.JSONEdit, 0, 4)
	switch opts.requestKind {
	case codexFinalUpstreamCompact:
		if tools := gjson.GetBytes(body, "tools"); !tools.Exists() || tools.Type == gjson.Null {
			edits = append(edits, helps.SetRawJSONEdit("tools", []byte("[]")))
		}
		if parallel := gjson.GetBytes(body, "parallel_tool_calls"); !parallel.Exists() || parallel.Type == gjson.Null {
			edits = append(edits, helps.SetJSONEdit("parallel_tool_calls", true))
		}
	default:
		if tools := gjson.GetBytes(body, "tools"); !tools.Exists() || tools.Type == gjson.Null {
			edits = append(edits, helps.SetRawJSONEdit("tools", []byte("[]")))
		}
		if toolChoice := gjson.GetBytes(body, "tool_choice"); !toolChoice.Exists() || toolChoice.Type == gjson.Null {
			edits = append(edits, helps.SetJSONEdit("tool_choice", "auto"))
		}
		if parallel := gjson.GetBytes(body, "parallel_tool_calls"); !parallel.Exists() || parallel.Type == gjson.Null {
			edits = append(edits, helps.SetJSONEdit("parallel_tool_calls", true))
		}
		if include := gjson.GetBytes(body, "include"); !include.Exists() || include.Type == gjson.Null {
			edits = append(edits, helps.SetRawJSONEdit("include", []byte("[]")))
		}
	}
	if len(edits) == 0 {
		return body
	}
	return helps.EditJSONBytes(body, edits...)
}

func pruneCodexFinalUpstreamBody(body []byte, opts codexFinalUpstreamBodyOptions) []byte {
	root := gjson.ParseBytes(body)
	if !root.IsObject() {
		return body
	}

	allowedFields := codexAllowedResponsesFinalUpstreamFields
	if opts.requestKind == codexFinalUpstreamCompact {
		allowedFields = codexAllowedCompactFinalUpstreamFields
	}

	edits := make([]helps.JSONEdit, 0, 8)
	root.ForEach(func(key, _ gjson.Result) bool {
		field := strings.TrimSpace(key.String())
		if field == "" {
			return true
		}
		if field == "previous_response_id" && opts.preservePreviousResponseID {
			return true
		}
		if _, ok := allowedFields[field]; ok {
			return true
		}
		edits = append(edits, helps.DeleteJSONEdit(field))
		return true
	})

	instructions := gjson.GetBytes(body, "instructions")
	if !instructions.Exists() || instructions.Type == gjson.Null || (instructions.Type == gjson.String && instructions.String() == "") {
		edits = append(edits, helps.DeleteJSONEdit("instructions"))
	}
	if len(edits) == 0 {
		return body
	}
	return helps.EditJSONBytes(body, edits...)
}

func normalizeCodexFinalUpstreamBodyUncached(body []byte, baseModel string, auth *cliproxyauth.Auth, opts codexFinalUpstreamBodyOptions) []byte {
	if len(bytes.TrimSpace(body)) == 0 {
		return body
	}

	body = codexEnsureFinalUpstreamBodyDefaults(body, opts)

	edits := []helps.JSONEdit{
		helps.SetJSONEdit("model", baseModel),
		helps.DeleteJSONEdit("prompt_cache_retention"),
		helps.DeleteJSONEdit("safety_identifier"),
		helps.DeleteJSONEdit("stream_options"),
		helps.DeleteJSONEdit("max_output_tokens"),
		helps.DeleteJSONEdit("max_completion_tokens"),
		helps.DeleteJSONEdit("temperature"),
		helps.DeleteJSONEdit("top_p"),
		helps.DeleteJSONEdit("truncation"),
		helps.DeleteJSONEdit("user"),
		helps.DeleteJSONEdit("context_management"),
	}
	if opts.requestKind == codexFinalUpstreamResponses {
		edits = append(edits, helps.SetJSONEdit("store", false))
	}
	if !opts.preservePreviousResponseID {
		edits = append(edits, helps.DeleteJSONEdit("previous_response_id"))
	}
	switch opts.streamMode {
	case codexStreamFieldTrue:
		edits = append(edits, helps.SetJSONEdit("stream", true))
	case codexStreamFieldFalse:
		edits = append(edits, helps.SetJSONEdit("stream", false))
	case codexStreamFieldDelete:
		edits = append(edits, helps.DeleteJSONEdit("stream"))
	}

	body = helps.EditJSONBytes(body, edits...)
	body = pruneCodexFinalUpstreamBody(body, opts)
	return body
}
