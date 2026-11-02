package responses

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	applypatch "github.com/router-for-me/CLIProxyAPI/v8/internal/client/codex/apply-patch"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func ConvertOpenAIResponsesRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertOpenAIResponsesRequestToInteractions(modelName, inputRawJSON, stream)

}

// convertOpenAIResponsesRequestToInteractions also reports a file, audio or video
// part Interactions cannot receive when it leaves a user turn with nothing to send.
func convertOpenAIResponsesRequestToInteractions(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	var drops translatorcommon.UserTurnDrops
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	outDoc := ojson.NewDocString(`{"model":"","input":[]}`)
	model := requestModel(modelName, root)
	_ = outDoc.Set("model", model)
	if streamValue, ok := requestStreamValue(root, stream); ok {
		_ = outDoc.Set("stream", streamValue)
	}
	if instructions := root.Get("instructions"); instructions.Exists() {
		_ = outDoc.Set("system_instruction", responsesInstructionsText(instructions))
	}
	if previousResponseID := firstNonEmpty(root.Get("previous_response_id").String(), root.Get("previous_interaction_id").String()); previousResponseID != "" {
		_ = outDoc.Set("previous_interaction_id", previousResponseID)
	}
	if environmentID := firstNonEmpty(root.Get("environment_id").String(), root.Get("environment.id").String()); environmentID != "" {
		_ = outDoc.Set("environment_id", environmentID)
	}
	if agentConfig := root.Get("agent_config"); agentConfig.Exists() {
		_ = outDoc.SetRaw("agent_config", agentConfig.Raw)
	}
	forAntigravity := isAntigravityModel(model)
	forDevin := isDevinModel(model) || !forAntigravity
	if input := root.Get("input"); input.Exists() {
		setResponsesInputOnInteractionsDoc(outDoc, input, forAntigravity, &drops)
	}
	appendResponsesToolsToInteractionsDoc(outDoc, root, forAntigravity, forDevin)
	if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		if toolChoice.IsObject() {
			tcRaw := []byte(toolChoice.Raw)
			fnName := firstNonEmpty(toolChoice.Get("function.name").String(), toolChoice.Get("name").String(), toolChoice.Get("custom.name").String())
			ns := firstNonEmpty(toolChoice.Get("namespace").String(), toolChoice.Get("function.namespace").String(), toolChoice.Get("custom.namespace").String())
			if ns != "" && fnName != "" {
				fnName = util.QualifyResponsesNamespaceToolName(ns, fnName)
			}
			if forDevin && (translatorcommon.IsDevinCodexAppAutomationUpdate(ns, fnName) || translatorcommon.IsDevinCodexAppAutomationUpdate("", fnName)) {
				tcRaw = nil
			}
			if forAntigravity && fnName != "" {
				fnName = translatorcommon.AntigravityToolNameToUpstream(fnName)
			}
			if fnName != "" && tcRaw != nil {
				if toolChoice.Get("function.name").Exists() {
					tcRaw, _ = ojson.SetBytes(tcRaw, "function.name", fnName)
				} else if toolChoice.Get("name").Exists() {
					tcRaw, _ = ojson.SetBytes(tcRaw, "name", fnName)
				} else if toolChoice.Get("custom.name").Exists() {
					tcRaw, _ = ojson.SetBytes(tcRaw, "custom.name", fnName)
				}
			}
			if tcRaw != nil {
				_ = outDoc.SetRawBytes("generation_config.tool_choice", tcRaw)
			}
		} else {
			_ = outDoc.SetRaw("generation_config.tool_choice", toolChoice.Raw)
		}
	}
	if effort := root.Get("reasoning.effort"); effort.Exists() && effort.Type == ojson.String {
		_ = outDoc.Set("generation_config.thinking_level", strings.ToLower(strings.TrimSpace(effort.String())))
	}
	if summary := root.Get("reasoning.summary"); summary.Exists() && summary.Type == ojson.String {
		_ = outDoc.Set("generation_config.thinking_summaries", summary.String())
	}
	if format := root.Get("response_format"); format.Exists() {
		_ = outDoc.SetRaw("response_format", format.Raw)
	} else if format := root.Get("text.format"); format.Exists() {
		_ = outDoc.SetRaw("response_format", format.Raw)
	}
	if isAntigravityModel(model) {
		if maxOutputTokens := firstExisting(root.Get("max_output_tokens"), root.Get("max_tokens"), root.Get("max_completion_tokens")); maxOutputTokens.Exists() && !root.Get("agent_config.max_total_tokens").Exists() {
			_ = outDoc.Set("agent_config.max_total_tokens", maxOutputTokens.Int())
		}
		for _, knob := range []string{"temperature", "top_p", "top_k", "stop_sequences", "max_output_tokens", "presence_penalty", "frequency_penalty", "candidate_count"} {
			_ = outDoc.Delete("generation_config." + knob)
		}
	} else {
		if maxOutputTokens := firstExisting(root.Get("max_output_tokens"), root.Get("max_tokens"), root.Get("max_completion_tokens")); maxOutputTokens.Exists() {
			_ = outDoc.Set("generation_config.max_output_tokens", maxOutputTokens.Int())
		}
		if temp := root.Get("temperature"); temp.Exists() {
			_ = outDoc.Set("generation_config.temperature", temp.Float())
		}
		if topP := root.Get("top_p"); topP.Exists() {
			_ = outDoc.Set("generation_config.top_p", topP.Float())
		}
		if presencePenalty := root.Get("presence_penalty"); presencePenalty.Exists() {
			_ = outDoc.Set("generation_config.presence_penalty", presencePenalty.Float())
		}
		if frequencyPenalty := root.Get("frequency_penalty"); frequencyPenalty.Exists() {
			_ = outDoc.Set("generation_config.frequency_penalty", frequencyPenalty.Float())
		}
		if stop := root.Get("stop"); stop.Exists() {
			_ = outDoc.SetRaw("generation_config.stop_sequences", stop.Raw)
		}
	}
	return outDoc.Bytes(), drops.Err()
}

