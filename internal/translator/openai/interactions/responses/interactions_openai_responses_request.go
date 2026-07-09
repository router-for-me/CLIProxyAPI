package responses

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func ConvertOpenAIResponsesRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","input":[]}`)
	out, _ = sjson.SetBytes(out, "model", requestModel(modelName, root))
	if streamValue, ok := requestStreamValue(root, stream); ok {
		out, _ = sjson.SetBytes(out, "stream", streamValue)
	}
	if instructions := root.Get("instructions"); instructions.Exists() {
		out, _ = sjson.SetBytes(out, "system_instruction", responsesInstructionsText(instructions))
	}
	if previousResponseID := root.Get("previous_response_id"); previousResponseID.Exists() && previousResponseID.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "previous_interaction_id", previousResponseID.String())
	}
	if input := root.Get("input"); input.Exists() {
		out = appendResponsesInputToInteractions(out, input)
	}
	out = appendResponsesToolsToInteractions(out, root.Get("tools"))
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "generation_config.tool_choice", []byte(toolChoice.Raw))
	}
	if effort := root.Get("reasoning.effort"); effort.Exists() && effort.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "generation_config.thinking_level", strings.ToLower(strings.TrimSpace(effort.String())))
	}
	if summary := root.Get("reasoning.summary"); summary.Exists() && summary.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "generation_config.thinking_summaries", summary.String())
	}
	if format := root.Get("response_format"); format.Exists() {
		out, _ = sjson.SetRawBytes(out, "response_format", []byte(format.Raw))
	} else if format := root.Get("text.format"); format.Exists() {
		out, _ = sjson.SetRawBytes(out, "response_format", []byte(format.Raw))
	}
	return out
}

func ConvertInteractionsRequestToOpenAIResponses(modelName string, inputRawJSON []byte, stream bool) []byte {
	root := gjson.ParseBytes(inputRawJSON)
	out := []byte(`{"model":"","input":[]}`)
	out, _ = sjson.SetBytes(out, "model", requestModel(modelName, root))
	if stream || root.Get("stream").Bool() {
		out, _ = sjson.SetBytes(out, "stream", true)
	}
	if instructions := interactionsSystemInstructionText(root); instructions != "" {
		out, _ = sjson.SetBytes(out, "instructions", instructions)
	}
	if previousInteractionID := root.Get("previous_interaction_id"); previousInteractionID.Exists() && previousInteractionID.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "previous_response_id", previousInteractionID.String())
	}
	if input := root.Get("input"); input.Exists() {
		out = appendInteractionsInputToResponses(out, input)
	}
	out = appendInteractionsToolsToResponses(out, root.Get("tools"))
	if toolChoice := root.Get("generation_config.tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(toolChoice.Raw))
	} else if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		out, _ = sjson.SetRawBytes(out, "tool_choice", []byte(toolChoice.Raw))
	}
	if effort := interactionsThinkingEffort(root); effort != "" {
		out, _ = sjson.SetBytes(out, "reasoning.effort", effort)
	}
	if summary := root.Get("generation_config.thinking_summaries"); summary.Exists() && summary.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "reasoning.summary", summary.String())
	}
	if responseModalities := root.Get("response_modalities"); responseModalities.Exists() {
		out, _ = sjson.SetRawBytes(out, "modalities", []byte(responseModalities.Raw))
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == gjson.String {
		out, _ = sjson.SetBytes(out, "service_tier", serviceTier.String())
	}
	if format := root.Get("response_format"); format.Exists() {
		out, _ = sjson.SetRawBytes(out, "text.format", []byte(format.Raw))
	}
	return out
}

func requestModel(modelName string, root gjson.Result) string {
	if strings.TrimSpace(modelName) != "" {
		return modelName
	}
	return root.Get("model").String()
}

func requestStreamValue(root gjson.Result, stream bool) (bool, bool) {
	if value := root.Get("stream"); value.Exists() {
		return value.Bool(), true
	}
	if stream {
		return true, true
	}
	return false, false
}

