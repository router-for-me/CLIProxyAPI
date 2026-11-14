package interactions

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/MoeForge/moego/ojson"
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v8/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func ConvertInteractionsRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	return convertInteractionsRequestToAntigravity(modelName, inputRawJSON, stream)

}

// convertInteractionsRequestToAntigravity also reports a user turn that was left
// empty because its only attachment has no Antigravity equivalent.
func convertInteractionsRequestToAntigravity(modelName string, inputRawJSON []byte, stream bool) ([]byte, error) {
	root := util.ParseGJSONBytesNoCopy(inputRawJSON)
	functionNameMap := util.SanitizedFunctionNameMap(inputRawJSON)
	outDoc := ojson.NewDoc([]byte(`{"project":"","request":{"contents":[]},"model":""}`))
	_ = outDoc.Set("model", modelName)
	if stream || root.Get("stream").Bool() {
		_ = outDoc.Set("request.stream", true)
	}
	copyInteractionsSystemToAntigravityDoc(outDoc, root)
	copyInteractionsGenerationConfigToAntigravityDoc(outDoc, root)
	contentItems := translatorcommon.NewRawArrayItems(root.Get("input.#").Int())
	errInput := appendInteractionsInputToAntigravity(&contentItems, root.Get("input"))
	translatorcommon.SetRawArrayItemsDoc(outDoc, "request.contents", contentItems)
	copyInteractionsToolsToAntigravityDoc(outDoc, root, functionNameMap)
	if outDoc.Get("request.toolConfig.functionCallingConfig.mode").String() == "NONE" {
		_ = outDoc.Delete("request.tools")
	}
	attachDefaultAntigravitySafetySettingsDoc(outDoc)
	out := rewriteInteractionsFunctionNames(outDoc.Bytes(), functionNameMap)
	return out, errInput
}