func ConvertInteractionsRequestToOpenAIResponses(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	outDoc := ojson.NewDocString(`{"model":"","input":[]}`)
	model := requestModel(modelName, root)
	_ = outDoc.Set("model", model)
	if stream || root.Get("stream").Bool() {
		_ = outDoc.Set("stream", true)
	}
	if instructions := interactionsSystemInstructionText(root); instructions != "" {
		_ = outDoc.Set("instructions", instructions)
	}
	if previousInteractionID := firstNonEmpty(root.Get("previous_interaction_id").String(), root.Get("previous_response_id").String()); previousInteractionID != "" {
		_ = outDoc.Set("previous_response_id", previousInteractionID)
	}
	if environmentID := firstNonEmpty(root.Get("environment_id").String(), root.Get("environment.id").String()); environmentID != "" {
		_ = outDoc.Set("environment_id", environmentID)
	}
	if agentConfig := root.Get("agent_config"); agentConfig.Exists() {
		_ = outDoc.SetRaw("agent_config", agentConfig.Raw)
	}
	forAntigravity := isAntigravityModel(model)
	if input := root.Get("input"); input.Exists() {
		setInteractionsInputOnResponsesDoc(outDoc, input, forAntigravity)
	}
	appendInteractionsToolsToResponsesDoc(outDoc, root.Get("tools"), forAntigravity)
	if toolChoice := root.Get("generation_config.tool_choice"); toolChoice.Exists() {
		_ = outDoc.SetRaw("tool_choice", toolChoice.Raw)
	} else if toolChoice := root.Get("tool_choice"); toolChoice.Exists() {
		_ = outDoc.SetRaw("tool_choice", toolChoice.Raw)
	}
	if effort := interactionsThinkingEffort(root); effort != "" {
		_ = outDoc.Set("reasoning.effort", effort)
	}
	if summary := root.Get("generation_config.thinking_summaries"); summary.Exists() && summary.Type == ojson.String {
		_ = outDoc.Set("reasoning.summary", summary.String())
	}
	if responseModalities := root.Get("response_modalities"); responseModalities.Exists() {
		_ = outDoc.SetRaw("modalities", responseModalities.Raw)
	}
	if serviceTier := root.Get("service_tier"); serviceTier.Exists() && serviceTier.Type == ojson.String {
		_ = outDoc.Set("service_tier", serviceTier.String())
	}
	if format := root.Get("response_format"); format.Exists() {
		_ = outDoc.SetRaw("text.format", format.Raw)
	}
	return outDoc.Bytes(), nil
}

func requestModel(modelName string, root ojson.Result) string {
	if strings.TrimSpace(modelName) != "" {
		return modelName
	}
	return root.Get("model").String()
}

func requestStreamValue(root ojson.Result, stream bool) (bool, bool) {
	if value := root.Get("stream"); value.Exists() {
		return value.Bool(), true
	}
	if stream {
		return true, true
	}
	return false, false
}

