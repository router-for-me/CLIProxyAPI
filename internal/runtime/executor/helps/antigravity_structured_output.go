package helps

import (
	"regexp"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

var claude55Model = regexp.MustCompile(`^claude-(opus|sonnet)-5-5(?:-(high|reasoning))?(?:-[0-9]{8})?(?:\[[^\[\]]+\])?$`)

// PrepareAntigravityClaudeStructuredOutput replaces native output schemas with
// best-effort prompt instructions for Claude 5.5, which rejects forced tool use.
// Call after sensitive-word obfuscation and before schema cleaning or payload rules.
// The boolean tells the request builder not to restore VALIDATED tool mode.
func PrepareAntigravityClaudeStructuredOutput(model string, payload []byte) ([]byte, bool) {
	if len(payload) == 0 {
		return payload, false
	}
	doc := ojson.NewDoc(payload)
	if !PrepareAntigravityClaudeStructuredOutputDoc(model, doc) {
		return payload, false
	}
	return doc.Bytes(), true
}

// PrepareAntigravityClaudeStructuredOutputDoc replaces native output schemas with
// best-effort prompt instructions in place on doc for Claude 5.5.
func PrepareAntigravityClaudeStructuredOutputDoc(model string, doc *ojson.Doc) bool {
	if doc == nil || doc.Len() == 0 {
		return false
	}
	model = strings.ToLower(thinking.ParseSuffix(strings.TrimSpace(model)).ModelName)
	if !claude55Model.MatchString(model) {
		return false
	}

	containers := []string{"request.generationConfig", "request.generation_config"}
	schemaKeys := []string{"responseSchema", "responseJsonSchema", "response_schema", "response_json_schema"}
	var schema ojson.Result
	for _, container := range containers {
		for _, key := range schemaKeys {
			if candidate := doc.Get(container + "." + key); candidate.IsObject() {
				schema = candidate
				break
			}
		}
		if schema.Exists() {
			break
		}
	}
	if !schema.Exists() {
		return false
	}

	// Wrap the untouched schema as a format, not as format settings themselves.
	format := ojson.Parse(`{"type":"json_schema","schema":` + schema.Raw + `}`)
	instruction := common.BuildClaudeStructuredOutputInstruction(format)
	systemPath := "request.systemInstruction"
	if !doc.Get(systemPath).Exists() && doc.Get("request.system_instruction").Exists() {
		systemPath = "request.system_instruction"
	}
	if errSet := doc.Set(systemPath+".parts.-1.text", instruction); errSet != nil {
		return false
	}

	// Remove only native output leaves; other config fields and aliases stay put.
	for _, container := range containers {
		for _, key := range append(schemaKeys, "responseMimeType", "response_mime_type") {
			_ = util.DeleteJSONDocIfExists(doc, container+"."+key)
		}
	}
	for _, container := range []string{"request.toolConfig", "request.tool_config"} {
		for _, key := range []string{"functionCallingConfig", "function_calling_config"} {
			path := container + "." + key
			switch strings.ToUpper(doc.Get(path + ".mode").String()) {
			case "ANY", "VALIDATED":
				_ = doc.Set(path+".mode", "AUTO")
				_ = util.DeleteJSONDocIfExists(doc, path+".allowedFunctionNames")
				_ = util.DeleteJSONDocIfExists(doc, path+".allowed_function_names")
			}
		}
	}
	return true
}
