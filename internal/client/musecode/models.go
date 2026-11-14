package musecode

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// HomeModelsLoader loads scoped models when Home mode is active.
// Returning false indicates the loader has already written an HTTP error response.
type HomeModelsLoader func(c *gin.Context) ([]ModelInput, bool)

// ModelListWriter writes the final serialized model catalog response.
type ModelListWriter func(c *gin.Context, sourceFormat string, payload any)

// ModelRoute describes one provider-specific route serving a public model ID.
type ModelRoute struct {
	Provider                  string
	MetadataModelID           string
	Created                   int64
	OwnedBy                   string
	Type                      string
	DisplayName               string
	Description               string
	ContextLength             int
	MaxContextLength          int
	MaxCompletionTokens       int
	InputTokenLimit           int
	OutputTokenLimit          int
	Thinking                  *registry.ThinkingSupport
	ExplicitThinking          bool
	SupportedInputModalities  []string
	ExplicitInputModalities   bool
	SupportedOutputModalities []string
	HasOutputModalities       bool
}

// ModelInput represents a normalized available model entry used to construct
// the Muse Code /muse-code/models catalog.
type ModelInput struct {
	ID                        string
	MetadataModelID           string
	Created                   int64
	OwnedBy                   string
	Type                      string
	DisplayName               string
	Description               string
	ContextLength             int
	MaxContextLength          int
	MaxCompletionTokens       int
	InputTokenLimit           int
	OutputTokenLimit          int
	Thinking                  *registry.ThinkingSupport
	ExplicitThinking          bool
	SupportedInputModalities  []string
	ExplicitInputModalities   bool
	SupportedOutputModalities []string
	HasOutputModalities       bool
	Routes                    []ModelRoute
}

// Response represents the /muse-code/models response envelope.
type Response struct {
	Object string       `json:"object"`
	Data   []ModelEntry `json:"data"`
}

// ModelEntry represents a single model record inside Response.Data.
type ModelEntry struct {
	ID       string        `json:"id"`
	Object   string        `json:"object"`
	Created  int64         `json:"created"`
	OwnedBy  string        `json:"owned_by"`
	Metadata ModelMetadata `json:"metadata"`
}

// ModelMetadata wraps provider-client metadata for a model entry.
type ModelMetadata struct {
	MuseCode MuseCodeMetadata `json:"muse-code"`
}

// MuseCodeMetadata describes model capabilities and defaults consumed by Muse Code CLI.
type MuseCodeMetadata struct {
	Name                 string                   `json:"name"`
	Family               string                   `json:"family"`
	ReleaseDate          string                   `json:"release_date"`
	IsHidden             bool                     `json:"is_hidden"`
	Attachment           bool                     `json:"attachment"`
	Reasoning            bool                     `json:"reasoning"`
	Temperature          bool                     `json:"temperature"`
	ToolCall             bool                     `json:"tool_call"`
	Modalities           Modalities               `json:"modalities"`
	Limit                *Limits                  `json:"limit,omitempty"`
	Options              Options                  `json:"options"`
	Variants             map[string]EffortVariant `json:"variants,omitempty"`
	Description          *string                  `json:"description"`
	BackgroundAgentModel string                   `json:"background_agent_model,omitempty"`
}

// Modalities lists supported input and output content modalities.
type Modalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

// Limits describes context window and max output token limits.
type Limits struct {
	Context int `json:"context,omitempty"`
	Output  int `json:"output,omitempty"`
}

// Options contains default request options injected by Muse Code CLI.
type Options struct {
	ReasoningEffort string   `json:"reasoningEffort,omitempty"`
	ForceReasoning  bool     `json:"forceReasoning,omitempty"`
	Include         []string `json:"include,omitempty"`
	Temperature     *float64 `json:"temperature,omitempty"`
	TopP            *float64 `json:"top_p,omitempty"`
}

// EffortVariant describes a selectable reasoning effort variant.
type EffortVariant struct {
	ReasoningEffort string `json:"reasoningEffort"`
}

var canonicalReasoningOrder = []string{
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
	"ultra",
}

var allowedReasoningLevels = map[string]struct{}{
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
	"max":     {},
	"ultra":   {},
}

var budgetCandidateLevels = []string{
	"minimal",
	"low",
	"medium",
	"high",
	"xhigh",
	"max",
}

// SetModelListHooks configures optional hooks for loading Home-mode models and
// writing intercepted model catalog responses.
func (h *Handler) SetModelListHooks(homeLoader HomeModelsLoader, responseWriter ModelListWriter) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.homeModelsLoader = homeLoader
	h.modelListWriter = responseWriter
	h.mu.Unlock()
}