func responsesInstructionsText(instructions ojson.Result) string {
	if instructions.Type == ojson.String {
		return instructions.String()
	}
	if text := instructions.Get("text"); text.Exists() {
		return text.String()
	}
	if parts := instructions.Get("content"); parts.Exists() && parts.IsArray() {
		var builder strings.Builder
		parts.ForEach(func(_, part ojson.Result) bool {
			if text := part.Get("text").String(); text != "" {
				builder.WriteString(text)
			}
			return true
		})
		return builder.String()
	}
	return instructions.String()
}

func interactionsSystemInstructionText(root ojson.Result) string {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		return ""
	}
	if sys.Type == ojson.String {
		return sys.String()
	}
	if text := sys.Get("text"); text.Exists() {
		return text.String()
	}
	if parts := sys.Get("parts"); parts.Exists() && parts.IsArray() {
		var builder strings.Builder
		parts.ForEach(func(_, part ojson.Result) bool {
			if text := part.Get("text").String(); text != "" {
				builder.WriteString(text)
			}
			return true
		})
		return builder.String()
	}
	return ""
}

func interactionsThinkingEffort(root ojson.Result) string {
	for _, path := range []string{
		"generation_config.thinking_level",
		"generation_config.thinkingConfig.thinkingLevel",
		"generation_config.thinkingConfig.thinking_level",
		"generation_config.thinking_config.thinking_level",
	} {
		if level := root.Get(path); level.Exists() && level.Type == ojson.String {
			return strings.ToLower(strings.TrimSpace(level.String()))
		}
	}
	return ""
}

func setResponsesInputOnInteractionsDoc(outDoc *ojson.Doc, input ojson.Result, forAntigravity bool, drops *translatorcommon.UserTurnDrops) {
	functionNamesByCallID := make(map[string]string)
	items := make([][]byte, 0)
	if input.Type == ojson.String {
		items = append(items, interactionsTextStep("user_input", input.String()))
	} else if input.IsArray() {
		input.ForEach(func(_, item ojson.Result) bool {
			if converted := responsesInputItemToInteractions(item, functionNamesByCallID, forAntigravity, drops); converted != nil {
				items = append(items, converted)
			}
			return true
		})
	} else if input.IsObject() {
		if converted := responsesInputItemToInteractions(input, functionNamesByCallID, forAntigravity, drops); converted != nil {
			items = append(items, converted)
		}
	}
	if len(items) > 0 {
		_ = outDoc.SetRawBytes("input", translatorcommon.JoinRawArray(items))
	}
}

// responsesUserTurnDrops returns drops when the item is a real user turn. A
// system or developer message is also sent as a user_input step, but it is not
// the user's own turn and must never be refused or hide an emptied one.
func responsesUserTurnDrops(role string, drops *translatorcommon.UserTurnDrops) *translatorcommon.UserTurnDrops {
	if role == "" || role == "user" {
		return drops
	}
	return nil
}

func responsesInputItemToInteractions(item ojson.Result, functionNamesByCallID map[string]string, forAntigravity bool, drops *translatorcommon.UserTurnDrops) []byte {
	switch item.Get("type").String() {
	case "message":
		stepType := "user_input"
		role := item.Get("role").String()
		turnDrops := responsesUserTurnDrops(role, drops)
		if role == "assistant" || role == "model" {
			stepType = "model_output"
			turnDrops = nil
		}
		stepDoc := ojson.NewDocString(`{"type":"","content":[]}`)
		_ = stepDoc.Set("type", stepType)
		appendResponsesContentToInteractionsDoc(stepDoc, item.Get("content"), turnDrops)
		return stepDoc.Bytes()
	case "function_call":
		callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
		name := item.Get("name").String()
		if ns := item.Get("namespace").String(); ns != "" && name != "" {
			name = util.QualifyResponsesNamespaceToolName(ns, name)
		}
		if callID != "" && name != "" {
			functionNamesByCallID[callID] = name
		}
		return responsesFunctionCallToInteractions(item, forAntigravity)
	case "custom_tool_call":
		callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
		name := item.Get("name").String()
		if ns := item.Get("namespace").String(); ns != "" && name != "" {
			name = util.QualifyResponsesNamespaceToolName(ns, name)
		}
		if callID != "" && name != "" {
			functionNamesByCallID[callID] = name
		}
		return responsesCustomToolCallToInteractions(item, forAntigravity)
	case "function_call_output", "custom_tool_call_output":
		return responsesFunctionOutputToInteractions(item, functionNamesByCallID, forAntigravity)
	case "input_text", "output_text", "text":
		stepType := "user_input"
		if item.Get("type").String() == "output_text" {
			stepType = "model_output"
		}
		return interactionsTextStep(stepType, item.Get("text").String())
	case "input_image", "output_image", "input_file", "input_audio", "input_video":
		stepType := "user_input"
		var turnDrops *translatorcommon.UserTurnDrops
		if item.Get("type").String() == "output_image" {
			stepType = "model_output"
		} else {
			turnDrops = drops
		}
		stepDoc := ojson.NewDocString(`{"type":"","content":[]}`)
		_ = stepDoc.Set("type", stepType)
		appendResponsesContentToInteractionsDoc(stepDoc, item, turnDrops)
		return stepDoc.Bytes()
	default:
		if content := item.Get("content"); content.Exists() {
			stepDoc := ojson.NewDocString(`{"type":"user_input","content":[]}`)
			appendResponsesContentToInteractionsDoc(stepDoc, content, responsesUserTurnDrops(item.Get("role").String(), drops))
			return stepDoc.Bytes()
		}
	}
	return nil
}