func rewriteInteractionsFunctionNames(out []byte, functionNameMap map[string]string) []byte {
	var outDoc *ojson.Doc
	ensureOutDoc := func() *ojson.Doc {
		if outDoc == nil {
			outDoc = ojson.NewDoc(out)
		}
		return outDoc
	}
	contents := ojson.GetBytesNoCopy(out, "request.contents")
	canBatchContents := contents.IsArray()
	if canBatchContents {
		contents.ForEach(func(_, content ojson.Result) bool {
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
		contents.ForEach(func(_, content ojson.Result) bool {
			contentJSON := []byte(content.Raw)
			partsChanged := false
			partItems := make([][]byte, 0, 4)
			content.Get("parts").ForEach(func(_, part ojson.Result) bool {
				var partDoc *ojson.Doc
				for _, field := range []string{"functionCall", "functionResponse"} {
					nameResult := part.Get(field + ".name")
					name := nameResult.String()
					if name == "" {
						continue
					}
					mappedName := util.MapSanitizedFunctionName(functionNameMap, name)
					if nameResult.Type == ojson.String && mappedName == name {
						continue
					}
					if partDoc == nil {
						partDoc = ojson.NewDocString(part.Raw)
					}
					_ = partDoc.Set(field+".name", mappedName)
					partsChanged = true
				}
				if partDoc != nil {
					partItems = append(partItems, partDoc.Bytes())
				} else {
					partItems = append(partItems, []byte(part.Raw))
				}
				return true
			})
			if partsChanged {
				contentJSON, _ = ojson.SetRawBytes(contentJSON, "parts", translatorcommon.JoinRawArray(partItems))
				contentsChanged = true
			}
			contentItems = append(contentItems, contentJSON)
			return true
		})
		if contentsChanged {
			_ = ensureOutDoc().SetRawBytes("request.contents", translatorcommon.JoinRawArray(contentItems))
		}
	} else {
		for contentIndex, content := range contents.Array() {
			for partIndex, part := range content.Get("parts").Array() {
				for _, field := range []string{"functionCall", "functionResponse"} {
					nameResult := part.Get(field + ".name")
					name := nameResult.String()
					if name == "" {
						continue
					}
					mappedName := util.MapSanitizedFunctionName(functionNameMap, name)
					if nameResult.Type == ojson.String && mappedName == name {
						continue
					}
					path := fmt.Sprintf("request.contents.%d.parts.%d.%s.name", contentIndex, partIndex, field)
					_ = ensureOutDoc().Set(path, mappedName)
				}
			}
		}
	}

	allowedPath := "request.toolConfig.functionCallingConfig.allowedFunctionNames"
	allowedNames := ojson.GetBytesNoCopy(out, allowedPath)
	if allowedNames.IsArray() {
		namesChanged := false
		nameItems := make([][]byte, 0, 4)
		allowedNames.ForEach(func(_, name ojson.Result) bool {
			mappedName := util.MapSanitizedFunctionName(functionNameMap, name.String())
			namesChanged = namesChanged || name.Type != ojson.String || mappedName != name.String()
			mappedNameJSON, _ := json.Marshal(mappedName)
			nameItems = append(nameItems, mappedNameJSON)
			return true
		})
		if namesChanged {
			_ = ensureOutDoc().SetRawBytes(allowedPath, translatorcommon.JoinRawArray(nameItems))
		}
	} else {
		for index, name := range allowedNames.Array() {
			mappedName := util.MapSanitizedFunctionName(functionNameMap, name.String())
			if name.Type == ojson.String && mappedName == name.String() {
				continue
			}
			path := fmt.Sprintf("%s.%d", allowedPath, index)
			_ = ensureOutDoc().Set(path, mappedName)
		}
	}
	if outDoc != nil {
		return outDoc.Bytes()
	}
	return out
}

func copyInteractionsSystemToAntigravityDoc(outDoc *ojson.Doc, root ojson.Result) {
	sys := root.Get("system_instruction")
	if !sys.Exists() {
		return
	}
	if sys.Type == ojson.String {
		instr, _ := ojson.SetBytes([]byte(`{"parts":[{"text":""}]}`), "parts.0.text", sys.String())
		_ = outDoc.SetRawBytes("request.systemInstruction", instr)
		return
	}
	if text := sys.Get("text"); text.Exists() && !sys.Get("parts").Exists() {
		instr, _ := ojson.SetBytes([]byte(`{"parts":[{"text":""}]}`), "parts.0.text", text.String())
		_ = outDoc.SetRawBytes("request.systemInstruction", instr)
		return
	}
	_ = outDoc.SetRaw("request.systemInstruction", sys.Raw)
}

func copyInteractionsGenerationConfigToAntigravityDoc(outDoc *ojson.Doc, root ojson.Result) {
	if cfg := root.Get("generation_config"); cfg.Exists() {
		_ = outDoc.SetRawBytes("request.generationConfig", convertSnakeCaseKeysToCamelCaseForAntigravity([]byte(cfg.Raw)))
	} else if cfg := root.Get("generationConfig"); cfg.Exists() {
		_ = outDoc.SetRaw("request.generationConfig", cfg.Raw)
	}
	normalizeInteractionsGenerationConfigForAntigravityDoc(outDoc)
	copyInteractionsReasoningToAntigravityDoc(outDoc, root)
	copyInteractionsResponseModalitiesToAntigravityDoc(outDoc, root)
	copyInteractionsToolChoiceToAntigravityDoc(outDoc, root)
}

func normalizeInteractionsGenerationConfigForAntigravityDoc(outDoc *ojson.Doc) {
	if thinkingLevel := outDoc.Get("request.generationConfig.thinkingLevel"); thinkingLevel.Exists() {
		_ = outDoc.SetRaw("request.generationConfig.thinkingConfig.thinkingLevel", thinkingLevel.Raw)
		_ = outDoc.Delete("request.generationConfig.thinkingLevel")
	}
	if thinkingBudget := outDoc.Get("request.generationConfig.thinkingBudget"); thinkingBudget.Exists() {
		_ = outDoc.SetRaw("request.generationConfig.thinkingConfig.thinkingBudget", thinkingBudget.Raw)
		_ = outDoc.Delete("request.generationConfig.thinkingBudget")
	}
	if includeThoughts := outDoc.Get("request.generationConfig.includeThoughts"); includeThoughts.Exists() {
		_ = outDoc.SetRaw("request.generationConfig.thinkingConfig.includeThoughts", includeThoughts.Raw)
		_ = outDoc.Delete("request.generationConfig.includeThoughts")
	}
	if summaries := outDoc.Get("request.generationConfig.thinkingSummaries"); summaries.Exists() {
		if includeThoughts, ok := antigravityThinkingSummariesIncludeThoughts(summaries); ok {
			_ = outDoc.Set("request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		}
		_ = outDoc.Delete("request.generationConfig.thinkingSummaries")
	}
	if toolChoice := outDoc.Get("request.generationConfig.toolChoice"); toolChoice.Exists() {
		_ = outDoc.Delete("request.generationConfig.toolChoice")
	}
}

func copyInteractionsReasoningToAntigravityDoc(outDoc *ojson.Doc, root ojson.Result) {
	reasoning := root.Get("reasoning")
	if !reasoning.Exists() {
		return
	}
	effort := strings.ToLower(strings.TrimSpace(reasoning.Get("effort").String()))
	if effort == "" {
		effort = strings.ToLower(strings.TrimSpace(reasoning.Get("thinking_level").String()))
	}
	if effort != "" {
		// Thinking amount and summary visibility are independent. This OpenAI-style
		// compatibility alias controls only the amount; includeThoughts is written
		// below only for an explicit Interactions summary selector.
		if effort == "auto" {
			_ = outDoc.Set("request.generationConfig.thinkingConfig.thinkingBudget", -1)
		} else {
			_ = outDoc.Set("request.generationConfig.thinkingConfig.thinkingLevel", effort)
		}
	}
	if summary := reasoning.Get("summary"); summary.Exists() {
		if includeThoughts, ok := antigravityThinkingSummariesIncludeThoughts(summary); ok {
			_ = outDoc.Set("request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		}
	}
}

func copyInteractionsResponseModalitiesToAntigravityDoc(outDoc *ojson.Doc, root ojson.Result) {
	mods := root.Get("response_modalities")
	if !mods.Exists() {
		mods = root.Get("responseModalities")
	}
	if !mods.Exists() || !mods.IsArray() {
		return
	}
	var responseMods []string
	mods.ForEach(func(_, mod ojson.Result) bool {
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
		_ = outDoc.Set("request.generationConfig.responseModalities", responseMods)
	}
}

func copyInteractionsToolChoiceToAntigravityDoc(outDoc *ojson.Doc, root ojson.Result) {
	toolChoice := root.Get("tool_choice")
	if !toolChoice.Exists() {
		toolChoice = root.Get("generation_config.tool_choice")
	}
	if !toolChoice.Exists() {
		toolChoice = root.Get("generationConfig.toolChoice")
	}
	if !toolChoice.Exists() {
		return
	}
	mode := ""
	var allowedNames []string
	if toolChoice.Type == ojson.String {
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
		return
	}
	_ = outDoc.Set("request.toolConfig.functionCallingConfig.mode", mode)
	if len(allowedNames) > 0 {
		_ = outDoc.Set("request.toolConfig.functionCallingConfig.allowedFunctionNames", allowedNames)
	}
}

type antigravityInteractionsInputContext struct {
	items            [][]byte
	inModelTurn      bool
	lastStepType     string
	pendingSignature string
	// run follows the consecutive user content so that an attachment Antigravity
	// cannot carry is refused only when its whole user turn is left with nothing to send.
	run translatorcommon.UserRun
	// instruction is set while the step being added is developer or system content.
	instruction bool
}

// userRun returns the tracker for content added with role. Model content and
// developer or system content close the open user turn and have no tracker, so
// they can never keep an emptied user turn alive.
func (ctx *antigravityInteractionsInputContext) userRun(role string) *translatorcommon.UserRun {
	if role == "user" && !ctx.instruction {
		return &ctx.run
	}
	ctx.run.End()
	return nil
}

// appendText adds a text content; blank text does not keep a user turn alive.
func (ctx *antigravityInteractionsInputContext) appendText(role, text string) {
	appendAntigravityTextContent(&ctx.items, role, text)
	run := ctx.userRun(role)
	if strings.TrimSpace(text) != "" {
		run.Add()
	}
}

func appendInteractionsInputToAntigravity(items *[][]byte, input ojson.Result) error {
	if !input.Exists() {
		return nil
	}
	ctx := &antigravityInteractionsInputContext{
		items: *items,
	}
	if input.Type == ojson.String {
		appendAntigravityTextContent(&ctx.items, "user", input.String())
		ctx.lastStepType = "text"
		*items = ctx.items
		return nil
	}
	if input.IsArray() {
		input.ForEach(func(_, item ojson.Result) bool {
			if !appendInteractionsContentBlockItem(ctx, item) {
				appendInteractionsStepToAntigravity(ctx, item, "user")
			}
			return true
		})
	} else if steps := input.Get("steps"); steps.Exists() && steps.IsArray() {
		defaultRole := "user"
		if role := input.Get("role").String(); role == "model" || role == "assistant" {
			defaultRole = "model"
		}
		ctx.instruction = translatorcommon.IsInteractionsInstructionStep(input, false)
		steps.ForEach(func(_, step ojson.Result) bool {
			appendInteractionsStepToAntigravity(ctx, step, defaultRole)
			return true
		})
		ctx.instruction = false
	} else {
		appendInteractionsStepToAntigravity(ctx, input, "user")
	}
	flushPendingAntigravitySignature(ctx)
	*items = ctx.items
	ctx.run.End()
	return ctx.run.Err()
}

// appendInteractionsContentBlockItem accepts the shorthand where input is a bare
// list of content blocks instead of steps. Those items have no step envelope and
// would otherwise be dropped, losing images and files sent that way.
func appendInteractionsContentBlockItem(ctx *antigravityInteractionsInputContext, item ojson.Result) bool {
	switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
	case "text", "image", "audio", "video", "document":
	default:
		return false
	}
	if item.Get("content").Exists() || item.Get("steps").Exists() || item.Get("parts").Exists() {
		return false
	}
	// Steps carrying an explicit role keep the step dispatch so that system or
	// developer content still closes the open user turn instead of masking a
	// dropped attachment.
	if item.Get("role").Exists() {
		return false
	}
	if ctx.inModelTurn {
		flushPendingAntigravitySignature(ctx)
		ctx.inModelTurn = false
	}
	appendInteractionsContentPartToAntigravity(&ctx.items, "user", item, ctx.userRun("user"))
	ctx.lastStepType = "user_input"
	return true
}

func appendInteractionsStepToAntigravity(ctx *antigravityInteractionsInputContext, step ojson.Result, defaultRole string) {
	inheritedInstruction := ctx.instruction
	ctx.instruction = translatorcommon.IsInteractionsInstructionStep(step, inheritedInstruction)
	defer func() { ctx.instruction = inheritedInstruction }()
	if step.Type == ojson.String {
		if ctx.inModelTurn {
			flushPendingAntigravitySignature(ctx)
			ctx.inModelTurn = false
		}
		ctx.appendText(defaultRole, step.String())
		ctx.lastStepType = "text"
		return
	}
	if steps := step.Get("steps"); steps.Exists() && steps.IsArray() {
		role := defaultRole
		if itemRole := step.Get("role").String(); itemRole == "model" || itemRole == "assistant" {
			role = "model"
		} else if itemRole == "user" {
			role = "user"
		}
		steps.ForEach(func(_, child ojson.Result) bool {
			appendInteractionsStepToAntigravity(ctx, child, role)
			return true
		})
		return
	}
	switch step.Get("type").String() {
	case "model_output":
		ctx.run.End()
		if ctx.pendingSignature != "" {
			carrier := antigravityTextPartJSON("", false)
			carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
			ctx.pendingSignature = ""
			if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
				ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
			} else {
				ctx.items = append(ctx.items, antigravityContent("model", [][]byte{carrier}))
			}
		}
		partItems := extractInteractionsStepContentPartsToAntigravity(step, false)
		if len(partItems) > 0 {
			if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
				ctx.items[len(ctx.items)-1] = appendGeminiContentParts(ctx.items[len(ctx.items)-1], partItems)
			} else {
				ctx.items = append(ctx.items, antigravityContent("model", partItems))
			}
		}
		ctx.inModelTurn = true
		ctx.lastStepType = "model_output"
	case "thought":
		ctx.run.End()
		if sig := firstNonEmptyString(step.Get("signature").String(), step.Get("thought_signature").String(), step.Get("thoughtSignature").String()); sig != "" {
			if ctx.pendingSignature != "" && ctx.pendingSignature != sig {
				carrier := antigravityTextPartJSON("", false)
				carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
				if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
					ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
				} else {
					ctx.items = append(ctx.items, antigravityContent("model", [][]byte{carrier}))
				}
			}
			ctx.pendingSignature = sig
		}
		partItems := extractInteractionsThoughtPartsToAntigravity(step)
		if len(partItems) > 0 {
			if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
				ctx.items[len(ctx.items)-1] = appendGeminiContentParts(ctx.items[len(ctx.items)-1], partItems)
			} else {
				ctx.items = append(ctx.items, antigravityContent("model", partItems))
			}
		}
		ctx.inModelTurn = true
		ctx.lastStepType = "thought"
	case "function_call":
		ctx.run.End()
		part := buildAntigravityFunctionCallPart(step)
		sig := firstNonEmptyString(step.Get("signature").String(), step.Get("thought_signature").String(), step.Get("thoughtSignature").String())
		if sig == "" && ctx.pendingSignature != "" {
			sig = ctx.pendingSignature
			ctx.pendingSignature = ""
		} else if sig != "" && ctx.pendingSignature != "" {
			if ctx.pendingSignature == sig {
				ctx.pendingSignature = ""
			} else {
				carrier := antigravityTextPartJSON("", false)
				carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
				ctx.pendingSignature = ""
				if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
					ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
				} else {
					ctx.items = append(ctx.items, antigravityContent("model", [][]byte{carrier}))
				}
			}
		}
		if sig != "" {
			part, _ = ojson.SetBytes(part, "thoughtSignature", sig)
		}
		if ctx.inModelTurn && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
			ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], part)
		} else {
			ctx.items = append(ctx.items, antigravityContent("model", [][]byte{part}))
		}
		ctx.inModelTurn = true
		ctx.lastStepType = "function_call"
	case "function_result":
		if ctx.inModelTurn {
			flushPendingAntigravitySignature(ctx)
			ctx.inModelTurn = false
		}
		part := buildAntigravityFunctionResultPart(step)
		// A tool result is content the model reads, so it keeps the surrounding user turn.
		ctx.run.Add()
		if ctx.lastStepType == "function_result" && len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "user" {
			ctx.items[len(ctx.items)-1] = appendGeminiUserContentPart(ctx.items[len(ctx.items)-1], part)
		} else {
			ctx.items = append(ctx.items, antigravityContent("user", [][]byte{part}))
		}
		ctx.lastStepType = "function_result"
	case "user_input", "":
		if ctx.inModelTurn {
			flushPendingAntigravitySignature(ctx)
			ctx.inModelTurn = false
		}
		if step.Get("parts").Exists() {
			appendInteractionsNativeContentToAntigravity(ctx, step, defaultRole)
		} else {
			appendInteractionsContentListToAntigravity(ctx, defaultRole, step.Get("content"))
		}
		ctx.lastStepType = "user_input"
	default:
		if ctx.inModelTurn {
			flushPendingAntigravitySignature(ctx)
			ctx.inModelTurn = false
		}
		if step.Get("parts").Exists() {
			appendInteractionsNativeContentToAntigravity(ctx, step, defaultRole)
		} else if step.Get("content").Exists() {
			appendInteractionsContentListToAntigravity(ctx, defaultRole, step.Get("content"))
		} else if text := step.Get("text"); text.Exists() {
			ctx.appendText(defaultRole, text.String())
		}
		ctx.lastStepType = "default"
	}
}

