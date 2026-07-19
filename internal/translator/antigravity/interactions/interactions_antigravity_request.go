package interactions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertInteractionsRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	functionNameMap := util.SanitizedFunctionNameMap(inputRawJSON)
	out := []byte(`{"project":"","request":{"contents":[]},"model":""}`)
	out, _ = sjson.SetBytes(out, "model", modelName)
	if stream || root.Get("stream").Bool() {
		out, _ = sjson.SetBytes(out, "request.stream", true)
	}
	out = copyInteractionsSystemToAntigravity(out, root)
	out = copyInteractionsGenerationConfigToAntigravity(out, root)
	contentItems := translatorcommon.NewRawArrayItems(root.Get("input.#").Int())
	appendInteractionsInputToAntigravity(&contentItems, root.Get("input"))
	out = translatorcommon.SetRawArrayItems(out, "request.contents", contentItems)
	out = copyInteractionsToolsToAntigravity(out, root, functionNameMap)
	out = rewriteInteractionsFunctionNames(out, functionNameMap)
	out = attachDefaultAntigravitySafetySettings(out)
	return out
}

func rewriteInteractionsFunctionNames(out []byte, functionNameMap map[string]string) []byte {
	contents := gjson.GetBytes(out, "request.contents")
	canBatchContents := contents.IsArray()
	if canBatchContents {
		contents.ForEach(func(_, content gjson.Result) bool {
			parts := content.Get("parts")
			if parts.Exists() && !parts.IsArray() {
				canBatchContents = false
				return false
			}
			return true
		})
	}
	if canBatchContents {
		contentsChanged := false
		contentItems := translatorcommon.NewRawArrayItems(contents.Get("#").Int())
		contents.ForEach(func(_, content gjson.Result) bool {
			contentJSON := []byte(content.Raw)
			partsChanged := false
			partItems := make([][]byte, 0, 4)
			content.Get("parts").ForEach(func(_, part gjson.Result) bool {
				partJSON := []byte(part.Raw)
				for _, field := range []string{"functionCall", "functionResponse"} {
					name := part.Get(field + ".name").String()
					if name == "" {
						continue
					}
					partJSON, _ = sjson.SetBytes(partJSON, field+".name", util.MapSanitizedFunctionName(functionNameMap, name))
					partsChanged = true
				}
				partItems = append(partItems, partJSON)
				return true
			})
			if partsChanged {
				contentJSON, _ = sjson.SetRawBytes(contentJSON, "parts", translatorcommon.JoinRawArray(partItems))
				contentsChanged = true
			}
			contentItems = append(contentItems, contentJSON)
			return true
		})
		if contentsChanged {
			out, _ = sjson.SetRawBytes(out, "request.contents", translatorcommon.JoinRawArray(contentItems))
		}
	} else {
		for contentIndex, content := range contents.Array() {
			for partIndex, part := range content.Get("parts").Array() {
				for _, field := range []string{"functionCall", "functionResponse"} {
					name := part.Get(field + ".name").String()
					if name == "" {
						continue
					}
					path := fmt.Sprintf("request.contents.%d.parts.%d.%s.name", contentIndex, partIndex, field)
					out, _ = sjson.SetBytes(out, path, util.MapSanitizedFunctionName(functionNameMap, name))
				}
			}
		}
	}

	allowedPath := "request.toolConfig.functionCallingConfig.allowedFunctionNames"
	allowedNames := gjson.GetBytes(out, allowedPath)
	if allowedNames.IsArray() {
		nameItems := make([][]byte, 0, 4)
		allowedNames.ForEach(func(_, name gjson.Result) bool {
			mappedName, _ := json.Marshal(util.MapSanitizedFunctionName(functionNameMap, name.String()))
			nameItems = append(nameItems, mappedName)
			return true
		})
		out, _ = sjson.SetRawBytes(out, allowedPath, translatorcommon.JoinRawArray(nameItems))
	} else {
		for index, name := range allowedNames.Array() {
			path := fmt.Sprintf("%s.%d", allowedPath, index)
			out, _ = sjson.SetBytes(out, path, util.MapSanitizedFunctionName(functionNameMap, name.String()))
		}
	}
	return out
}