// appendResponsesContentToInteractionsDoc converts one message content into step
// content. A non-nil drops closes the user turn: an attachment that cannot be
// sent is recorded, and text or any other sendable part beside it keeps the turn
// alive. An empty text part is forwarded but does not count as sendable.
func appendResponsesContentToInteractionsDoc(stepDoc *ojson.Doc, content ojson.Result, drops *translatorcommon.UserTurnDrops) {
	var contentItems [][]byte
	sendable := 0
	appendPart := func(item ojson.Result) {
		part, ok := responsesContentPartToInteractions(item)
		if !ok {
			if partType := item.Get("type").String(); drops != nil && isResponsesUnsendableAttachmentType(partType) {
				drops.Drop(partType)
			}
			return
		}
		contentItems = append(contentItems, part)
		if ojson.GetBytesNoCopy(part, "type").String() != "text" || ojson.GetBytesNoCopy(part, "text").String() != "" {
			sendable++
		}
	}
	if content.Type == ojson.String {
		part := []byte(`{"type":"text","text":""}`)
		part, _ = ojson.SetBytes(part, "text", content.String())
		contentItems = append(contentItems, part)
		if content.String() != "" {
			sendable++
		}
	} else if content.IsArray() {
		content.ForEach(func(_, item ojson.Result) bool {
			appendPart(item)
			return true
		})
	} else if content.IsObject() {
		appendPart(content)
	}
	if drops != nil {
		drops.EndTurn(sendable)
	}
	if len(contentItems) > 0 {
		translatorcommon.SetRawArrayItemsDoc(stepDoc, "content", contentItems)
	}
}

// isResponsesUnsendableAttachmentType reports the Responses attachment types
// whose loss must be refused when they leave a user turn empty. A file id,
// a part without bytes and a video have no Interactions counterpart.
func isResponsesUnsendableAttachmentType(partType string) bool {
	switch partType {
	case "input_file", "input_audio", "input_video":
		return true
	}
	return false
}

func responsesContentPartToInteractions(part ojson.Result) ([]byte, bool) {
	switch part.Get("type").String() {
	case "input_text", "output_text", "text":
		out := []byte(`{"type":"text","text":""}`)
		out, _ = ojson.SetBytes(out, "text", part.Get("text").String())
		return out, true
	case "input_image", "output_image":
		return responsesImagePartToInteractions(part), true
	case "input_file":
		return responsesFilePartToInteractions(part)
	case "input_audio":
		return responsesAudioPartToInteractions(part)
	}
	if text := part.Get("text"); text.Exists() {
		out := []byte(`{"type":"text","text":""}`)
		out, _ = ojson.SetBytes(out, "text", text.String())
		return out, true
	}
	return nil, false
}

// responsesFilePartToInteractions maps an input_file onto an Interactions
// document. Interactions takes inline bytes or a url; a bare file_id names a file
// only the OpenAI side can resolve, so it reports false.
func responsesFilePartToInteractions(part ojson.Result) ([]byte, bool) {
	filename := part.Get("filename").String()
	fallbackMIMEType := firstNonEmpty(part.Get("mime_type").String(), part.Get("mimeType").String())
	outDoc := ojson.NewDocString(`{"type":"document"}`)
	if filename != "" {
		_ = outDoc.Set("filename", filename)
	}
	hasContent := false
	if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, fallbackMIMEType, part.Get("file_data").String()); ok {
		_ = outDoc.Set("mime_type", mimeType)
		_ = outDoc.Set("data", data)
		hasContent = true
	}
	if fileURL := part.Get("file_url").String(); fileURL != "" {
		_ = outDoc.Set("file_url", fileURL)
		hasContent = true
	}
	return outDoc.Bytes(), hasContent
}