// HandleModels serves GET /muse-code/models from CPA's available model catalog
// (or the configured Home loader when Home mode is enabled).
func (h *Handler) HandleModels(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	if c.Request.Method != http.MethodGet {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	var homeEnabled bool
	var homeLoader HomeModelsLoader
	var listWriter ModelListWriter

	if h != nil {
		h.mu.RLock()
		cfg := h.cfg
		authManager := h.authManager
		homeLoader = h.homeModelsLoader
		listWriter = h.modelListWriter
		h.mu.RUnlock()
		homeEnabled = (authManager != nil && authManager.HomeEnabled()) || (cfg != nil && cfg.Home.Enabled)
	}

	var inputs []ModelInput
	if homeEnabled {
		if homeLoader == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "home control center unavailable"})
			return
		}
		var ok bool
		inputs, ok = homeLoader(c)
		if !ok {
			return
		}
	} else {
		inputs = ModelInputsFromGlobalRegistry(registry.GetGlobalRegistry())
	}

	payload := BuildModelsResponse(inputs)
	body, errMarshal := MarshalCompact(payload)
	if errMarshal != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": errMarshal.Error()})
		return
	}

	if listWriter != nil {
		listWriter(c, "openai", body)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", body)
}

// ModelInputsFromGlobalRegistry extracts ModelInput entries from reg, preserving
// per-provider route metadata so conflicting capabilities merge conservatively.
func ModelInputsFromGlobalRegistry(reg *registry.ModelRegistry) []ModelInput {
	if reg == nil {
		return nil
	}
	infos := reg.GetAvailableModelInfos()
	inputs := ModelInputsFromRegistry(infos)
	for i := range inputs {
		providers := reg.GetModelProviders(inputs[i].ID)
		if len(providers) == 0 {
			continue
		}
		routes := make([]ModelRoute, 0, len(providers))
		for _, provider := range providers {
			pInfo := reg.GetModelInfo(inputs[i].ID, provider)
			if pInfo == nil {
				continue
			}
			routes = append(routes, ModelRoute{
				Provider:                  provider,
				MetadataModelID:           pInfo.MetadataModelID,
				Created:                   pInfo.Created,
				OwnedBy:                   pInfo.OwnedBy,
				Type:                      pInfo.Type,
				DisplayName:               pInfo.DisplayName,
				Description:               pInfo.Description,
				ContextLength:             pInfo.ContextLength,
				MaxContextLength:          pInfo.MaxContextLength,
				MaxCompletionTokens:       pInfo.MaxCompletionTokens,
				InputTokenLimit:           pInfo.InputTokenLimit,
				OutputTokenLimit:          pInfo.OutputTokenLimit,
				Thinking:                  pInfo.Thinking,
				ExplicitThinking:          pInfo.ExplicitThinking,
				SupportedInputModalities:  append([]string(nil), pInfo.SupportedInputModalities...),
				ExplicitInputModalities:   pInfo.ExplicitInputModalities,
				SupportedOutputModalities: append([]string(nil), pInfo.SupportedOutputModalities...),
				HasOutputModalities:       len(pInfo.SupportedOutputModalities) > 0,
			})
		}
		if len(routes) > 0 {
			inputs[i].Routes = routes
		}
	}
	return inputs
}

// ModelInputsFromRegistry converts registry ModelInfo records into ModelInput entries.
func ModelInputsFromRegistry(infos []*registry.ModelInfo) []ModelInput {
	if len(infos) == 0 {
		return nil
	}
	inputs := make([]ModelInput, 0, len(infos))
	for _, info := range infos {
		if info == nil || strings.TrimSpace(info.ID) == "" {
			continue
		}
		inputs = append(inputs, ModelInput{
			ID:                        info.ID,
			MetadataModelID:           info.MetadataModelID,
			Created:                   info.Created,
			OwnedBy:                   info.OwnedBy,
			Type:                      info.Type,
			DisplayName:               info.DisplayName,
			Description:               info.Description,
			ContextLength:             info.ContextLength,
			MaxContextLength:          info.MaxContextLength,
			MaxCompletionTokens:       info.MaxCompletionTokens,
			InputTokenLimit:           info.InputTokenLimit,
			OutputTokenLimit:          info.OutputTokenLimit,
			Thinking:                  info.Thinking,
			ExplicitThinking:          info.ExplicitThinking,
			SupportedInputModalities:  append([]string(nil), info.SupportedInputModalities...),
			ExplicitInputModalities:   info.ExplicitInputModalities,
			SupportedOutputModalities: append([]string(nil), info.SupportedOutputModalities...),
			HasOutputModalities:       len(info.SupportedOutputModalities) > 0,
		})
	}
	return inputs
}