func flushPendingAntigravitySignature(ctx *antigravityInteractionsInputContext) {
	if ctx.pendingSignature == "" {
		return
	}
	carrier := antigravityTextPartJSON("", false)
	carrier, _ = ojson.SetBytes(carrier, "thoughtSignature", ctx.pendingSignature)
	ctx.pendingSignature = ""
	if len(ctx.items) > 0 && ojson.GetBytesNoCopy(ctx.items[len(ctx.items)-1], "role").String() == "model" {
		ctx.items[len(ctx.items)-1] = appendGeminiContentPart(ctx.items[len(ctx.items)-1], carrier)
	} else {
		ctx.items = append(ctx.items, antigravityContent("model", [][]byte{carrier}))
	}
}

func extractInteractionsThoughtPartsToAntigravity(step ojson.Result) [][]byte {
	content := step.Get("content")
	if !content.Exists() {
		content = step.Get("summary")
	}
	if !content.Exists() {
		content = step.Get("text")
	}
	if !content.Exists() {
		return nil
	}
	var partItems [][]byte
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if partJSON := appendInteractionsContentToAntigravityPart(nil, part, true); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := appendInteractionsContentToAntigravityPart(nil, content, true); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == ojson.String {
		partItems = append(partItems, antigravityTextPartJSON(content.String(), true))
	}
	return partItems
}

