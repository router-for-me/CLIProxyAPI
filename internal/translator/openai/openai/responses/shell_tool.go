package responses

import (
	"errors"
	"math"
	"strconv"
	"strings"

	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var errInvalidShellAction = errors.New("invalid shell action: expected nonempty commands strings and optional positive integer limits")

func convertResponsesShellToolToOpenAIChat(_ gjson.Result, name string) ([]byte, bool) {
	tool := []byte(`{"type":"function","function":{"name":"","description":"Request commands to execute in the client-provided local shell environment. Each commands entry is a complete shell command, not an argv element.","parameters":{"type":"object","properties":{"commands":{"type":"array","items":{"type":"string"},"minItems":1},"timeout_ms":{"type":"integer","minimum":1},"max_output_length":{"type":"integer","minimum":1}},"required":["commands"],"additionalProperties":false}}}`)
	tool, _ = sjson.SetBytes(tool, "function.name", name)
	return tool, true
}

func (idx *responsesToolIndex) isShell(name string) bool {
	return idx.byChat[name].shell
}

func (idx *responsesToolIndex) shellName() string {
	for _, d := range idx.declarations {
		if d.shell {
			return d.chatName
		}
	}
	return ""
}

// Normalize shell history before the existing call grouping and output pairing.
// Keep the entire output envelope as JSON text, including truncation metadata.
func (idx *responsesToolIndex) shellHistory(items []gjson.Result) []gjson.Result {
	name := idx.shellName()
	if name == "" {
		// Historical calls remain meaningful after the client withdraws the tool.
		// Reserve a replay-only name without adding an available declaration.
		reserved := make(map[string]bool)
		for key := range idx.byChat {
			reserved[key] = true
		}
		for key := range idx.byRaw {
			reserved[key] = true
		}
		for key := range idx.byLocal {
			reserved[key] = true
		}
		for _, item := range items {
			if kind := item.Get("type").String(); kind == "function_call" || kind == "custom_tool_call" {
				reserved[idx.canonicalName(item.Get("name").String())] = true
			}
		}
		name = "__cpa_local_shell"
		for suffix := 1; reserved[name]; suffix++ {
			name = "__cpa_local_shell_" + strconv.Itoa(suffix)
		}
	}
	for i, item := range items {
		raw := []byte(item.Raw)
		switch item.Get("type").String() {
		case "shell_call":
			if environment := item.Get("environment.type"); environment.Exists() && environment.String() != "local" {
				continue
			}
			raw, _ = sjson.SetBytes(raw, "type", "function_call")
			raw, _ = sjson.SetBytes(raw, "name", name)
			raw, _ = sjson.SetBytes(raw, "arguments", item.Get("action").Raw)
		case "shell_call_output":
			raw, _ = sjson.SetBytes(raw, "type", "function_call_output")
			raw, _ = sjson.SetBytes(raw, "output", item.Raw)
		default:
			continue
		}
		items[i] = gjson.ParseBytes(raw)
	}
	return items
}

func shellCallItem(callID, arguments, status string) ([]byte, error) {
	action := gjson.Parse(arguments)
	if !gjson.Valid(arguments) || !action.IsObject() {
		return nil, errInvalidShellAction
	}
	commands := action.Get("commands")
	if !commands.IsArray() || len(commands.Array()) == 0 {
		return nil, errInvalidShellAction
	}
	for _, command := range commands.Array() {
		if command.Type != gjson.String || strings.TrimSpace(command.String()) == "" {
			return nil, errInvalidShellAction
		}
	}
	valid := true
	action.ForEach(func(key, value gjson.Result) bool {
		switch key.String() {
		case "commands":
		case "timeout_ms", "max_output_length":
			if value.Type != gjson.Null && (value.Type != gjson.Number || value.Float() <= 0 || math.Trunc(value.Float()) != value.Float()) {
				valid = false
			}
		default:
			valid = false
		}
		return valid
	})
	if !valid {
		return nil, errInvalidShellAction
	}
	item := shellCallPlaceholder(callID)
	item, _ = sjson.SetBytes(item, "status", status)
	item, _ = sjson.SetRawBytes(item, "action", []byte(arguments))
	return item, nil
}

func shellCallPlaceholder(callID string) []byte {
	item := []byte(`{"id":"","type":"shell_call","status":"in_progress","call_id":"","action":{"commands":[]}}`)
	item, _ = sjson.SetBytes(item, "id", "sh_"+callID)
	item, _ = sjson.SetBytes(item, "call_id", callID)
	return item
}

func responsesToolInputFailure(responseID string, sequence int, err error) []byte {
	failure := translatorcommon.ApplyPatchFailure(responseID, sequence)
	if errors.Is(err, errInvalidShellAction) {
		failure, _ = sjson.SetBytes(failure, "response.error.message", errInvalidShellAction.Error())
	}
	return failure
}