// BuildModelsResponse builds a Muse Code compatible model catalog from the given inputs.
func BuildModelsResponse(inputs []ModelInput) Response {
	deduped := make([]ModelInput, 0, len(inputs))
	seen := make(map[string]struct{}, len(inputs))
	availableIDs := make(map[string]struct{}, len(inputs))

	for _, input := range inputs {
		id := strings.TrimSpace(input.ID)
		if id == "" {
			continue
		}
		input.ID = id
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		availableIDs[id] = struct{}{}
		deduped = append(deduped, input)
	}

	sort.Slice(deduped, func(i, j int) bool {
		return deduped[i].ID < deduped[j].ID
	})

	data := make([]ModelEntry, 0, len(deduped))
	for _, input := range deduped {
		data = append(data, buildModelEntry(input, availableIDs))
	}

	return Response{
		Object: "list",
		Data:   data,
	}
}

// MarshalCompact encodes resp as compact JSON via *ojson.Doc.
func MarshalCompact(resp Response) ([]byte, error) {
	doc := ojson.NewDocString(`{"object":"list","data":[]}`)
	object := strings.TrimSpace(resp.Object)
	if object == "" {
		object = "list"
	}
	if errSet := doc.Set("object", object); errSet != nil {
		return nil, errSet
	}

	for _, entry := range resp.Data {
		entryDoc, errEntry := marshalModelEntryDoc(entry)
		if errEntry != nil {
			return nil, errEntry
		}
		if errAppend := doc.SetRawBytes("data.-1", entryDoc.Bytes()); errAppend != nil {
			return nil, errAppend
		}
	}

	return doc.Bytes(), nil
}

func marshalModelEntryDoc(entry ModelEntry) (*ojson.Doc, error) {
	entryDoc := ojson.NewDocString(`{"id":"","object":"model","created":0,"owned_by":"","metadata":{"muse-code":{}}}`)
	if err := entryDoc.Set("id", entry.ID); err != nil {
		return nil, err
	}
	obj := entry.Object
	if obj == "" {
		obj = "model"
	}
	if err := entryDoc.Set("object", obj); err != nil {
		return nil, err
	}
	if err := entryDoc.Set("created", entry.Created); err != nil {
		return nil, err
	}
	if err := entryDoc.Set("owned_by", entry.OwnedBy); err != nil {
		return nil, err
	}

	mc := entry.Metadata.MuseCode
	mcDoc := ojson.NewDocString(`{}`)
	if err := mcDoc.Set("name", mc.Name); err != nil {
		return nil, err
	}
	if err := mcDoc.Set("family", mc.Family); err != nil {
		return nil, err
	}
	if err := mcDoc.Set("release_date", mc.ReleaseDate); err != nil {
		return nil, err
	}
	if err := mcDoc.Set("is_hidden", mc.IsHidden); err != nil {
		return nil, err
	}
	if err := mcDoc.Set("attachment", mc.Attachment); err != nil {
		return nil, err
	}
	if err := mcDoc.Set("reasoning", mc.Reasoning); err != nil {
		return nil, err
	}
	if err := mcDoc.Set("temperature", mc.Temperature); err != nil {
		return nil, err
	}
	if err := mcDoc.Set("tool_call", mc.ToolCall); err != nil {
		return nil, err
	}

	modalitiesDoc := ojson.NewDocString(`{"input":[],"output":[]}`)
	for _, mod := range mc.Modalities.Input {
		if err := modalitiesDoc.Set("input.-1", mod); err != nil {
			return nil, err
		}
	}
	for _, mod := range mc.Modalities.Output {
		if err := modalitiesDoc.Set("output.-1", mod); err != nil {
			return nil, err
		}
	}
	if err := mcDoc.SetRawBytes("modalities", modalitiesDoc.Bytes()); err != nil {
		return nil, err
	}

	if mc.Limit != nil {
		limitDoc := ojson.NewDocString(`{}`)
		if mc.Limit.Context > 0 {
			if err := limitDoc.Set("context", mc.Limit.Context); err != nil {
				return nil, err
			}
		}
		if mc.Limit.Output > 0 {
			if err := limitDoc.Set("output", mc.Limit.Output); err != nil {
				return nil, err
			}
		}
		if err := mcDoc.SetRawBytes("limit", limitDoc.Bytes()); err != nil {
			return nil, err
		}
	}

	optsDoc := ojson.NewDocString(`{}`)
	if mc.Options.ReasoningEffort != "" {
		if err := optsDoc.Set("reasoningEffort", mc.Options.ReasoningEffort); err != nil {
			return nil, err
		}
	}
	if mc.Options.ForceReasoning {
		if err := optsDoc.Set("forceReasoning", true); err != nil {
			return nil, err
		}
	}
	for _, inc := range mc.Options.Include {
		if err := optsDoc.Set("include.-1", inc); err != nil {
			return nil, err
		}
	}
	if mc.Options.Temperature != nil {
		if err := optsDoc.Set("temperature", *mc.Options.Temperature); err != nil {
			return nil, err
		}
	}
	if mc.Options.TopP != nil {
		if err := optsDoc.Set("top_p", *mc.Options.TopP); err != nil {
			return nil, err
		}
	}
	if err := mcDoc.SetRawBytes("options", optsDoc.Bytes()); err != nil {
		return nil, err
	}

	if len(mc.Variants) > 0 {
		varDoc := ojson.NewDocString(`{}`)
		for _, key := range orderedVariantKeys(mc.Variants) {
			vDoc := ojson.NewDocString(`{}`)
			if err := vDoc.Set("reasoningEffort", mc.Variants[key].ReasoningEffort); err != nil {
				return nil, err
			}
			if err := varDoc.SetRawBytes(ojson.Escape(key), vDoc.Bytes()); err != nil {
				return nil, err
			}
		}
		if err := mcDoc.SetRawBytes("variants", varDoc.Bytes()); err != nil {
			return nil, err
		}
	}

	if mc.Description != nil {
		if err := mcDoc.Set("description", *mc.Description); err != nil {
			return nil, err
		}
	} else {
		if err := mcDoc.Set("description", nil); err != nil {
			return nil, err
		}
	}

	if mc.BackgroundAgentModel != "" {
		if err := mcDoc.Set("background_agent_model", mc.BackgroundAgentModel); err != nil {
			return nil, err
		}
	}

	if err := entryDoc.SetRawBytes("metadata.muse-code", mcDoc.Bytes()); err != nil {
		return nil, err
	}
	return entryDoc, nil
}