func extractInteractionsStepContentPartsToAntigravity(step ojson.Result, thought bool) [][]byte {
	content := step.Get("content")
	if !content.Exists() {
		content = step.Get("text")
	}
	if !content.Exists() {
		return nil
	}
	var partItems [][]byte
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if partJSON := appendInteractionsContentToAntigravityPart(nil, part, thought); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := appendInteractionsContentToAntigravityPart(nil, content, thought); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == ojson.String {
		partItems = append(partItems, antigravityTextPartJSON(content.String(), thought))
	}
	return partItems
}

func appendGeminiContentPart(content []byte, part []byte) []byte {
	parts := ojson.GetBytesNoCopy(content, "parts").Array()
	rawParts := make([][]byte, 0, len(parts)+1)
	for _, p := range parts {
		rawParts = append(rawParts, []byte(p.Raw))
	}
	rawParts = append(rawParts, part)
	updated, err := ojson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(rawParts))
	if err != nil {
		return content
	}
	return updated
}

func appendGeminiContentParts(content []byte, newParts [][]byte) []byte {
	if len(newParts) == 0 {
		return content
	}
	parts := ojson.GetBytesNoCopy(content, "parts").Array()
	rawParts := make([][]byte, 0, len(parts)+len(newParts))
	for _, p := range parts {
		rawParts = append(rawParts, []byte(p.Raw))
	}
	rawParts = append(rawParts, newParts...)
	updated, err := ojson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(rawParts))
	if err != nil {
		return content
	}
	return updated
}