func responsesInstructionsText(instructions gjson.Result) string {
	if instructions.Type == gjson.String {
		return instructions.String()
	}
	if text := instructions.Get("text"); text.Exists() {
		return text.String()
	}
	if parts := instructions.Get("content"); parts.Exists() && parts.IsArray() {
		var builder strings.Builder
		parts.ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text").String(); text != "" {
				builder.WriteString(text)
			}
			return true
		})
		return builder.String()
	}
	return instructions.String()
}

func interactionsSystemInstructionText(root gjson.Result) string {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		return ""
	}
	if sys.Type == gjson.String {
		return sys.String()
	}
	if text := sys.Get("text"); text.Exists() {
		return text.String()
	}
	if parts := sys.Get("parts"); parts.Exists() && parts.IsArray() {
		var builder strings.Builder
		parts.ForEach(func(_, part gjson.Result) bool {
			if text := part.Get("text").String(); text != "" {
				builder.WriteString(text)
			}
			return true
		})
		return builder.String()
	}
	return ""
}

func interactionsThinkingEffort(root gjson.Result) string {
	for _, path := range []string{
		"generation_config.thinking_level",
		"generation_config.thinkingConfig.thinkingLevel",
		"generation_config.thinkingConfig.thinking_level",
		"generation_config.thinking_config.thinking_level",
	} {
		if level := root.Get(path); level.Exists() && level.Type == gjson.String {
			return strings.ToLower(strings.TrimSpace(level.String()))
		}
	}
	return ""
}

func appendResponsesInputToInteractions(out []byte, input gjson.Result) []byte {
	functionNamesByCallID := make(map[string]string)
	if input.Type == gjson.String {
		return appendInteractionsTextStep(out, "user_input", input.String())
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			out = appendResponsesInputItemToInteractions(out, item, functionNamesByCallID)
			return true
		})
		return out
	}
	if input.IsObject() {
		return appendResponsesInputItemToInteractions(out, input, functionNamesByCallID)
	}
	return out
}

func appendResponsesInputItemToInteractions(out []byte, item gjson.Result, functionNamesByCallID map[string]string) []byte {
	switch item.Get("type").String() {
	case "message":
		stepType := "user_input"
		if role := item.Get("role").String(); role == "assistant" || role == "model" {
			stepType = "model_output"
		}
		step := []byte(`{"type":"","content":[]}`)
		step, _ = sjson.SetBytes(step, "type", stepType)
		step = appendResponsesContentToInteractions(step, item.Get("content"), stepType)
		out, _ = sjson.SetRawBytes(out, "input.-1", step)
	case "function_call":
		callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
		if callID != "" {
			if name := item.Get("name").String(); name != "" {
				functionNamesByCallID[callID] = name
			}
		}
		out, _ = sjson.SetRawBytes(out, "input.-1", responsesFunctionCallToInteractions(item))
	case "function_call_output":
		out, _ = sjson.SetRawBytes(out, "input.-1", responsesFunctionOutputToInteractions(item, functionNamesByCallID))
	case "input_text", "output_text", "text":
		stepType := "user_input"
		if item.Get("type").String() == "output_text" {
			stepType = "model_output"
		}
		out = appendInteractionsTextStep(out, stepType, item.Get("text").String())
	case "input_image", "output_image":
		stepType := "user_input"
		if item.Get("type").String() == "output_image" {
			stepType = "model_output"
		}
		step := []byte(`{"type":"","content":[]}`)
		step, _ = sjson.SetBytes(step, "type", stepType)
		if part, ok := responsesContentPartToInteractions(item); ok {
			step, _ = sjson.SetRawBytes(step, "content.-1", part)
		}
		out, _ = sjson.SetRawBytes(out, "input.-1", step)
	default:
		if content := item.Get("content"); content.Exists() {
			step := []byte(`{"type":"user_input","content":[]}`)
			step = appendResponsesContentToInteractions(step, content, "user_input")
			out, _ = sjson.SetRawBytes(out, "input.-1", step)
		}
	}
	return out
}