func copyInteractionsSystemToAntigravity(out []byte, root gjson.Result) []byte {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		return out
	}
	if sys.Type == gjson.String {
		instr := []byte(`{"parts":[{"text":""}]}`)
		instr, _ = sjson.SetBytes(instr, "parts.0.text", sys.String())
		out, _ = sjson.SetRawBytes(out, "request.systemInstruction", instr)
		return out
	}
	if text := sys.Get("text"); text.Exists() && !sys.Get("parts").Exists() {
		instr := []byte(`{"parts":[{"text":""}]}`)
		instr, _ = sjson.SetBytes(instr, "parts.0.text", text.String())
		out, _ = sjson.SetRawBytes(out, "request.systemInstruction", instr)
		return out
	}
	out, _ = sjson.SetRawBytes(out, "request.systemInstruction", []byte(sys.Raw))
	return out
}

func copyInteractionsGenerationConfigToAntigravity(out []byte, root gjson.Result) []byte {
	if cfg := root.Get("generation_config"); cfg.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig", convertSnakeCaseKeysToCamelCaseForAntigravity([]byte(cfg.Raw)))
	} else if cfg := root.Get("generationConfig"); cfg.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig", []byte(cfg.Raw))
	}
	out = normalizeInteractionsGenerationConfigForAntigravity(out)
	out = copyInteractionsReasoningToAntigravity(out, root)
	out = copyInteractionsResponseModalitiesToAntigravity(out, root)
	out = copyInteractionsToolChoiceToAntigravity(out, root)
	return out
}

func normalizeInteractionsGenerationConfigForAntigravity(out []byte) []byte {
	if thinkingLevel := gjson.GetBytes(out, "request.generationConfig.thinkingLevel"); thinkingLevel.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", []byte(thinkingLevel.Raw))
		out, _ = sjson.DeleteBytes(out, "request.generationConfig.thinkingLevel")
	}
	if thinkingBudget := gjson.GetBytes(out, "request.generationConfig.thinkingBudget"); thinkingBudget.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget", []byte(thinkingBudget.Raw))
		out, _ = sjson.DeleteBytes(out, "request.generationConfig.thinkingBudget")
	}
	if includeThoughts := gjson.GetBytes(out, "request.generationConfig.includeThoughts"); includeThoughts.Exists() {
		out, _ = sjson.SetRawBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", []byte(includeThoughts.Raw))
		out, _ = sjson.DeleteBytes(out, "request.generationConfig.includeThoughts")
	}
	if summaries := gjson.GetBytes(out, "request.generationConfig.thinkingSummaries"); summaries.Exists() {
		if includeThoughts, ok := antigravityThinkingSummariesIncludeThoughts(summaries); ok {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		}
		out, _ = sjson.DeleteBytes(out, "request.generationConfig.thinkingSummaries")
	}
	if toolChoice := gjson.GetBytes(out, "request.generationConfig.toolChoice"); toolChoice.Exists() {
		out, _ = sjson.DeleteBytes(out, "request.generationConfig.toolChoice")
	}
	return out
}

