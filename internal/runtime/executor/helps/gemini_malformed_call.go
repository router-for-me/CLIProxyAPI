package helps

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"regexp"
	"strings"

	geminicommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/gemini/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

type GeminiStreamPrefix struct {
	Reader io.Reader
	Bytes  []byte
	Tool   string
}

var geminiMalformedCallHeader = regexp.MustCompile(`\bcall:([A-Za-z0-9_.:-]{1,129})\s*\{`)

// Multi-call messages must be rejected unless every marker opens a header, and a name may contain "call:".
var geminiMalformedCallMarker = regexp.MustCompile(`\bcall:`)

func ReadGeminiStreamPrefix(body io.Reader, originalRequest, finalRequest []byte, maxBytes int64) (GeminiStreamPrefix, error) {
	limited := &io.LimitedReader{R: body, N: maxBytes}
	reader := bufio.NewReader(limited)
	var prefix []byte
	tool := ""
	result := func(target string) GeminiStreamPrefix {
		return GeminiStreamPrefix{
			Reader: io.MultiReader(bytes.NewReader(prefix), reader, body),
			Bytes:  prefix,
			Tool:   target,
		}
	}
	for {
		line, errRead := reader.ReadBytes('\n')
		prefix = append(prefix, line...)
		if errRead != nil && !errors.Is(errRead, io.EOF) {
			return result(""), errRead
		}
		if limited.N == 0 {
			return result(""), nil
		}
		nextTool, eligible := geminiRecoveryLine(line)
		if !eligible || (nextTool != "" && tool != "" && nextTool != tool) {
			return result(""), nil
		}
		if nextTool != "" {
			if !GeminiToolRecoveryAllowed(originalRequest, nextTool) || !GeminiToolRecoveryAllowed(finalRequest, nextTool) {
				return result(""), nil
			}
			tool = nextTool
		}
		if errors.Is(errRead, io.EOF) || geminiStreamDone(line) {
			return result(tool), nil
		}
	}
}

func geminiStreamDone(line []byte) bool {
	line = bytes.TrimSpace(line)
	return bytes.HasPrefix(line, []byte("data:")) && bytes.Equal(bytes.TrimSpace(line[5:]), []byte("[DONE]"))
}

func geminiRecoveryLine(line []byte) (string, bool) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 || line[0] == ':' || bytes.HasPrefix(line, []byte("event:")) || bytes.HasPrefix(line, []byte("id:")) {
		return "", true
	}
	if geminiStreamDone(line) {
		return "", true
	}
	payload := JSONPayload(line)
	if !gjson.ValidBytes(payload) {
		return "", false
	}
	root := gjson.ParseBytes(payload)
	if wrapped := root.Get("response"); wrapped.IsObject() {
		root = wrapped
	}
	known := root.IsObject()
	root.ForEach(func(key, _ gjson.Result) bool {
		switch key.String() {
		case "candidates", "usageMetadata", "cpaUsageMetadata", "modelVersion", "responseId", "createTime":
		default:
			known = false
		}
		return known
	})
	if !known {
		return "", false
	}
	candidates := root.Get("candidates")
	if !candidates.Exists() {
		return "", true
	}
	items := candidates.Array()
	if !candidates.IsArray() || len(items) > 1 {
		return "", false
	}
	if len(items) == 0 {
		return "", true
	}
	candidate := items[0]
	if geminiRecoveryContent(candidate.Get("content")) {
		return "", false
	}
	reason := candidate.Get("finishReason").String()
	if !geminicommon.IsTerminalFinishReason(reason) {
		return "", true
	}
	if reason != "MALFORMED_FUNCTION_CALL" {
		return "", false
	}
	tool := geminiMalformedToolName(candidate.Get("finishMessage").String())
	return tool, tool != ""
}

func geminiRecoveryContent(content gjson.Result) bool {
	if !content.Exists() {
		return false
	}
	if !content.IsObject() {
		return true
	}
	hasContent := false
	content.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "role":
		case "parts":
			hasContent = geminiRecoveryParts(value)
		default:
			hasContent = true
		}
		return !hasContent
	})
	return hasContent
}