func appendGeminiUserContentPart(content []byte, part []byte) []byte {
	parts := ojson.GetBytesNoCopy(content, "parts").Array()
	rawParts := make([][]byte, 0, len(parts)+1)
	for _, p := range parts {
		rawParts = append(rawParts, []byte(p.Raw))
	}
	rawParts = append(rawParts, part)
	rawParts = translatorcommon.ReorderGeminiUserParts(rawParts)
	updated, err := ojson.SetRawBytes(content, "parts", translatorcommon.JoinRawArray(rawParts))
	if err != nil {
		return content
	}
	return updated
}

func firstNonEmptyString(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func buildAntigravityFunctionCallPart(step ojson.Result) []byte {
	partDoc := ojson.NewDoc([]byte(`{"functionCall":{"name":"","args":{}}}`))
	_ = partDoc.Set("functionCall.name", step.Get("name").String())
	if callID := step.Get("call_id"); callID.Exists() {
		_ = partDoc.Set("functionCall.id", callID.String())
	} else if id := step.Get("id"); id.Exists() {
		_ = partDoc.Set("functionCall.id", id.String())
	}
	if args := step.Get("arguments"); args.Exists() {
		_ = partDoc.SetRaw("functionCall.args", args.Raw)
	}
	return partDoc.Bytes()
}

func buildAntigravityFunctionResultPart(step ojson.Result) []byte {
	partDoc := ojson.NewDoc([]byte(`{"functionResponse":{"name":"","response":{}}}`))
	_ = partDoc.Set("functionResponse.name", step.Get("name").String())
	if callID := step.Get("call_id"); callID.Exists() {
		_ = partDoc.Set("functionResponse.id", callID.String())
	} else if id := step.Get("id"); id.Exists() {
		_ = partDoc.Set("functionResponse.id", id.String())
	}
	if result := step.Get("result"); result.Exists() {
		translatorcommon.SetGeminiFunctionResponseResultDoc(partDoc, "functionResponse.response", result)
	}
	return partDoc.Bytes()
}

func appendInteractionsNativeContentToAntigravity(ctx *antigravityInteractionsInputContext, step ojson.Result, defaultRole string) {
	parts := step.Get("parts")
	if !parts.Exists() || !parts.IsArray() {
		return
	}
	role := antigravityContentRole(step.Get("role").String(), defaultRole)
	run := ctx.userRun(role)
	partItems := make([][]byte, 0, 4)
	parts.ForEach(func(_, part ojson.Result) bool {
		partJSON := interactionsNativeAntigravityPart(part)
		if len(partJSON) == 0 {
			if droppedType := translatorcommon.InteractionsAttachmentType(part); droppedType != "" {
				run.Drop(droppedType)
			}
			return true
		}
		partItems = append(partItems, partJSON)
		if translatorcommon.GeminiPartIsSendable(partJSON) {
			run.Add()
		}
		return true
	})
	if len(partItems) > 0 {
		ctx.items = append(ctx.items, antigravityContent(role, partItems))
	}
}

func appendInteractionsStepContentToAntigravity(items *[][]byte, role string, step ojson.Result, thought bool) {
	content := step.Get("content")
	if !content.Exists() {
		return
	}
	partItems := make([][]byte, 0, 4)
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			if partJSON := appendInteractionsContentToAntigravityPart(nil, part, thought); len(partJSON) > 0 {
				partItems = append(partItems, partJSON)
			}
			return true
		})
	} else if content.IsObject() {
		if partJSON := appendInteractionsContentToAntigravityPart(nil, content, thought); len(partJSON) > 0 {
			partItems = append(partItems, partJSON)
		}
	} else if content.Type == ojson.String {
		partItems = append(partItems, antigravityTextPartJSON(content.String(), thought))
	}
	if len(partItems) > 0 {
		*items = append(*items, antigravityContent(role, partItems))
	}
}