func copyInteractionsReasoningToAntigravity(out []byte, root gjson.Result) []byte {
	reasoning := root.Get("reasoning")
	if !reasoning.Exists() {
		return out
	}
	effort := strings.ToLower(strings.TrimSpace(reasoning.Get("effort").String()))
	if effort == "" {
		effort = strings.ToLower(strings.TrimSpace(reasoning.Get("thinking_level").String()))
	}
	if effort != "" {
		if effort == "auto" {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", true)
		} else {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.thinkingLevel", effort)
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", effort != "none")
		}
	}
	if summary := reasoning.Get("summary"); summary.Exists() {
		if includeThoughts, ok := antigravityThinkingSummariesIncludeThoughts(summary); ok {
			out, _ = sjson.SetBytes(out, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		}
	}
	return out
}

func copyInteractionsResponseModalitiesToAntigravity(out []byte, root gjson.Result) []byte {
	mods := root.Get("response_modalities")
	if !mods.Exists() {
		mods = root.Get("responseModalities")
	}
	if !mods.Exists() || !mods.IsArray() {
		return out
	}
	var responseMods []string
	mods.ForEach(func(_, mod gjson.Result) bool {
		switch strings.ToLower(strings.TrimSpace(mod.String())) {
		case "text":
			responseMods = append(responseMods, "TEXT")
		case "image":
			responseMods = append(responseMods, "IMAGE")
		case "audio":
			responseMods = append(responseMods, "AUDIO")
		}
		return true
	})
	if len(responseMods) > 0 {
		out, _ = sjson.SetBytes(out, "request.generationConfig.responseModalities", responseMods)
	}
	return out
}

func copyInteractionsToolChoiceToAntigravity(out []byte, root gjson.Result) []byte {
	toolChoice := root.Get("tool_choice")
	if !toolChoice.Exists() {
		toolChoice = root.Get("generation_config.tool_choice")
	}
	if !toolChoice.Exists() {
		toolChoice = root.Get("generationConfig.toolChoice")
	}
	if !toolChoice.Exists() {
		return out
	}
	mode := ""
	var allowedNames []string
	if toolChoice.Type == gjson.String {
		switch strings.ToLower(strings.TrimSpace(toolChoice.String())) {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		}
	} else if toolChoice.IsObject() {
		switch strings.ToLower(strings.TrimSpace(toolChoice.Get("type").String())) {
		case "none":
			mode = "NONE"
		case "auto":
			mode = "AUTO"
		case "required", "any":
			mode = "ANY"
		case "function":
			mode = "ANY"
			if name := toolChoice.Get("function.name").String(); strings.TrimSpace(name) != "" {
				allowedNames = append(allowedNames, name)
			}
		case "tool":
			mode = "ANY"
			if name := toolChoice.Get("name").String(); strings.TrimSpace(name) != "" {
				allowedNames = append(allowedNames, name)
			}
		}
	}
	if mode == "" {
		return out
	}
	out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.mode", mode)
	if len(allowedNames) > 0 {
		out, _ = sjson.SetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames", allowedNames)
	}
	return out
}

func appendInteractionsInputToAntigravity(items *[][]byte, input gjson.Result) {
	if !input.Exists() {
		return
	}
	if input.Type == gjson.String {
		appendAntigravityTextContent(items, "user", input.String())
		return
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			appendInteractionsStepToAntigravity(items, item, "user")
			return true
		})
		return
	}
	if steps := input.Get("steps"); steps.Exists() && steps.IsArray() {
		defaultRole := "user"
		if role := input.Get("role").String(); role == "model" || role == "assistant" {
			defaultRole = "model"
		}
		steps.ForEach(func(_, step gjson.Result) bool {
			appendInteractionsStepToAntigravity(items, step, defaultRole)
			return true
		})
		return
	}
	appendInteractionsStepToAntigravity(items, input, "user")
}

func appendInteractionsStepToAntigravity(items *[][]byte, step gjson.Result, defaultRole string) {
	if step.Type == gjson.String {
		appendAntigravityTextContent(items, defaultRole, step.String())
		return
	}
	if steps := step.Get("steps"); steps.Exists() && steps.IsArray() {
		role := defaultRole
		if itemRole := step.Get("role").String(); itemRole == "model" || itemRole == "assistant" {
			role = "model"
		} else if itemRole == "user" {
			role = "user"
		}
		steps.ForEach(func(_, child gjson.Result) bool {
			appendInteractionsStepToAntigravity(items, child, role)
			return true
		})
		return
	}
	switch step.Get("type").String() {
	case "model_output":
		appendInteractionsStepContentToAntigravity(items, "model", step, false)
	case "thought":
		appendInteractionsStepContentToAntigravity(items, "model", step, true)
	case "function_call":
		appendInteractionsFunctionCallToAntigravity(items, step)
	case "function_result":
		appendInteractionsFunctionResultToAntigravity(items, step)
	case "user_input", "":
		if step.Get("parts").Exists() {
			appendInteractionsNativeContentToAntigravity(items, step, defaultRole)
		} else {
			appendInteractionsContentListToAntigravity(items, defaultRole, step.Get("content"))
		}
	default:
		if step.Get("parts").Exists() {
			appendInteractionsNativeContentToAntigravity(items, step, defaultRole)
		} else if step.Get("content").Exists() {
			appendInteractionsContentListToAntigravity(items, defaultRole, step.Get("content"))
		} else if text := step.Get("text"); text.Exists() {
			appendAntigravityTextContent(items, defaultRole, text.String())
		}
	}
}