// responsesAudioPartToInteractions maps an input_audio with inline bytes onto an
// Interactions audio part.
func responsesAudioPartToInteractions(part ojson.Result) ([]byte, bool) {
	audio := part.Get("input_audio")
	data := firstNonEmpty(audio.Get("data").String(), part.Get("data").String())
	if data == "" {
		return nil, false
	}
	outDoc := ojson.NewDocString(`{"type":"audio","data":""}`)
	_ = outDoc.Set("data", data)
	if format := firstNonEmpty(audio.Get("format").String(), part.Get("format").String()); format != "" {
		_ = outDoc.Set("mime_type", responsesInputAudioMIMEType(format))
	}
	return outDoc.Bytes(), true
}

func responsesInputAudioMIMEType(format string) string {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "wav":
		return "audio/wav"
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

func responsesImagePartToInteractions(part ojson.Result) []byte {
	outDoc := ojson.NewDocString(`{"type":"image"}`)
	imageURL := firstNonEmpty(part.Get("image_url").String(), part.Get("url").String())
	if mimeType, data, ok := parseDataURL(imageURL); ok {
		_ = outDoc.Set("mime_type", mimeType)
		_ = outDoc.Set("data", data)
		return outDoc.Bytes()
	}
	if data := part.Get("data").String(); data != "" {
		_ = outDoc.Set("data", data)
		if mimeType := part.Get("mime_type").String(); mimeType != "" {
			_ = outDoc.Set("mime_type", mimeType)
		}
		return outDoc.Bytes()
	}
	if imageURL != "" {
		_ = outDoc.Set("image_url", imageURL)
	}
	return outDoc.Bytes()
}

func responsesFunctionCallToInteractions(item ojson.Result, forAntigravity bool) []byte {
	outDoc := ojson.NewDocString(`{"type":"function_call","name":"","arguments":{}}`)
	name := item.Get("name").String()
	if ns := item.Get("namespace").String(); ns != "" && name != "" {
		name = util.QualifyResponsesNamespaceToolName(ns, name)
	}
	if forAntigravity {
		name = translatorcommon.AntigravityToolNameToUpstream(name)
	}
	_ = outDoc.Set("name", name)
	if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String()); callID != "" {
		_ = outDoc.Set("call_id", callID)
	}
	setJSONValueDoc(outDoc, "arguments", item.Get("arguments"), `{}`)
	return outDoc.Bytes()
}

func responsesCustomToolCallToInteractions(item ojson.Result, forAntigravity bool) []byte {
	outDoc := ojson.NewDocString(`{"type":"function_call","name":"","arguments":{}}`)
	name := item.Get("name").String()
	if ns := item.Get("namespace").String(); ns != "" && name != "" {
		name = util.QualifyResponsesNamespaceToolName(ns, name)
	}
	if forAntigravity {
		name = translatorcommon.AntigravityToolNameToUpstream(name)
	}
	_ = outDoc.Set("name", name)
	if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String()); callID != "" {
		_ = outDoc.Set("call_id", callID)
	}
	if input := item.Get("input"); input.Exists() {
		_ = outDoc.Set("arguments.input", input.String())
	} else {
		setJSONValueDoc(outDoc, "arguments", item.Get("arguments"), `{}`)
	}
	return outDoc.Bytes()
}

func responsesFunctionOutputToInteractions(item ojson.Result, functionNamesByCallID map[string]string, forAntigravity bool) []byte {
	outDoc := ojson.NewDocString(`{"type":"function_result","name":"","result":{}}`)
	callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
	name := item.Get("name").String()
	if ns := item.Get("namespace").String(); ns != "" && name != "" {
		name = util.QualifyResponsesNamespaceToolName(ns, name)
	}
	if name == "" && callID != "" {
		name = functionNamesByCallID[callID]
	}
	if name != "" {
		if forAntigravity {
			name = translatorcommon.AntigravityToolNameToUpstream(name)
		}
		_ = outDoc.Set("name", name)
	}
	if callID != "" {
		_ = outDoc.Set("call_id", callID)
	}
	result := item.Get("output")
	if !result.Exists() {
		result = item.Get("result")
	}
	setJSONValueDoc(outDoc, "result", result, `{}`)
	return outDoc.Bytes()
}

func interactionsTextStep(stepType, text string) []byte {
	stepDoc := ojson.NewDocString(`{"type":"","content":[{"type":"text","text":""}]}`)
	_ = stepDoc.Set("type", stepType)
	_ = stepDoc.Set("content.0.text", text)
	return stepDoc.Bytes()
}