func appendInteractionsContentListToAntigravity(ctx *antigravityInteractionsInputContext, role string, content ojson.Result) {
	if !content.Exists() {
		return
	}
	run := ctx.userRun(role)
	if content.IsArray() {
		content.ForEach(func(_, part ojson.Result) bool {
			appendInteractionsContentPartToAntigravity(&ctx.items, role, part, run)
			return true
		})
		return
	}
	if content.IsObject() {
		appendInteractionsContentPartToAntigravity(&ctx.items, role, content, run)
	} else if content.Type == ojson.String {
		ctx.appendText(role, content.String())
	}
}

// appendInteractionsContentPartToAntigravity adds one content part. run is nil for
// model content; for user content it records what is sent and what cannot be.
func appendInteractionsContentPartToAntigravity(items *[][]byte, role string, part ojson.Result, run *translatorcommon.UserRun) {
	partJSON := appendInteractionsContentToAntigravityPart(nil, part, false)
	if len(partJSON) == 0 {
		if droppedType := translatorcommon.InteractionsAttachmentType(part); droppedType != "" {
			run.Drop(droppedType)
		}
		return
	}
	*items = append(*items, antigravityContent(role, [][]byte{partJSON}))
	if translatorcommon.GeminiPartIsSendable(partJSON) {
		run.Add()
	}
}

func appendInteractionsContentToAntigravityPart(_ []byte, content ojson.Result, thought bool) []byte {
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
				return antigravityInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
			}
		}
		// uri is the Interactions spelling of file_uri.
		if fileURI := firstNonEmptyString(content.Get("file_uri").String(), content.Get("fileUri").String(), content.Get("uri").String()); fileURI != "" {
			mimeType := content.Get("mime_type").String()
			if mimeType == "" {
				mimeType = content.Get("mimeType").String()
			}
			return antigravityFileDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mimeType":%q,"fileUri":%q}`, mimeType, fileURI)))
		}
		if url := content.Get("url"); url.Exists() {
			return antigravityInlineDataPartFromDataURL(url.String())
		}
	case "image_url":
		return antigravityInlineDataPartFromDataURL(content.Get("image_url.url").String())
	case "input_audio":
		mimeType := antigravityInputAudioMimeType(content.Get("input_audio.format").String())
		return antigravityInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, content.Get("input_audio.data").String())))
	case "file":
		filename := content.Get("file.filename").String()
		fileData := content.Get("file.file_data").String()
		if mimeType, data, ok := translatorcommon.NormalizeOpenAIFileData(filename, "", fileData); ok {
			return antigravityInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, mimeType, data)))
		}
	}
	return nil
}