func appendInteractionsNativeContentToAntigravity(items *[][]byte, step gjson.Result, defaultRole string) {
	parts := step.Get("parts")
	if !parts.Exists() || !parts.IsArray() {
		return
	}
	partItems := make([][]byte, 0, 4)
	parts.ForEach(func(_, part gjson.Result) bool {
		if partJSON := interactionsNativeAntigravityPart(part); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
		return true
	})
	if len(partItems) > 0 {
		role := antigravityContentRole(step.Get("role").String(), defaultRole)
		*items = append(*items, antigravityContent(role, partItems))
	}
}

func appendInteractionsStepContentToAntigravity(items *[][]byte, role string, step gjson.Result, thought bool) {
	content := step.Get("content")
	if !content.Exists() {
		return
	}
	partItems := make([][]byte, 0, 4)
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			if partJSON := appendInteractionsContentToAntigravityPart(nil, part, thought); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := appendInteractionsContentToAntigravityPart(nil, content, thought); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == gjson.String {
		partItems = append(partItems, antigravityTextPartJSON(content.String(), thought))
	}
	if len(partItems) > 0 {
		*items = append(*items, antigravityContent(role, partItems))
	}
}

func appendInteractionsContentListToAntigravity(items *[][]byte, role string, content gjson.Result) {
	if !content.Exists() {
		return
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			appendInteractionsContentPartToAntigravity(items, role, part)
			return true
		})
		return
	}
	if content.IsObject() {
		appendInteractionsContentPartToAntigravity(items, role, content)
	} else if content.Type == gjson.String {
		appendAntigravityTextContent(items, role, content.String())
	}
}

func appendInteractionsContentPartToAntigravity(items *[][]byte, role string, part gjson.Result) {
	partJSON := appendInteractionsContentToAntigravityPart(nil, part, false)
	if len(partJSON) > 0 {
		*items = append(*items, antigravityContent(role, [][]byte{partJSON}))
	}
}

func appendInteractionsContentToAntigravityPart(_ []byte, content gjson.Result, thought bool) []byte {
	if text := content.Get("text"); text.Exists() {
		return antigravityTextPartJSON(text.String(), thought)
	}
	if inline := content.Get("inline_data"); inline.Exists() {
		return antigravityInlineDataPartJSON(inline)
	}
	if inline := content.Get("inlineData"); inline.Exists() {
		return antigravityInlineDataPartJSON(inline)
	}
	switch strings.ToLower(strings.TrimSpace(content.Get("type").String())) {
	case "text":
		if text := content.Get("text"); text.Exists() {
			return antigravityTextPartJSON(text.String(), thought)
		}
	case "image", "audio", "video", "document":
		if mime := content.Get("mime_type"); mime.Exists() || content.Get("mimeType").Exists() {
			mimeType := mime.String()
			if mimeType == "" {
				mimeType = content.Get("mimeType").String()
			}
			if data := content.Get("data").String(); data != "" {
				return antigravityInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
			}
		}
		if uri := content.Get("file_uri"); uri.Exists() || content.Get("fileUri").Exists() {
			fileURI := uri.String()
			if fileURI == "" {
				fileURI = content.Get("fileUri").String()
			}
			mimeType := content.Get("mime_type").String()
			if mimeType == "" {
				mimeType = content.Get("mimeType").String()
			}
			return antigravityFileDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mimeType":%q,"fileUri":%q}`, mimeType, fileURI)))
		}
		if url := content.Get("url"); url.Exists() {
			return antigravityInlineDataPartFromDataURL(url.String())
		}
	case "image_url":
		return antigravityInlineDataPartFromDataURL(content.Get("image_url.url").String())
	case "input_audio":
		mimeType := antigravityInputAudioMimeType(content.Get("input_audio.format").String())
		return antigravityInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, content.Get("input_audio.data").String())))
	case "file":
		filename := content.Get("file.filename").String()
		fileData := content.Get("file.file_data").String()
		ext := ""
		if sp := strings.Split(filename, "."); len(sp) > 1 {
			ext = sp[len(sp)-1]
		}
		if mimeType, ok := misc.MimeTypes[ext]; ok && fileData != "" {
			return antigravityInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, fileData)))
		}
	}
	return nil
}