func appendResponsesToolsToInteractionsDoc(outDoc *ojson.Doc, root ojson.Result, forAntigravity bool, forDevin bool) {
	if !root.Exists() {
		return
	}
	targetRoot := root
	if root.IsArray() {
		targetRoot = ojson.Parse(`{"tools":` + root.Raw + `}`)
	}
	descriptors := util.CollectResponsesToolDescriptors(targetRoot)
	if len(descriptors) == 0 {
		return
	}
	winners := util.CollectResponsesToolWinners(targetRoot)

	seenNames := make(map[string]struct{})
	var toolItems [][]byte
	for _, descriptor := range descriptors {
		winner, ok := winners[descriptor.Name]
		if !ok || winner.Order != descriptor.Order {
			continue
		}
		if _, seen := seenNames[descriptor.Name]; seen {
			continue
		}
		seenNames[descriptor.Name] = struct{}{}

		if forDevin && (translatorcommon.IsDevinCodexAppAutomationUpdate(descriptor.Namespace, descriptor.LocalName) || translatorcommon.IsDevinCodexAppAutomationUpdate("", descriptor.Name)) {
			continue
		}

		name := descriptor.Name
		if forAntigravity {
			name = translatorcommon.AntigravityToolNameToUpstream(name)
		}

		itemDoc := ojson.NewDocString(`{"type":"function","name":""}`)
		_ = itemDoc.Set("name", name)
		desc := util.ResponsesToolDescription(descriptor.Tool)
		if applypatch.IsCustomTool(descriptor.Tool) {
			desc = applypatch.Description(descriptor.Tool)
		}
		if desc != "" {
			if forDevin {
				desc = translatorcommon.SanitizeDevinToolDescription(descriptor.Name, desc)
				if descriptor.LocalName != "" && descriptor.LocalName != descriptor.Name {
					desc = translatorcommon.SanitizeDevinToolDescription(descriptor.LocalName, desc)
				}
			}
			_ = itemDoc.Set("description", desc)
		}

		if applypatch.IsCustomTool(descriptor.Tool) {
			_ = itemDoc.SetRawBytes("parameters", applypatch.Parameters())
		} else if descriptor.ToolType == "custom" {
			_ = itemDoc.SetRaw("parameters", `{"type":"object","properties":{"input":{"type":"string"}},"required":["input"]}`)
		} else {
			params := util.ResponsesToolParameters(descriptor.Tool)
			if params.Exists() {
				_ = itemDoc.SetRaw("parameters", params.Raw)
			}
		}

		toolItems = append(toolItems, itemDoc.Bytes())
	}

	if len(toolItems) > 0 {
		_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
	}
}

func setInteractionsInputOnResponsesDoc(outDoc *ojson.Doc, input ojson.Result, forAntigravity bool) {
	items := make([][]byte, 0)
	if input.Type == ojson.String {
		items = append(items, interactionsTextMessage(input.String()))
	} else if input.IsArray() {
		input.ForEach(func(_, item ojson.Result) bool {
			if converted := interactionsInputItemToResponses(item, forAntigravity); converted != nil {
				items = append(items, converted)
			}
			return true
		})
	} else if input.IsObject() {
		if converted := interactionsInputItemToResponses(input, forAntigravity); converted != nil {
			items = append(items, converted)
		}
	}
	if len(items) > 0 {
		_ = outDoc.SetRawBytes("input", translatorcommon.JoinRawArray(items))
	}
}

func interactionsTextMessage(text string) []byte {
	item := []byte(`{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}`)
	item, _ = ojson.SetBytes(item, "content.0.text", text)
	return item
}

func interactionsInputItemToResponses(item ojson.Result, forAntigravity bool) []byte {
	switch item.Get("type").String() {
	case "user_input":
		return interactionsMessageToResponses(item, "user")
	case "model_output":
		return interactionsMessageToResponses(item, "assistant")
	case "thought":
		return interactionsThoughtToResponses(item)
	case "function_call":
		return interactionsFunctionCallToResponses(item, forAntigravity)
	case "function_result":
		return interactionsFunctionResultToResponses(item, forAntigravity)
	default:
		if item.Type == ojson.String {
			return interactionsTextMessage(item.String())
		}
	}
	return nil
}