func orderedVariantKeys(variants map[string]EffortVariant) []string {
	keys := make([]string, 0, len(variants))
	seen := make(map[string]struct{}, len(variants))
	for _, level := range canonicalReasoningOrder {
		if _, exists := variants[level]; exists {
			keys = append(keys, level)
			seen[level] = struct{}{}
		}
	}
	var extra []string
	for key := range variants {
		if _, exists := seen[key]; !exists {
			extra = append(extra, key)
		}
	}
	sort.Strings(extra)
	return append(keys, extra...)
}

type resolvedRoute struct {
	metadataModelID  string
	ownedBy          string
	modelType        string
	displayName      string
	description      string
	created          int64
	inputModalities  []string
	outputModalities []string
	isHidden         bool
	contextLimit     int
	outputLimit      int
	reasoningLevels  []string
}

func buildModelEntry(input ModelInput, availableIDs map[string]struct{}) ModelEntry {
	routes := input.Routes
	if len(routes) == 0 {
		routes = []ModelRoute{
			{
				MetadataModelID:           input.MetadataModelID,
				Created:                   input.Created,
				OwnedBy:                   input.OwnedBy,
				Type:                      input.Type,
				DisplayName:               input.DisplayName,
				Description:               input.Description,
				ContextLength:             input.ContextLength,
				MaxContextLength:          input.MaxContextLength,
				MaxCompletionTokens:       input.MaxCompletionTokens,
				InputTokenLimit:           input.InputTokenLimit,
				OutputTokenLimit:          input.OutputTokenLimit,
				Thinking:                  input.Thinking,
				ExplicitThinking:          input.ExplicitThinking,
				SupportedInputModalities:  input.SupportedInputModalities,
				ExplicitInputModalities:   input.ExplicitInputModalities,
				SupportedOutputModalities: input.SupportedOutputModalities,
				HasOutputModalities:       input.HasOutputModalities,
			},
		}
	}

	resolved := make([]resolvedRoute, 0, len(routes))
	for _, route := range routes {
		resolved = append(resolved, resolveSingleRoute(input.ID, route))
	}

	var created int64
	for _, r := range resolved {
		if created <= 0 && r.created > 0 {
			created = r.created
		}
	}
	if created <= 0 {
		created = input.Created
	}

	var ownedBy string
	for _, r := range resolved {
		if ownedBy == "" && r.ownedBy != "" {
			ownedBy = r.ownedBy
		}
	}
	if ownedBy == "" {
		ownedBy = strings.TrimSpace(input.OwnedBy)
	}

	var modelType string
	for _, r := range resolved {
		if modelType == "" && r.modelType != "" {
			modelType = r.modelType
		}
	}
	if modelType == "" {
		modelType = strings.TrimSpace(input.Type)
	}

	// Only propagate a canonical identity when every route agrees on it.
	canonicalID := strings.TrimSpace(resolved[0].metadataModelID)
	for i := 1; i < len(resolved); i++ {
		if !strings.EqualFold(strings.TrimSpace(resolved[i].metadataModelID), canonicalID) {
			canonicalID = ""
			break
		}
	}

	if ownedBy == "" {
		ownedBy = inferOwnedBy(input.ID, canonicalID, modelType, "")
	}

	var displayName string
	for _, r := range resolved {
		if displayName == "" && r.displayName != "" {
			displayName = r.displayName
		}
	}
	if displayName == "" {
		displayName = strings.TrimSpace(input.DisplayName)
	}
	if displayName == "" {
		displayName = input.ID
	}

	var descriptionText string
	for _, r := range resolved {
		if descriptionText == "" && r.description != "" {
			descriptionText = r.description
		}
	}
	if descriptionText == "" {
		descriptionText = strings.TrimSpace(input.Description)
	}
	var description *string
	if descriptionText != "" {
		description = &descriptionText
	}

	// Merge capabilities conservatively across all routes so no unavailable
	// capability or excess context limit is advertised.
	inputModalities := append([]string(nil), resolved[0].inputModalities...)
	outputModalities := append([]string(nil), resolved[0].outputModalities...)
	reasoningLevels := append([]string(nil), resolved[0].reasoningLevels...)
	contextLimit := resolved[0].contextLimit
	outputLimit := resolved[0].outputLimit
	anyHidden := resolved[0].isHidden

	for i := 1; i < len(resolved); i++ {
		r := resolved[i]
		inputModalities = intersectStringSlices(inputModalities, r.inputModalities)
		outputModalities = intersectStringSlices(outputModalities, r.outputModalities)
		reasoningLevels = intersectStringSlices(reasoningLevels, r.reasoningLevels)
		contextLimit = minPositive(contextLimit, r.contextLimit)
		outputLimit = minPositive(outputLimit, r.outputLimit)
		if r.isHidden {
			anyHidden = true
		}
	}

	if len(inputModalities) == 0 {
		inputModalities = []string{"text"}
	}

	isHidden := false
	if len(outputModalities) > 0 {
		isHidden = !containsModality(outputModalities, "text")
	} else {
		// Disjoint output modalities across routes (for example text vs image):
		// hide the model conservatively and keep output modalities consistent with isHidden.
		isHidden = anyHidden || true
		for _, r := range resolved {
			if !containsModality(r.outputModalities, "text") && len(r.outputModalities) > 0 {
				outputModalities = append([]string(nil), r.outputModalities...)
				break
			}
		}
		if len(outputModalities) == 0 {
			outputModalities = []string{"image"}
		}
	}

	if isHidden {
		reasoningLevels = nil
	}

	toolCall := !isHidden
	attachment := hasNonTextModality(inputModalities)
	family := inferFamily(input.ID, canonicalID, ownedBy, modelType, isHidden)

	releaseDate := ""
	if created > 0 {
		releaseDate = time.Unix(created, 0).UTC().Format("2006-01-02")
	}

	var limit *Limits
	if contextLimit > 0 || outputLimit > 0 {
		limit = &Limits{
			Context: contextLimit,
			Output:  outputLimit,
		}
	}

	reasoning := len(reasoningLevels) > 0
	temperature := !reasoning && !isHidden

	var variants map[string]EffortVariant
	var options Options
	if reasoning {
		variants = make(map[string]EffortVariant, len(reasoningLevels))
		for _, level := range reasoningLevels {
			variants[level] = EffortVariant{ReasoningEffort: level}
		}
		defaultEffort := selectDefaultEffort(reasoningLevels, family)
		options = Options{
			ReasoningEffort: defaultEffort,
			ForceReasoning:  true,
			Include:         []string{"reasoning.encrypted_content"},
		}
		if family == "avocado" {
			defaultTemp := 0.9
			defaultTopP := 0.9
			options.Temperature = &defaultTemp
			options.TopP = &defaultTopP
		}
	}

	backgroundAgentModel := ""
	if strings.HasPrefix(strings.ToLower(input.ID), "muse-wonder-") {
		if _, exists := availableIDs["muse-spark-1.3"]; exists {
			backgroundAgentModel = "muse-spark-1.3"
		}
	}

	return ModelEntry{
		ID:      input.ID,
		Object:  "model",
		Created: created,
		OwnedBy: ownedBy,
		Metadata: ModelMetadata{
			MuseCode: MuseCodeMetadata{
				Name:                 displayName,
				Family:               family,
				ReleaseDate:          releaseDate,
				IsHidden:             isHidden,
				Attachment:           attachment,
				Reasoning:            reasoning,
				Temperature:          temperature,
				ToolCall:             toolCall,
				Modalities:           Modalities{Input: inputModalities, Output: outputModalities},
				Limit:                limit,
				Options:              options,
				Variants:             variants,
				Description:          description,
				BackgroundAgentModel: backgroundAgentModel,
			},
		},
	}
}