func appendInteractionsFunctionCallToAntigravity(items *[][]byte, step gjson.Result) {
	part := []byte(`{"functionCall":{"name":"","args":{}}}`)
	part, _ = sjson.SetBytes(part, "functionCall.name", step.Get("name").String())
	if callID := step.Get("call_id"); callID.Exists() {
		part, _ = sjson.SetBytes(part, "functionCall.id", callID.String())
	} else if id := step.Get("id"); id.Exists() {
		part, _ = sjson.SetBytes(part, "functionCall.id", id.String())
	}
	if args := step.Get("arguments"); args.Exists() {
		part, _ = sjson.SetRawBytes(part, "functionCall.args", []byte(args.Raw))
	}
	*items = append(*items, antigravityContent("model", [][]byte{part}))
}

func appendInteractionsFunctionResultToAntigravity(items *[][]byte, step gjson.Result) {
	part := []byte(`{"functionResponse":{"name":"","response":{}}}`)
	part, _ = sjson.SetBytes(part, "functionResponse.name", step.Get("name").String())
	if callID := step.Get("call_id"); callID.Exists() {
		part, _ = sjson.SetBytes(part, "functionResponse.id", callID.String())
	} else if id := step.Get("id"); id.Exists() {
		part, _ = sjson.SetBytes(part, "functionResponse.id", id.String())
	}
	if result := step.Get("result"); result.Exists() {
		part, _ = sjson.SetRawBytes(part, "functionResponse.response", []byte(result.Raw))
	}
	*items = append(*items, antigravityContent("user", [][]byte{part}))
}

func copyInteractionsToolsToAntigravity(out []byte, root gjson.Result, functionNameMap map[string]string) []byte {
	tools := root.Get("tools")
	if !tools.Exists() {
		return out
	}
	if !tools.IsArray() {
		out, _ = sjson.SetRawBytes(out, "request.tools", []byte(tools.Raw))
		return out
	}
	var functionDeclarations [][]byte
	var otherTools [][]byte
	tools.ForEach(func(_, tool gjson.Result) bool {
		if decls := tool.Get("functionDeclarations"); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl gjson.Result) bool {
				if converted := antigravityFunctionDeclarationJSON(decl, functionNameMap); len(converted) > 0 {
					functionDeclarations = append(functionDeclarations, converted)
				}
				return true
			})
			return true
		}
		if decls := tool.Get("function_declarations"); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl gjson.Result) bool {
				if converted := antigravityFunctionDeclarationJSON(decl, functionNameMap); len(converted) > 0 {
					functionDeclarations = append(functionDeclarations, converted)
				}
				return true
			})
			return true
		}
		if tool.Get("type").String() == "function" || tool.Get("name").Exists() {
			if converted := antigravityFunctionDeclarationJSON(tool, functionNameMap); len(converted) > 0 {
				functionDeclarations = append(functionDeclarations, converted)
			}
			return true
		}
		otherTools = append(otherTools, []byte(tool.Raw))
		return true
	})
	deduplicated := util.DeduplicateFunctionDeclarations(translatorcommon.JoinRawArray(functionDeclarations))
	hasFunction := len(deduplicated) > 2
	if hasFunction || len(otherTools) > 0 {
		toolItems := make([][]byte, 0, 1+len(otherTools))
		if hasFunction {
			functionToolNode := []byte(`{"functionDeclarations":[]}`)
			functionToolNode, _ = sjson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
			toolItems = append(toolItems, functionToolNode)
		}
		toolItems = append(toolItems, otherTools...)
		out, _ = sjson.SetRawBytes(out, "request.tools", translatorcommon.JoinRawArray(toolItems))
	}
	return out
}