func geminiRecoveryParts(parts gjson.Result) bool {
	if !parts.IsArray() {
		return true
	}
	for _, part := range parts.Array() {
		if geminiRecoveryPart(part) {
			return true
		}
	}
	return false
}

func geminiRecoveryPart(part gjson.Result) bool {
	if !part.IsObject() {
		return true
	}
	hasContent := false
	part.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "text", "thoughtSignature":
			hasContent = value.Type != gjson.String || value.String() != ""
		case "thought":
			hasContent = value.Type != gjson.True && value.Type != gjson.False
		default:
			hasContent = true
		}
		return !hasContent
	})
	return hasContent
}

func geminiMalformedToolName(message string) string {
	message = strings.TrimPrefix(message, "Malformed function call:")
	headers := geminiMalformedCallHeader.FindAllStringSubmatchIndex(message, -1)
	markers := geminiMalformedCallMarker.FindAllStringIndex(message, -1)
	if len(headers) == 0 || len(headers) != len(markers) {
		return ""
	}
	name := ""
	for index, header := range headers {
		if header[0] != markers[index][0] {
			return ""
		}
		// Function names may contain ':' of their own, so only the namespace prefix is stripped.
		declared, ok := strings.CutPrefix(message[header[2]:header[3]], "default_api:")
		if !ok || declared == "" {
			return ""
		}
		if name != "" && name != declared {
			return ""
		}
		name = declared
	}
	return name
}

func GeminiToolRecoveryAllowed(body []byte, name string) bool {
	root := gjson.ParseBytes(body)
	if !geminiDeclaresTool(root, name) {
		return false
	}
	selection := root.Get("toolConfig.functionCallingConfig")
	mode := selection.Get("mode").String()
	switch mode {
	case "", "AUTO", "VALIDATED", "ANY":
	default:
		return false
	}
	allowed := selection.Get("allowedFunctionNames")
	if allowed.Exists() && !allowed.IsArray() {
		return false
	}
	names := allowed.Array()
	if len(names) == 0 {
		return mode != "ANY" || geminiToolCount(root) > 1
	}
	found := false
	for _, item := range names {
		found = found || item.String() == name
	}
	return found && (mode != "ANY" || len(names) > 1)
}

func ConstrainGeminiRecoveryTool(body []byte, name string) ([]byte, error) {
	body, err := sjson.SetBytes(bytes.Clone(body), "toolConfig.functionCallingConfig.mode", "ANY")
	if err != nil {
		return nil, err
	}
	return sjson.SetBytes(body, "toolConfig.functionCallingConfig.allowedFunctionNames", []string{name})
}

func GeminiRecoveryToolConfigured(body []byte, name string) bool {
	root := gjson.ParseBytes(body)
	selection := root.Get("toolConfig.functionCallingConfig")
	names := selection.Get("allowedFunctionNames").Array()
	return selection.Get("mode").String() == "ANY" && len(names) == 1 && names[0].String() == name && geminiDeclaresTool(root, name)
}

func GeminiRecoveryBodyPreserved(first, retry []byte) (bool, error) {
	firstPayload, err := geminiPayloadWithoutToolChoice(first)
	if err != nil {
		return false, err
	}
	retryPayload, err := geminiPayloadWithoutToolChoice(retry)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(firstPayload, retryPayload), nil
}

func geminiPayloadWithoutToolChoice(body []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var payload map[string]any
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	if tools, ok := payload["toolConfig"].(map[string]any); ok {
		if selection, okSelection := tools["functionCallingConfig"].(map[string]any); okSelection {
			delete(selection, "mode")
			delete(selection, "allowedFunctionNames")
			if len(selection) == 0 {
				delete(tools, "functionCallingConfig")
			}
		}
		if len(tools) == 0 {
			delete(payload, "toolConfig")
		}
	}
	return payload, nil
}

func geminiDeclaresTool(root gjson.Result, name string) bool {
	for _, tool := range root.Get("tools").Array() {
		for _, declaration := range tool.Get("functionDeclarations").Array() {
			if declaration.Get("name").String() == name {
				return true
			}
		}
	}
	return false
}

func geminiToolCount(root gjson.Result) int {
	count := 0
	for _, tool := range root.Get("tools").Array() {
		count += len(tool.Get("functionDeclarations").Array())
	}
	return count
}