func resolveSingleRoute(modelID string, route ModelRoute) resolvedRoute {
	metaID := strings.TrimSpace(route.MetadataModelID)
	staticInfo := resolveStaticModelInfo(modelID, metaID)

	created := route.Created
	if created <= 0 && staticInfo != nil {
		created = staticInfo.Created
	}

	modelType := strings.TrimSpace(route.Type)
	if modelType == "" && staticInfo != nil {
		modelType = strings.TrimSpace(staticInfo.Type)
	}

	ownedBy := strings.TrimSpace(route.OwnedBy)
	if ownedBy == "" && staticInfo != nil {
		ownedBy = strings.TrimSpace(staticInfo.OwnedBy)
	}
	if ownedBy == "" {
		ownedBy = inferOwnedBy(modelID, metaID, modelType, route.Provider)
	}

	displayName := strings.TrimSpace(route.DisplayName)
	if displayName == "" && staticInfo != nil {
		displayName = strings.TrimSpace(staticInfo.DisplayName)
	}

	description := strings.TrimSpace(route.Description)
	if description == "" && staticInfo != nil {
		description = strings.TrimSpace(staticInfo.Description)
	}

	var inputModalities []string
	switch {
	case route.ExplicitInputModalities:
		inputModalities = normalizeModalities(route.SupportedInputModalities)
	case len(route.SupportedInputModalities) > 0:
		inputModalities = normalizeModalities(route.SupportedInputModalities)
	case staticInfo != nil && len(staticInfo.SupportedInputModalities) > 0:
		inputModalities = normalizeModalities(staticInfo.SupportedInputModalities)
	}
	if len(inputModalities) == 0 {
		inputModalities = []string{"text"}
	}

	var outputModalities []string
	explicitOutput := false
	switch {
	case len(route.SupportedOutputModalities) > 0:
		outputModalities = normalizeModalities(route.SupportedOutputModalities)
		explicitOutput = len(outputModalities) > 0
	case route.HasOutputModalities:
		outputModalities = []string{"text"}
		explicitOutput = true
	case staticInfo != nil && len(staticInfo.SupportedOutputModalities) > 0:
		outputModalities = normalizeModalities(staticInfo.SupportedOutputModalities)
		explicitOutput = len(outputModalities) > 0
	default:
		outputModalities = inferOutputModalities(modelID, metaID, modelType)
	}

	isHidden := !containsModality(outputModalities, "text")
	if !explicitOutput && isNonChatModelType(modelType) {
		isHidden = true
	}

	contextLimit := firstPositive(
		route.MaxContextLength,
		route.ContextLength,
		route.InputTokenLimit,
	)
	if contextLimit <= 0 && staticInfo != nil {
		contextLimit = firstPositive(
			staticInfo.MaxContextLength,
			staticInfo.ContextLength,
			staticInfo.InputTokenLimit,
		)
	}
	outputLimit := firstPositive(
		route.MaxCompletionTokens,
		route.OutputTokenLimit,
	)
	if outputLimit <= 0 && staticInfo != nil {
		outputLimit = firstPositive(
			staticInfo.MaxCompletionTokens,
			staticInfo.OutputTokenLimit,
		)
	}

	thinkingSupport := route.Thinking
	if !route.ExplicitThinking && thinkingSupport == nil && staticInfo != nil {
		thinkingSupport = staticInfo.Thinking
	}

	var reasoningLevels []string
	if !isHidden {
		reasoningLevels = resolveReasoningLevels(thinkingSupport)
	}

	return resolvedRoute{
		metadataModelID:  metaID,
		ownedBy:          ownedBy,
		modelType:        modelType,
		displayName:      displayName,
		description:      description,
		created:          created,
		inputModalities:  inputModalities,
		outputModalities: outputModalities,
		isHidden:         isHidden,
		contextLimit:     contextLimit,
		outputLimit:      outputLimit,
		reasoningLevels:  reasoningLevels,
	}
}