func antigravityFunctionDeclarationJSON(decl gjson.Result, functionNameMap map[string]string) []byte {
	fn := decl
	if nested := decl.Get("function"); nested.Exists() && nested.IsObject() {
		fn = nested
	}
	name := fn.Get("name").String()
	if strings.TrimSpace(name) == "" {
		return nil
	}
	out := []byte(`{"name":"","parametersJsonSchema":{"type":"object","properties":{}}}`)
	out, _ = sjson.SetBytes(out, "name", util.MapSanitizedFunctionName(functionNameMap, name))
	if desc := fn.Get("description"); desc.Exists() {
		out, _ = sjson.SetBytes(out, "description", desc.String())
	}
	if params := fn.Get("parametersJsonSchema"); params.Exists() {
		out, _ = sjson.SetRawBytes(out, "parametersJsonSchema", []byte(params.Raw))
	} else if params := fn.Get("parameters"); params.Exists() {
		out, _ = sjson.SetRawBytes(out, "parametersJsonSchema", []byte(params.Raw))
	}
	if response := fn.Get("response"); response.Exists() {
		out, _ = sjson.SetRawBytes(out, "response", []byte(response.Raw))
	}
	if responseSchema := fn.Get("responseJsonSchema"); responseSchema.Exists() {
		out, _ = sjson.SetRawBytes(out, "responseJsonSchema", []byte(responseSchema.Raw))
	}
	out, _ = sjson.DeleteBytes(out, "strict")
	return out
}

func interactionsNativeAntigravityPart(part gjson.Result) []byte {
	switch {
	case part.Get("text").Exists(), part.Get("functionCall").Exists(), part.Get("functionResponse").Exists():
		return []byte(part.Raw)
	case part.Get("inlineData").Exists():
		return antigravityInlineDataPartJSON(part.Get("inlineData"))
	case part.Get("fileData").Exists():
		return antigravityFileDataPartJSON(part.Get("fileData"))
	case part.Get("inline_data").Exists():
		return antigravityInlineDataPartJSON(part.Get("inline_data"))
	case part.Get("file_data").Exists():
		return antigravityFileDataPartJSON(part.Get("file_data"))
	}
	return nil
}

func antigravityTextPartJSON(text string, thought bool) []byte {
	partJSON := []byte(`{"text":""}`)
	partJSON, _ = sjson.SetBytes(partJSON, "text", text)
	if thought {
		partJSON, _ = sjson.SetBytes(partJSON, "thought", true)
	}
	return partJSON
}

func antigravityInlineDataPartJSON(inline gjson.Result) []byte {
	mimeType := inline.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inline.Get("mime_type").String()
	}
	data := inline.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	partJSON := []byte(`{"inlineData":{"mimeType":"","data":""}}`)
	partJSON, _ = sjson.SetBytes(partJSON, "inlineData.mimeType", mimeType)
	partJSON, _ = sjson.SetBytes(partJSON, "inlineData.data", data)
	return partJSON
}

func antigravityFileDataPartJSON(fileData gjson.Result) []byte {
	mimeType := fileData.Get("mimeType").String()
	if mimeType == "" {
		mimeType = fileData.Get("mime_type").String()
	}
	fileURI := fileData.Get("fileUri").String()
	if fileURI == "" {
		fileURI = fileData.Get("file_uri").String()
	}
	if mimeType == "" || fileURI == "" {
		return nil
	}
	partJSON := []byte(`{"fileData":{"mimeType":"","fileUri":""}}`)
	partJSON, _ = sjson.SetBytes(partJSON, "fileData.mimeType", mimeType)
	partJSON, _ = sjson.SetBytes(partJSON, "fileData.fileUri", fileURI)
	return partJSON
}