func appendInteractionsFunctionCallToAntigravity(items *[][]byte, step ojson.Result) {
	part := buildAntigravityFunctionCallPart(step)
	*items = append(*items, antigravityContent("model", [][]byte{part}))
}

func appendInteractionsFunctionResultToAntigravity(items *[][]byte, step ojson.Result) {
	part := buildAntigravityFunctionResultPart(step)
	*items = append(*items, antigravityContent("user", [][]byte{part}))
}

func copyInteractionsToolsToAntigravityDoc(outDoc *ojson.Doc, root ojson.Result, functionNameMap map[string]string) {
	if outDoc.Get("request.toolConfig.functionCallingConfig.mode").String() == "NONE" {
		_ = outDoc.Delete("request.tools")
		return
	}
	tools := root.Get("tools")
	if !tools.Exists() {
		return
	}
	if !tools.IsArray() {
		_ = outDoc.SetRaw("request.tools", tools.Raw)
		return
	}
	var functionDeclarations [][]byte
	var otherTools [][]byte
	tools.ForEach(func(_, tool ojson.Result) bool {
		if decls := tool.Get("functionDeclarations"); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl ojson.Result) bool {
				if converted := antigravityFunctionDeclarationJSON(decl, functionNameMap); len(converted) > 0 {
					functionDeclarations = append(functionDeclarations, converted)
				}
				return true
			})
			return true
		}
		if decls := tool.Get("function_declarations"); decls.Exists() && decls.IsArray() {
			decls.ForEach(func(_, decl ojson.Result) bool {
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
		toolType := tool.Get("type").String()
		switch toolType {
		case "url_context":
			node := []byte(`{"urlContext":{}}`)
			if uc := tool.Get("url_context"); uc.Exists() && uc.IsObject() {
				node, _ = ojson.SetRawBytes(node, "urlContext", []byte(uc.Raw))
			} else if uc := tool.Get("urlContext"); uc.Exists() && uc.IsObject() {
				node, _ = ojson.SetRawBytes(node, "urlContext", []byte(uc.Raw))
			}
			otherTools = append(otherTools, node)
			return true
		case "code_execution":
			node := []byte(`{"codeExecution":{}}`)
			if ce := tool.Get("code_execution"); ce.Exists() && ce.IsObject() {
				node, _ = ojson.SetRawBytes(node, "codeExecution", []byte(ce.Raw))
			} else if ce := tool.Get("codeExecution"); ce.Exists() && ce.IsObject() {
				node, _ = ojson.SetRawBytes(node, "codeExecution", []byte(ce.Raw))
			}
			otherTools = append(otherTools, node)
			return true
		case "google_search", "web_search":
			node := []byte(`{"googleSearch":{}}`)
			if gs := tool.Get("google_search"); gs.Exists() && gs.IsObject() {
				node, _ = ojson.SetRawBytes(node, "googleSearch", []byte(gs.Raw))
			} else if gs := tool.Get("googleSearch"); gs.Exists() && gs.IsObject() {
				node, _ = ojson.SetRawBytes(node, "googleSearch", []byte(gs.Raw))
			}
			otherTools = append(otherTools, node)
			return true
		}
		if toolType == "" {
			rawDoc := ojson.NewDocString(tool.Raw)
			if uc := tool.Get("url_context"); uc.Exists() {
				_ = rawDoc.SetRaw("urlContext", uc.Raw)
				_ = rawDoc.Delete("url_context")
			}
			if ce := tool.Get("code_execution"); ce.Exists() {
				_ = rawDoc.SetRaw("codeExecution", ce.Raw)
				_ = rawDoc.Delete("code_execution")
			}
			if gs := tool.Get("google_search"); gs.Exists() {
				_ = rawDoc.SetRaw("googleSearch", gs.Raw)
				_ = rawDoc.Delete("google_search")
			}
			if ws := tool.Get("web_search"); ws.Exists() {
				_ = rawDoc.SetRaw("googleSearch", ws.Raw)
				_ = rawDoc.Delete("web_search")
			}
			otherTools = append(otherTools, rawDoc.Bytes())
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
			functionToolNode, _ = ojson.SetRawBytes(functionToolNode, "functionDeclarations", deduplicated)
			toolItems = append(toolItems, functionToolNode)
		}
		toolItems = append(toolItems, otherTools...)
		_ = outDoc.SetRawBytes("request.tools", translatorcommon.JoinRawArray(toolItems))
	}
}

func antigravityFunctionDeclarationJSON(decl ojson.Result, functionNameMap map[string]string) []byte {
	fn := decl
	if nested := decl.Get("function"); nested.Exists() && nested.IsObject() {
		fn = nested
	}
	name := fn.Get("name").String()
	if strings.TrimSpace(name) == "" {
		return nil
	}
	outDoc := ojson.NewDoc([]byte(`{"name":"","parametersJsonSchema":{"type":"object","properties":{}}}`))
	_ = outDoc.Set("name", util.MapSanitizedFunctionName(functionNameMap, name))
	if desc := fn.Get("description"); desc.Exists() {
		_ = outDoc.Set("description", desc.String())
	}
	if params := fn.Get("parametersJsonSchema"); params.Exists() {
		_ = outDoc.SetRaw("parametersJsonSchema", params.Raw)
	} else if params := fn.Get("parameters"); params.Exists() {
		_ = outDoc.SetRaw("parametersJsonSchema", params.Raw)
	}
	if response := fn.Get("response"); response.Exists() {
		_ = outDoc.SetRaw("response", response.Raw)
	}
	if responseSchema := fn.Get("responseJsonSchema"); responseSchema.Exists() {
		_ = outDoc.SetRaw("responseJsonSchema", responseSchema.Raw)
	}
	return outDoc.Bytes()
}

func interactionsNativeAntigravityPart(part ojson.Result) []byte {
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
	partDoc := ojson.NewDoc([]byte(`{"text":""}`))
	_ = partDoc.Set("text", text)
	if thought {
		_ = partDoc.Set("thought", true)
	}
	return partDoc.Bytes()
}

func antigravityInlineDataPartJSON(inline ojson.Result) []byte {
	mimeType := inline.Get("mimeType").String()
	if mimeType == "" {
		mimeType = inline.Get("mime_type").String()
	}
	data := inline.Get("data").String()
	if mimeType == "" || data == "" {
		return nil
	}
	partDoc := ojson.NewDoc([]byte(`{"inlineData":{"mimeType":"","data":""}}`))
	_ = partDoc.Set("inlineData.mimeType", mimeType)
	_ = partDoc.Set("inlineData.data", data)
	return partDoc.Bytes()
}

func antigravityFileDataPartJSON(fileData ojson.Result) []byte {
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
	partDoc := ojson.NewDoc([]byte(`{"fileData":{"mimeType":"","fileUri":""}}`))
	_ = partDoc.Set("fileData.mimeType", mimeType)
	_ = partDoc.Set("fileData.fileUri", fileURI)
	return partDoc.Bytes()
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
	return antigravityInlineDataPartJSON(ojson.Parse(fmt.Sprintf(`{"mime_type":%q,"data":%q}`, pieces[0], pieces[1][7:])))
}

func appendAntigravityTextContent(items *[][]byte, role, text string) {
	part := antigravityTextPartJSON(text, false)
	*items = append(*items, antigravityContent(antigravityContentRole(role, "user"), [][]byte{part}))
}

func antigravityContent(role string, parts [][]byte) []byte {
	contentDoc := ojson.NewDoc([]byte(`{"role":"","parts":[]}`))
	_ = contentDoc.Set("role", role)
	_ = contentDoc.SetRawBytes("parts", translatorcommon.JoinRawArray(parts))
	return contentDoc.Bytes()
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

func antigravityThinkingSummariesIncludeThoughts(summary ojson.Result) (bool, bool) {
	if summary.Type != ojson.String {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(summary.String())) {
	case "auto":
		return true, true
	case "none":
		return false, true
	default:
		return false, false
	}
}

func convertSnakeCaseKeysToCamelCaseForAntigravity(raw []byte) []byte {
	root := util.ParseGJSONBytesNoCopy(raw)
	if !root.Exists() {
		return raw
	}
	outDoc := ojson.NewDoc([]byte(`{}`))
	copySnakeCaseValueToCamelCaseForAntigravityDoc(outDoc, "", root)
	return outDoc.Bytes()
}

func copySnakeCaseValueToCamelCaseForAntigravityDoc(outDoc *ojson.Doc, path string, node ojson.Result) {
	if node.IsObject() {
		node.ForEach(func(key, value ojson.Result) bool {
			childPath := joinAntigravityJSONPath(path, toAntigravityCamelCase(key.String()))
			copySnakeCaseValueToCamelCaseForAntigravityDoc(outDoc, childPath, value)
			return true
		})
		return
	}
	if node.IsArray() {
		node.ForEach(func(_, value ojson.Result) bool {
			copySnakeCaseValueToCamelCaseForAntigravityDoc(outDoc, path+".-1", value)
			return true
		})
		return
	}
	_ = outDoc.SetRaw(path, node.Raw)
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

func attachDefaultAntigravitySafetySettingsDoc(outDoc *ojson.Doc) {
	if outDoc.Get("request.safetySettings").Exists() {
		return
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
		return
	}
	_ = outDoc.SetRawBytes("request.safetySettings", raw)
}