func appendResponsesContentToInteractions(step []byte, content gjson.Result, stepType string) []byte {
	if content.Type == gjson.String {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", content.String())
		step, _ = sjson.SetRawBytes(step, "content.-1", part)
		return step
	}
	if content.IsArray() {
		content.ForEach(func(_, item gjson.Result) bool {
			if part, ok := responsesContentPartToInteractions(item); ok {
				step, _ = sjson.SetRawBytes(step, "content.-1", part)
			}
			return true
		})
		return step
	}
	if content.IsObject() {
		if part, ok := responsesContentPartToInteractions(content); ok {
			step, _ = sjson.SetRawBytes(step, "content.-1", part)
		}
		return step
	}
	if stepType == "model_output" {
		return step
	}
	return step
}

func responsesContentPartToInteractions(part gjson.Result) ([]byte, bool) {
	switch part.Get("type").String() {
	case "input_text", "output_text", "text":
		out := []byte(`{"type":"text","text":""}`)
		out, _ = sjson.SetBytes(out, "text", part.Get("text").String())
		return out, true
	case "input_image", "output_image":
		return responsesImagePartToInteractions(part), true
	}
	if text := part.Get("text"); text.Exists() {
		out := []byte(`{"type":"text","text":""}`)
		out, _ = sjson.SetBytes(out, "text", text.String())
		return out, true
	}
	return nil, false
}

func responsesImagePartToInteractions(part gjson.Result) []byte {
	out := []byte(`{"type":"image"}`)
	imageURL := firstNonEmpty(part.Get("image_url").String(), part.Get("url").String())
	if mimeType, data, ok := parseDataURL(imageURL); ok {
		out, _ = sjson.SetBytes(out, "mime_type", mimeType)
		out, _ = sjson.SetBytes(out, "data", data)
		return out
	}
	if data := part.Get("data").String(); data != "" {
		out, _ = sjson.SetBytes(out, "data", data)
		if mimeType := part.Get("mime_type").String(); mimeType != "" {
			out, _ = sjson.SetBytes(out, "mime_type", mimeType)
		}
		return out
	}
	if imageURL != "" {
		out, _ = sjson.SetBytes(out, "image_url", imageURL)
	}
	return out
}

func responsesFunctionCallToInteractions(item gjson.Result) []byte {
	out := []byte(`{"type":"function_call","name":"","arguments":{}}`)
	out, _ = sjson.SetBytes(out, "name", item.Get("name").String())
	if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String()); callID != "" {
		out, _ = sjson.SetBytes(out, "call_id", callID)
	}
	setJSONValue(&out, "arguments", item.Get("arguments"), []byte(`{}`))
	return out
}

func responsesFunctionOutputToInteractions(item gjson.Result, functionNamesByCallID map[string]string) []byte {
	out := []byte(`{"type":"function_result","name":"","result":{}}`)
	callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
	if name := item.Get("name").String(); name != "" {
		out, _ = sjson.SetBytes(out, "name", name)
	} else if name := functionNamesByCallID[callID]; name != "" {
		out, _ = sjson.SetBytes(out, "name", name)
	}
	if callID != "" {
		out, _ = sjson.SetBytes(out, "call_id", callID)
	}
	result := item.Get("output")
	if !result.Exists() {
		result = item.Get("result")
	}
	setJSONValue(&out, "result", result, []byte(`{}`))
	return out
}

func appendInteractionsTextStep(out []byte, stepType, text string) []byte {
	step := []byte(`{"type":"","content":[{"type":"text","text":""}]}`)
	step, _ = sjson.SetBytes(step, "type", stepType)
	step, _ = sjson.SetBytes(step, "content.0.text", text)
	out, _ = sjson.SetRawBytes(out, "input.-1", step)
	return out
}