func interactionsMessageToResponses(item ojson.Result, role string) []byte {
	var contentItems [][]byte
	content := item.Get("content")
	if content.Type == ojson.String {
		partType := "input_text"
		if role == "assistant" {
			partType = "output_text"
		}
		partDoc := ojson.NewDocString(`{"type":"","text":""}`)
		_ = partDoc.Set("type", partType)
		_ = partDoc.Set("text", content.String())
		contentItems = append(contentItems, partDoc.Bytes())
	} else {
		content.ForEach(func(_, part ojson.Result) bool {
			if converted, ok := interactionsContentPartToResponses(part, role); ok {
				contentItems = append(contentItems, converted)
			}
			return true
		})
	}
	outDoc := ojson.NewDocString(`{"type":"message","role":"","content":[]}`)
	_ = outDoc.Set("role", role)
	translatorcommon.SetRawArrayItemsDoc(outDoc, "content", contentItems)
	return outDoc.Bytes()
}

func interactionsThoughtToResponses(item ojson.Result) []byte {
	var summaryItems [][]byte
	for _, text := range interactionsContentTexts(item.Get("content")) {
		part := []byte(`{"type":"summary_text","text":""}`)
		part, _ = ojson.SetBytes(part, "text", text)
		summaryItems = append(summaryItems, part)
	}
	out := []byte(`{"type":"reasoning","summary":[]}`)
	out = translatorcommon.SetRawArrayItems(out, "summary", summaryItems)
	return out
}

func interactionsContentPartToResponses(part ojson.Result, role string) ([]byte, bool) {
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
		outDoc := ojson.NewDocString(`{"type":"","text":""}`)
		_ = outDoc.Set("type", outType)
		_ = outDoc.Set("text", part.Get("text").String())
		return outDoc.Bytes(), true
	case "image":
		outType := "input_image"
		if role == "assistant" {
			outType = "output_image"
		}
		outDoc := ojson.NewDocString(`{"type":""}`)
		_ = outDoc.Set("type", outType)
		imageURL := interactionsMediaDataURL(part)
		if imageURL != "" {
			_ = outDoc.Set("image_url", imageURL)
		}
		return outDoc.Bytes(), true
	case "audio":
		out := []byte(`{"type":"output_text","text":""}`)
		format := mediaFormat(part.Get("mime_type").String())
		out, _ = ojson.SetBytes(out, "text", "Audio content: inline data (Format: "+format+")")
		return out, true
	case "video", "document":
		outType := "input_file"
		if role == "assistant" {
			outType = "output_file"
		}
		outDoc := ojson.NewDocString(`{"type":""}`)
		_ = outDoc.Set("type", outType)
		if dataURL := interactionsMediaDataURL(part); dataURL != "" {
			_ = outDoc.Set("file_data", dataURL)
		}
		if filename := part.Get("filename").String(); filename != "" {
			_ = outDoc.Set("filename", filename)
		}
		return outDoc.Bytes(), true
	}
	return nil, false
}

func interactionsFunctionCallToResponsesWithIdentity(item ojson.Result, forAntigravity bool, toolIdentityMap map[string]util.ResponsesToolIdentity) []byte {
	rawName := item.Get("name").String()
	name := rawName
	var namespace string
	var isCustom bool
	if forAntigravity {
		name = translatorcommon.AntigravityUpstreamToolNameToClient(name)
	}
	if toolIdentityMap != nil {
		if identity, ok := toolIdentityMap[rawName]; ok {
			name = identity.Name
			namespace = identity.Namespace
			isCustom = identity.Custom
		} else if identity, ok := toolIdentityMap[name]; ok {
			name = identity.Name
			namespace = identity.Namespace
			isCustom = identity.Custom
		}
	}

	callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String())
	if isCustom {
		outDoc := ojson.NewDocString(`{"type":"custom_tool_call","call_id":"","name":"","input":""}`)
		if callID != "" {
			_ = outDoc.Set("call_id", callID)
		}
		if namespace != "" {
			_ = outDoc.Set("namespace", namespace)
		}
		_ = outDoc.Set("name", name)
		inputStr := util.UnwrapResponsesCustomToolInput(jsonStringValue(item.Get("arguments"), "{}"))
		_ = outDoc.Set("input", inputStr)
		return outDoc.Bytes()
	}

	outDoc := ojson.NewDocString(`{"type":"function_call","call_id":"","name":"","arguments":"{}"}`)
	if callID != "" {
		_ = outDoc.Set("call_id", callID)
	}
	if namespace != "" {
		_ = outDoc.Set("namespace", namespace)
	}
	_ = outDoc.Set("name", name)
	_ = translatorcommon.SetStringWithoutHTMLEscapeDoc(outDoc, "arguments", jsonStringValue(item.Get("arguments"), "{}"))
	return outDoc.Bytes()
}