func antigravityInlineDataPartFromDataURL(dataURL string) []byte {
	if !strings.HasPrefix(dataURL, "data:") {
		return nil
	}
	payload := dataURL[5:]
	pieces := strings.SplitN(payload, ";", 2)
	if len(pieces) != 2 || !strings.HasPrefix(pieces[1], "base64,") {
		return nil
	}
	return antigravityInlineDataPartJSON(gjson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, pieces[0], pieces[1][7:])))
}

func appendAntigravityTextContent(items *[][]byte, role, text string) {
	part := antigravityTextPartJSON(text, false)
	*items = append(*items, antigravityContent(antigravityContentRole(role, "user"), [][]byte{part}))
}

func antigravityContent(role string, parts [][]byte) []byte {
	content := []byte(`{"role":"","parts":[]}`)
	content, _ = sjson.SetBytes(content, "role", role)
	content, _ = sjson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(parts))
	return content
}

func antigravityContentRole(role, defaultRole string) string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "model", "assistant":
		return "model"
	case "user":
		return "user"
	}
	if defaultRole == "model" {
		return "model"
	}
	return "user"
}

func antigravityInputAudioMimeType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "audio/wav"
	case "mp3":
		return "audio/mpeg"
	case "flac":
		return "audio/flac"
	case "opus":
		return "audio/opus"
	case "pcm16":
		return "audio/pcm"
	default:
		return "audio/mpeg"
	}
}

func antigravityThinkingSummariesIncludeThoughts(summary gjson.Result) (bool, bool) {
	switch summary.Type {
	case gjson.True:
		return true, true
	case gjson.False:
		return false, true
	case gjson.String:
		switch strings.ToLower(strings.TrimSpace(summary.String())) {
		case "", "none", "off", "false", "disabled":
			return false, true
		default:
			return true, true
		}
	}
	return false, false
}

func convertSnakeCaseKeysToCamelCaseForAntigravity(raw []byte) []byte {
	root := gjson.ParseBytes(raw)
	if !root.Exists() {
		return raw
	}
	out := []byte(`{}`)
	out = copySnakeCaseValueToCamelCaseForAntigravity(out, "", root)
	return out
}

func copySnakeCaseValueToCamelCaseForAntigravity(out []byte, path string, node gjson.Result) []byte {
	if node.IsObject() {
		node.ForEach(func(key, value gjson.Result) bool {
			childPath := joinAntigravityJSONPath(path, toAntigravityCamelCase(key.String()))
			out = copySnakeCaseValueToCamelCaseForAntigravity(out, childPath, value)
			return true
		})
		return out
	}
	if node.IsArray() {
		node.ForEach(func(_, value gjson.Result) bool {
			out = copySnakeCaseValueToCamelCaseForAntigravity(out, path+".-1", value)
			return true
		})
		return out
	}
	out, _ = sjson.SetRawBytes(out, path, []byte(node.Raw))
	return out
}

func joinAntigravityJSONPath(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func toAntigravityCamelCase(s string) string {
	parts := strings.Split(s, "_")
	if len(parts) == 0 {
		return s
	}
	out := parts[0]
	for _, part := range parts[1:] {
		if part == "" {
			continue
		}
		out += strings.ToUpper(part[:1]) + part[1:]
	}
	return out
}

func attachDefaultAntigravitySafetySettings(out []byte) []byte {
	if gjson.GetBytes(out, "request.safetySettings").Exists() {
		return out
	}
	settings := []map[string]string{
		{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
		{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "BLOCK_NONE"},
	}
	raw, errMarshal := json.Marshal(settings)
	if errMarshal != nil {
		return out
	}
	out, _ = sjson.SetRawBytes(out, "request.safetySettings", raw)
	return out
}