func appendResponsesToolsToInteractions(out []byte, tools gjson.Result) []byte {
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		switch tool.Get("type").String() {
		case "function", "":
			if converted, ok := functionToolToInteractions(tool); ok {
				out, _ = sjson.SetRawBytes(out, "tools.-1", converted)
			}
		case "namespace":
			group := []byte(`{"function_declarations":[]}`)
			children := tool.Get("children")
			if !children.Exists() {
				children = tool.Get("tools")
			}
			children.ForEach(func(_, child gjson.Result) bool {
				if converted, ok := functionDeclarationFromTool(child); ok {
					group, _ = sjson.SetRawBytes(group, "function_declarations.-1", converted)
				}
				return true
			})
			if gjson.GetBytes(group, "function_declarations.#").Int() > 0 {
				out, _ = sjson.SetRawBytes(out, "tools.-1", group)
			}
		}
		return true
	})
	return out
}

func functionToolToInteractions(tool gjson.Result) ([]byte, bool) {
	name := firstNonEmpty(tool.Get("name").String(), tool.Get("function.name").String())
	if name == "" {
		return nil, false
	}
	out := []byte(`{"type":"function","name":""}`)
	out, _ = sjson.SetBytes(out, "name", name)
	copyOptionalString(&out, "description", firstExisting(tool.Get("description"), tool.Get("function.description")))
	copyOptionalRaw(&out, "parameters", firstExisting(tool.Get("parameters"), tool.Get("function.parameters")))
	return out, true
}

func functionDeclarationFromTool(tool gjson.Result) ([]byte, bool) {
	name := firstNonEmpty(tool.Get("name").String(), tool.Get("function.name").String())
	if name == "" {
		return nil, false
	}
	out := []byte(`{"name":""}`)
	out, _ = sjson.SetBytes(out, "name", name)
	copyOptionalString(&out, "description", firstExisting(tool.Get("description"), tool.Get("function.description")))
	copyOptionalRaw(&out, "parameters", firstExisting(tool.Get("parameters"), tool.Get("function.parameters")))
	return out, true
}

func appendInteractionsInputToResponses(out []byte, input gjson.Result) []byte {
	if input.Type == gjson.String {
		item := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
		item, _ = sjson.SetBytes(item, "content.0.text", input.String())
		out, _ = sjson.SetRawBytes(out, "input.-1", item)
		return out
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			out = appendInteractionsInputItemToResponses(out, item)
			return true
		})
		return out
	}
	if input.IsObject() {
		return appendInteractionsInputItemToResponses(out, input)
	}
	return out
}

func appendInteractionsInputItemToResponses(out []byte, item gjson.Result) []byte {
	switch item.Get("type").String() {
	case "user_input":
		out, _ = sjson.SetRawBytes(out, "input.-1", interactionsMessageToResponses(item, "user"))
	case "model_output":
		out, _ = sjson.SetRawBytes(out, "input.-1", interactionsMessageToResponses(item, "assistant"))
	case "thought":
		out, _ = sjson.SetRawBytes(out, "input.-1", interactionsThoughtToResponses(item))
	case "function_call":
		out, _ = sjson.SetRawBytes(out, "input.-1", interactionsFunctionCallToResponses(item))
	case "function_result":
		out, _ = sjson.SetRawBytes(out, "input.-1", interactionsFunctionResultToResponses(item))
	default:
		if item.Type == gjson.String {
			return appendInteractionsInputToResponses(out, item)
		}
	}
	return out
}