func resolveStaticModelInfo(id, metadataModelID string) *registry.ModelInfo {
	id = strings.TrimSpace(id)
	metaID := strings.TrimSpace(metadataModelID)
	if metaID != "" {
		if info := registry.LookupStaticModelInfo(metaID); info != nil {
			return info
		}
		if !strings.EqualFold(metaID, id) {
			return nil
		}
	}
	return registry.LookupStaticModelInfo(id)
}

func inferOwnedBy(id, metadataModelID, modelType, provider string) string {
	lowerType := strings.ToLower(strings.TrimSpace(modelType))
	lowerProvider := normalizeProviderName(provider)
	lowerCanonical := strings.ToLower(strings.TrimSpace(metadataModelID))
	lowerID := strings.ToLower(strings.TrimSpace(id))

	if owned := inferOwnedByFromType(lowerType); owned != "" {
		return owned
	}
	if owned := inferOwnedByFromType(lowerProvider); owned != "" {
		return owned
	}
	if owned := inferOwnedByFromName(lowerCanonical); owned != "" {
		return owned
	}
	if owned := inferOwnedByFromName(lowerID); owned != "" {
		return owned
	}
	if lowerType != "" {
		return lowerType
	}
	if lowerProvider != "" {
		return lowerProvider
	}
	return "custom"
}