func interactionsFunctionCallToResponses(item ojson.Result, forAntigravity bool) []byte {
	return interactionsFunctionCallToResponsesWithIdentity(item, forAntigravity, nil)
}

func interactionsFunctionResultToResponses(item ojson.Result, forAntigravity bool) []byte {
	outDoc := ojson.NewDocString(`{"type":"function_call_output","call_id":"","output":""}`)
	if callID := firstNonEmpty(item.Get("call_id").String(), item.Get("id").String()); callID != "" {
		_ = outDoc.Set("call_id", callID)
	}
	if name := item.Get("name").String(); name != "" {
		if forAntigravity {
			name = translatorcommon.AntigravityUpstreamToolNameToClient(name)
		}
		_ = outDoc.Set("name", name)
	}
	result := item.Get("result")
	if !result.Exists() {
		result = item.Get("output")
	}
	_ = outDoc.Set("output", jsonStringValue(result, ""))
	return outDoc.Bytes()
}

func appendInteractionsToolsToResponsesDoc(outDoc *ojson.Doc, tools ojson.Result, forAntigravity bool) {
	if !tools.Exists() || !tools.IsArray() {
		return
	}
	var toolItems [][]byte
	tools.ForEach(func(_, tool ojson.Result) bool {
		if converted, ok := responsesToolFromInteractionsTool(tool, forAntigravity); ok {
			toolItems = append(toolItems, converted)
		}
		if decls := tool.Get("function_declarations"); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl ojson.Result) bool {
				if converted, ok := responsesToolFromInteractionsTool(decl, forAntigravity); ok {
					toolItems = append(toolItems, converted)
				}
				return true
			})
		}
		return true
	})
	if len(toolItems) > 0 {
		_ = outDoc.SetRawBytes("tools", translatorcommon.JoinRawArray(toolItems))
	}
}

func responsesToolFromInteractionsTool(tool ojson.Result, forAntigravity bool) ([]byte, bool) {
	name := firstNonEmpty(tool.Get("name").String(), tool.Get("function.name").String())
	if name == "" {
		return nil, false
	}
	if forAntigravity {
		name = translatorcommon.AntigravityUpstreamToolNameToClient(name)
	}
	outDoc := ojson.NewDocString(`{"type":"function","name":""}`)
	_ = outDoc.Set("name", name)
	copyOptionalStringDoc(outDoc, "description", firstExisting(tool.Get("description"), tool.Get("function.description")))
	copyOptionalRawDoc(outDoc, "parameters", firstExisting(tool.Get("parameters"), tool.Get("function.parameters"), tool.Get("parametersJsonSchema")))
	return outDoc.Bytes(), true
}

func interactionsContentTexts(content ojson.Result) []string {
	texts := make([]string, 0)
	if content.Type == ojson.String {
		return append(texts, content.String())
	}
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if text := firstNonEmpty(part.Get("text").String(), part.Get("content.text").String()); text != "" {
				texts = append(texts, text)
			}
			return true
		})
	}
	return texts
}

func interactionsMediaDataURL(part ojson.Result) string {
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

func setJSONValueDoc(outDoc *ojson.Doc, path string, value ojson.Result, defaultRaw string) {
	if !value.Exists() {
		_ = outDoc.SetRaw(path, defaultRaw)
		return
	}
	if value.Type == ojson.String && ojson.Valid(value.String()) {
		_ = outDoc.SetRaw(path, value.String())
		return
	}
	if value.Type == ojson.String {
		_ = outDoc.Set(path, value.String())
		return
	}
	_ = outDoc.SetRaw(path, value.Raw)
}

func jsonStringValue(value ojson.Result, fallback string) string {
	if !value.Exists() {
		return fallback
	}
	if value.Type == ojson.String {
		return value.String()
	}
	return value.Raw
}

func copyOptionalStringDoc(outDoc *ojson.Doc, path string, value ojson.Result) {
	if value.Exists() {
		_ = outDoc.Set(path, value.String())
	}
}

func copyOptionalRawDoc(outDoc *ojson.Doc, path string, value ojson.Result) {
	if value.Exists() {
		_ = outDoc.SetRaw(path, value.Raw)
	}
}

func isAntigravityModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "antigravity")
}

func isDevinModel(model string) bool {
	clean := strings.ToLower(strings.TrimSpace(model))
	return strings.HasPrefix(clean, "devin/") || clean == "devin" || strings.Contains(clean, "devin")
}

func firstExisting(values ...ojson.Result) ojson.Result {
	for _, value := range values {
		if value.Exists() {
			return value
		}
	}
	return ojson.Result{}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}