func interactionsMessageToResponses(item gjson.Result, role string) []byte {
	out := []byte(`{"type":"message","role":"","content":[]}`)
	out, _ = sjson.SetBytes(out, "role", role)
	content := item.Get("content")
	if content.Type == gjson.String {
		partType := "input_text"
		if role == "assistant" {
			partType = "output_text"
		}
		part := []byte(`{"type":"","text":""}`)
		part, _ = sjson.SetBytes(part, "type", partType)
		part, _ = sjson.SetBytes(part, "text", content.String())
		out, _ = sjson.SetRawBytes(out, "content.-1", part)
		return out
	}
	content.ForEach(func(_, part gjson.Result) bool {
		if converted, ok := interactionsContentPartToResponses(part, role); ok {
			out, _ = sjson.SetRawBytes(out, "content.-1", converted)
		}
		return true
	})
	return out
}

func interactionsThoughtToResponses(item gjson.Result) []byte {
	out := []byte(`{"type":"reasoning","summary":[]}`)
	for _, text := range interactionsContentTexts(item.Get("content")) {
		part := []byte(`{"type":"summary_text","text":""}`)
		part, _ = sjson.SetBytes(part, "text", text)
		out, _ = sjson.SetRawBytes(out, "summary.-1", part)
	}
	return out
}

func interactionsContentPartToResponses(part gjson.Result, role string) ([]byte, bool) {
	partType := part.Get("type").String()
	if partType == "" && part.Get("text").Exists() {
		partType = "text"
	}
	switch partType {
	case "text":
		outType := "input_text"
		if role == "assistant" {
			outType = "output_text"
		}
		out := []byte(`{"type":"","text":""}`)
		out, _ = sjson.SetBytes(out, "type", outType)
		out, _ = sjson.SetBytes(out, "text", part.Get("text").String())
		return out, true
	case "image":
		outType := "input_image"
		if role == "assistant" {
			outType = "output_image"
		}
		out := []byte(`{"type":""}`)
		out, _ = sjson.SetBytes(out, "type", outType)
		imageURL := interactionsMediaDataURL(part)
		if imageURL != "" {
			out, _ = sjson.SetBytes(out, "image_url", imageURL)
		}
		return out, true
	case "audio":
		out := []byte(`{"type":"output_text","text":""}`)
		format := mediaFormat(part.Get("mime_type").String())
		out, _ = sjson.SetBytes(out, "text", "Audio content: inline data (Format: "+format+")")
		return out, true
	case "video", "document":
		outType := "input_file"
		if role == "assistant" {
			outType = "output_file"
		}
		out := []byte(`{"type":""}`)
		out, _ = sjson.SetBytes(out, "type", outType)
		if dataURL := interactionsMediaDataURL(part); dataURL != "" {
			out, _ = sjson.SetBytes(out, "file_data", dataURL)
		}
		if filename := part.Get("filename").String(); filename != "" {
			out, _ = sjson.SetBytes(out, "filename", filename)
		}
		return out, true
	}
	return nil, false
}

func interactionsFunctionCallToResponses(item gjson.Result) []byte {
	out := []byte(`{"type":"function_call","call_id":"","name":"","arguments":"{}"}`)
	if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String()); callID != "" {
		out, _ = sjson.SetBytes(out, "call_id", callID)
	}
	out, _ = sjson.SetBytes(out, "name", item.Get("name").String())
	out, _ = sjson.SetBytes(out, "arguments", jsonStringValue(item.Get("arguments"), "{}"))
	return out
}

func interactionsFunctionResultToResponses(item gjson.Result) []byte {
	out := []byte(`{"type":"function_call_output","call_id":"","output":""}`)
	if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String()); callID != "" {
		out, _ = sjson.SetBytes(out, "call_id", callID)
	}
	if name := item.Get("name").String(); name != "" {
		out, _ = sjson.SetBytes(out, "name", name)
	}
	result := item.Get("result")
	if !result.Exists() {
		result = item.Get("output")
	}
	out, _ = sjson.SetBytes(out, "output", jsonStringValue(result, ""))
	return out
}