func normalizeProviderName(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch p {
	case "codex", "codex-free", "codex-plus", "codex-team", "codex-pro":
		return "codex"
	default:
		return p
	}
}

func inferOwnedByFromType(t string) string {
	switch t {
	case "meta":
		return "meta"
	case "devin":
		return "devin"
	case "claude", "antigravity":
		return "anthropic"
	case "gemini", "vertex", "aistudio", "gemini-cli":
		return "google"
	case "xai":
		return "xai"
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return "moonshot"
	case "codex", "openai", "openai-image":
		return "openai"
	default:
		return ""
	}
}

func inferOwnedByFromName(name string) string {
	if name == "" {
		return ""
	}
	switch {
	case strings.HasPrefix(name, "muse-") || strings.HasPrefix(name, "agnes-"):
		return "meta"
	case strings.HasPrefix(name, "devin/"):
		return "devin"
	case strings.HasPrefix(name, "claude-"):
		return "anthropic"
	case strings.HasPrefix(name, "gemini-") || strings.HasPrefix(name, "imagen-"):
		return "google"
	case strings.HasPrefix(name, "grok-"):
		return "xai"
	case strings.HasPrefix(name, "kimi-"):
		return "moonshot"
	case strings.HasPrefix(name, "gpt-") || strings.HasPrefix(name, "o1") || strings.HasPrefix(name, "o3") || strings.HasPrefix(name, "o4") || strings.Contains(name, "codex"):
		return "openai"
	default:
		return ""
	}
}

func inferFamily(id, metadataModelID, ownedBy, modelType string, isHidden bool) string {
	lowerID := strings.ToLower(strings.TrimSpace(id))
	lowerCanonical := strings.ToLower(strings.TrimSpace(metadataModelID))
	lowerOwned := strings.ToLower(strings.TrimSpace(ownedBy))
	lowerType := strings.ToLower(strings.TrimSpace(modelType))

	if isMetaIdentity(lowerCanonical, lowerID, lowerOwned, lowerType) {
		if isHidden {
			if strings.HasPrefix(lowerCanonical, "muse-image") || strings.HasPrefix(lowerID, "muse-image") {
				return "muse-image"
			}
			if strings.HasPrefix(lowerCanonical, "agnes-image") || strings.HasPrefix(lowerID, "agnes-image") {
				return "agnes-image"
			}
		}
		return "avocado"
	}
	if lowerOwned != "" && lowerOwned != "custom" {
		return lowerOwned
	}
	if lowerType != "" {
		return lowerType
	}
	if lowerOwned != "" {
		return lowerOwned
	}
	return "openai"
}

func isMetaIdentity(canonical, id, ownedBy, modelType string) bool {
	if ownedBy == "meta" || modelType == "meta" {
		return true
	}
	if canonical != "" {
		return strings.HasPrefix(canonical, "muse-") || strings.HasPrefix(canonical, "agnes-")
	}
	return strings.HasPrefix(id, "muse-") || strings.HasPrefix(id, "agnes-")
}

func normalizeModalities(modalities []string) []string {
	if len(modalities) == 0 {
		return nil
	}
	out := make([]string, 0, len(modalities))
	seen := make(map[string]struct{}, len(modalities))
	for _, raw := range modalities {
		mod := strings.ToLower(strings.TrimSpace(raw))
		if mod == "" {
			continue
		}
		if _, exists := seen[mod]; exists {
			continue
		}
		seen[mod] = struct{}{}
		out = append(out, mod)
	}
	return out
}