func appendInteractionsToolsToResponses(out []byte, tools gjson.Result) []byte {
	if !tools.Exists() || !tools.IsArray() {
		return out
	}
	tools.ForEach(func(_, tool gjson.Result) bool {
		if converted, ok := responsesToolFromInteractionsTool(tool); ok {
			out, _ = sjson.SetRawBytes(out, "tools.-1", converted)
		}
		if decls := tool.Get("function_declarations"); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl gjson.Result) bool {
				if converted, ok := responsesToolFromInteractionsTool(decl); ok {
					out, _ = sjson.SetRawBytes(out, "tools.-1", converted)
				}
				return true
			})
		}
		return true
	})
	return out
}

func responsesToolFromInteractionsTool(tool gjson.Result) ([]byte, bool) {
	name := firstNonEmpty(tool.Get("name").String(), tool.Get("function.name").String())
	if name == "" {
		return nil, false
	}
	out := []byte(`{"type":"function","name":""}`)
	out, _ = sjson.SetBytes(out, "name", name)
	copyOptionalString(&out, "description", firstExisting(tool.Get("description"), tool.Get("function.description")))
	copyOptionalRaw(&out, "parameters", firstExisting(tool.Get("parameters"), tool.Get("function.parameters"), tool.Get("parametersJsonSchema")))
	return out, true
}

func interactionsContentTexts(content gjson.Result) []string {
	texts := make([]string, 0)
	if content.Type == gjson.String {
		return append(texts, content.String())
	}
	if content.IsArray() {
		content.ForEach(func(_, part gjson.Result) bool {
			if text := firstNonEmpty(part.Get("text").String(), part.Get("content.text").String()); text != "" {
				texts = append(texts, text)
			}
			return true
		})
	}
	return texts
}

func interactionsMediaDataURL(part gjson.Result) string {
	if url := firstNonEmpty(part.Get("image_url").String(), part.Get("file_data").String(), part.Get("url").String()); url != "" {
		return url
	}
	data := part.Get("data").String()
	if data == "" {
		return ""
	}
	mimeType := part.Get("mime_type").String()
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return "data:" + mimeType + ";base64," + data
}

func mediaFormat(mimeType string) string {
	if mimeType == "" {
		return "unknown"
	}
	if _, format, ok := strings.Cut(mimeType, "/"); ok && format != "" {
		return format
	}
	return mimeType
}

func parseDataURL(value string) (string, string, bool) {
	if !strings.HasPrefix(value, "data:") {
		return "", "", false
	}
	header, data, ok := strings.Cut(strings.TrimPrefix(value, "data:"), ",")
	if !ok {
		return "", "", false
	}
	mimeType, _, _ := strings.Cut(header, ";")
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	return mimeType, data, true
}

func setJSONValue(out *[]byte, path string, value gjson.Result, defaultRaw []byte) {
	if !value.Exists() {
		*out, _ = sjson.SetRawBytes(*out, path, defaultRaw)
		return
	}
	if value.Type == gjson.String && gjson.Valid(value.String()) {
		*out, _ = sjson.SetRawBytes(*out, path, []byte(value.String()))
		return
	}
	if value.Type == gjson.String {
		*out, _ = sjson.SetBytes(*out, path, value.String())
		return
	}
	*out, _ = sjson.SetRawBytes(*out, path, []byte(value.Raw))
}

func jsonStringValue(value gjson.Result, fallback string) string {
	if !value.Exists() {
		return fallback
	}
	if value.Type == gjson.String {
		return value.String()
	}
	return value.Raw
}

func copyOptionalString(out *[]byte, path string, value gjson.Result) {
	if value.Exists() {
		*out, _ = sjson.SetBytes(*out, path, value.String())
	}
}

func copyOptionalRaw(out *[]byte, path string, value gjson.Result) {
	if value.Exists() {
		*out, _ = sjson.SetRawBytes(*out, path, []byte(value.Raw))
	}
}

func firstExisting(values ...gjson.Result) gjson.Result {
	for _, value := range values {
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