func inferOutputModalities(id, metadataModelID, modelType string) []string {
	lowerType := strings.ToLower(strings.TrimSpace(modelType))
	switch lowerType {
	case "openai-image", "image":
		return []string{"image"}
	case "video":
		return []string{"video"}
	case "audio", "tts":
		return []string{"audio"}
	}

	if canonical := strings.ToLower(strings.TrimSpace(metadataModelID)); canonical != "" {
		if inferred := inferOutputModalitiesFromName(canonical); len(inferred) > 0 {
			return inferred
		}
		return []string{"text"}
	}

	if inferred := inferOutputModalitiesFromName(strings.ToLower(strings.TrimSpace(id))); len(inferred) > 0 {
		return inferred
	}
	return []string{"text"}
}

func inferOutputModalitiesFromName(name string) []string {
	switch {
	case strings.HasPrefix(name, "muse-image") ||
		strings.HasPrefix(name, "agnes-image") ||
		strings.HasPrefix(name, "gpt-image") ||
		strings.HasPrefix(name, "imagen-") ||
		strings.HasPrefix(name, "grok-imagine-image") ||
		strings.HasPrefix(name, "dall-e"):
		return []string{"image"}
	case strings.HasPrefix(name, "grok-imagine-video"):
		return []string{"video"}
	case strings.HasPrefix(name, "grok-tts") ||
		strings.HasPrefix(name, "grok-voice-tts") ||
		strings.HasPrefix(name, "tts-"):
		return []string{"audio"}
	default:
		return nil
	}
}

func isNonChatModelType(modelType string) bool {
	switch strings.ToLower(strings.TrimSpace(modelType)) {
	case "openai-image", "image", "video", "audio", "tts":
		return true
	default:
		return false
	}
}

func containsModality(modalities []string, target string) bool {
	for _, mod := range modalities {
		if mod == target {
			return true
		}
	}
	return false
}

func hasNonTextModality(modalities []string) bool {
	for _, mod := range modalities {
		if mod != "text" {
			return true
		}
	}
	return false
}

func resolveReasoningLevels(support *registry.ThinkingSupport) []string {
	if support == nil {
		return nil
	}
	if len(support.Levels) > 0 {
		out := make([]string, 0, len(support.Levels))
		seen := make(map[string]struct{}, len(support.Levels))
		for _, raw := range support.Levels {
			level := strings.ToLower(strings.TrimSpace(raw))
			if _, ok := allowedReasoningLevels[level]; !ok {
				continue
			}
			if _, exists := seen[level]; exists {
				continue
			}
			seen[level] = struct{}{}
			out = append(out, level)
		}
		return out
	}

	// Align with thinking.detectModelCapability: budget support requires Min > 0 or Max > 0.
	if support.Min <= 0 && support.Max <= 0 {
		return nil
	}

	type budgetChoice struct {
		level    string
		distance int
	}
	byClamped := make(map[int]budgetChoice, len(budgetCandidateLevels))
	clampedOrder := make([]int, 0, len(budgetCandidateLevels))

	for _, level := range budgetCandidateLevels {
		rawBudget, ok := thinking.ConvertLevelToBudget(level)
		if !ok || rawBudget <= 0 {
			continue
		}
		clamped := rawBudget
		if support.Min > 0 && clamped < support.Min {
			clamped = support.Min
		}
		if support.Max > 0 && clamped > support.Max {
			clamped = support.Max
		}
		dist := rawBudget - clamped
		if dist < 0 {
			dist = -dist
		}
		if prev, exists := byClamped[clamped]; exists {
			if dist < prev.distance {
				byClamped[clamped] = budgetChoice{level: level, distance: dist}
			}
			continue
		}
		byClamped[clamped] = budgetChoice{level: level, distance: dist}
		clampedOrder = append(clampedOrder, clamped)
	}

	out := make([]string, 0, len(clampedOrder))
	for _, clamped := range clampedOrder {
		out = append(out, byClamped[clamped].level)
	}
	return out
}

func selectDefaultEffort(levels []string, family string) string {
	preferredOrder := []string{"medium", "high"}
	if family == "avocado" {
		preferredOrder = []string{"high", "medium"}
	}
	for _, pref := range preferredOrder {
		for _, level := range levels {
			if level == pref {
				return pref
			}
		}
	}
	return levels[0]
}

func intersectStringSlices(a, b []string) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}
	bSet := make(map[string]struct{}, len(b))
	for _, item := range b {
		bSet[item] = struct{}{}
	}
	out := make([]string, 0, len(a))
	seen := make(map[string]struct{}, len(a))
	for _, item := range a {
		if _, inB := bSet[item]; inB {
			if _, inSeen := seen[item]; !inSeen {
				seen[item] = struct{}{}
				out = append(out, item)
			}
		}
	}
	return out
}

func firstPositive(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func minPositive(a, b int) int {
	switch {
	case a <= 0:
		return b
	case b <= 0:
		return a
	case b < a:
		return b
	default:
		return a
	}
}
