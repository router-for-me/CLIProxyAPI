package dto

import (
	"encoding/json"
	"net/http"
	"strings"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type GeminiChatRequest struct {
	Requests           []GeminiChatRequest        `json:"requests,omitempty"` // For batch requests
	Contents           []GeminiChatContent        `json:"contents"`
	SafetySettings     []GeminiChatSafetySettings `json:"safetySettings,omitempty"`
	GenerationConfig   GeminiChatGenerationConfig `json:"generationConfig"`
	Tools              json.RawMessage            `json:"tools,omitempty"`
	ToolConfig         *ToolConfig                `json:"toolConfig,omitempty"`
	SystemInstructions *GeminiChatContent         `json:"systemInstruction,omitempty"`
	CachedContent      string                     `json:"cachedContent,omitempty"`
}

// UnmarshalJSON allows GeminiChatRequest to accept both snake_case and camelCase fields.
func (r *GeminiChatRequest) UnmarshalJSON(data []byte) error {
	type Alias GeminiChatRequest
	var aux struct {
		Alias
		SystemInstructionSnake *GeminiChatContent `json:"system_instruction,omitempty"`
	}

	if err := kitutil.Unmarshal(data, &aux); err != nil {
		return err
	}

	*r = GeminiChatRequest(aux.Alias)

	if aux.SystemInstructionSnake != nil {
		r.SystemInstructions = aux.SystemInstructionSnake
	}

	return nil
}

type ToolConfig struct {
	FunctionCallingConfig            *FunctionCallingConfig `json:"functionCallingConfig,omitempty"`
	RetrievalConfig                  *RetrievalConfig       `json:"retrievalConfig,omitempty"`
	IncludeServerSideToolInvocations *bool                  `json:"includeServerSideToolInvocations,omitempty"`
}

type FunctionCallingConfig struct {
	Mode                        FunctionCallingConfigMode `json:"mode,omitempty"`
	AllowedFunctionNames        []string                  `json:"allowedFunctionNames,omitempty"`
	StreamFunctionCallArguments *bool                     `json:"streamFunctionCallArguments,omitempty"`
}
type FunctionCallingConfigMode string

type RetrievalConfig struct {
	LatLng       *LatLng `json:"latLng,omitempty"`
	LanguageCode string  `json:"languageCode,omitempty"`
}

type LatLng struct {
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

func (r *GeminiChatRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var files []*types.FileMeta = make([]*types.FileMeta, 0)

	var maxTokens int

	if r.GenerationConfig.MaxOutputTokens != nil && *r.GenerationConfig.MaxOutputTokens > 0 {
		maxTokens = int(*r.GenerationConfig.MaxOutputTokens)
	}

	var inputTexts []string
	for _, content := range r.Contents {
		for _, part := range content.Parts {
			if part.Text != "" {
				inputTexts = append(inputTexts, part.Text)
			}
			if source := part.InlineData.ToFileSource(); source != nil {
				mimeType := part.InlineData.MimeType
				var fileType types.FileType
				if strings.HasPrefix(mimeType, "image/") {
					fileType = types.FileTypeImage
				} else if strings.HasPrefix(mimeType, "audio/") {
					fileType = types.FileTypeAudio
				} else if strings.HasPrefix(mimeType, "video/") {
					fileType = types.FileTypeVideo
				} else {
					fileType = types.FileTypeFile
				}
				files = append(files, &types.FileMeta{
					FileType: fileType,
					Source:   source,
				})
			}
		}
	}

	inputText := strings.Join(inputTexts, "\n")
	return &types.TokenCountMeta{
		CombineText: inputText,
		Files:       files,
		MaxTokens:   maxTokens,
	}
}

func (r *GeminiChatRequest) IsStream(c *http.Request) bool {
	if c == nil {
		return false
	}
	if c.URL.Query().Get("alt") == "sse" {
		return true
	}
	// Native Gemini API uses URL action to indicate streaming:
	// /v1beta/models/{model}:streamGenerateContent
	if strings.Contains(c.URL.Path, "streamGenerateContent") {
		return true
	}
	return false
}

func (r *GeminiChatRequest) SetModelName(modelName string) {
	// GeminiChatRequest does not have a model field, so this method does nothing.
}

func (r *GeminiChatRequest) GetTools() []GeminiChatTool {
	var tools []GeminiChatTool
	if strings.HasPrefix(string(r.Tools), "[") {
		// is array
		if err := kitutil.Unmarshal(r.Tools, &tools); err != nil {
			kitutil.LogError("error_unmarshalling_tools: " + err.Error())
			return nil
		}
	} else if strings.HasPrefix(string(r.Tools), "{") {
		// is object
		singleTool := GeminiChatTool{}
		if err := kitutil.Unmarshal(r.Tools, &singleTool); err != nil {
			kitutil.LogError("error_unmarshalling_single_tool: " + err.Error())
			return nil
		}
		tools = []GeminiChatTool{singleTool}
	}
	return tools
}

func (r *GeminiChatRequest) SetTools(tools []GeminiChatTool) {
	if len(tools) == 0 {
		r.Tools = json.RawMessage("[]")
		return
	}

	// Marshal the tools to JSON
	data, err := kitutil.Marshal(tools)
	if err != nil {
		kitutil.LogError("error_marshalling_tools: " + err.Error())
		return
	}
	r.Tools = data
}

type GeminiThinkingConfig struct {
	IncludeThoughts *bool `json:"includeThoughts,omitempty"`
	ThinkingBudget  *int  `json:"thinkingBudget,omitempty"`
	// TODO Conflict with thinkingbudget.
	ThinkingLevel string `json:"thinkingLevel,omitempty"`
}

// UnmarshalJSON allows GeminiThinkingConfig to accept both snake_case and camelCase fields.
func (c *GeminiThinkingConfig) UnmarshalJSON(data []byte) error {
	type Alias GeminiThinkingConfig
	var aux struct {
		Alias
		IncludeThoughtsSnake *bool  `json:"include_thoughts,omitempty"`
		ThinkingBudgetSnake  *int   `json:"thinking_budget,omitempty"`
		ThinkingLevelSnake   string `json:"thinking_level,omitempty"`
	}

	if err := kitutil.Unmarshal(data, &aux); err != nil {
		return err
	}

	*c = GeminiThinkingConfig(aux.Alias)

	if aux.IncludeThoughtsSnake != nil {
		c.IncludeThoughts = aux.IncludeThoughtsSnake
	}

	if aux.ThinkingBudgetSnake != nil {
		c.ThinkingBudget = aux.ThinkingBudgetSnake
	}

	if aux.ThinkingLevelSnake != "" {
		c.ThinkingLevel = aux.ThinkingLevelSnake
	}

	return nil
}

func (c *GeminiThinkingConfig) SetThinkingBudget(budget int) {
	c.ThinkingBudget = &budget
}

type GeminiInlineData struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

func (d *GeminiInlineData) ToFileSource() types.FileSource {
	if d == nil || d.Data == "" {
		return nil
	}
	return types.NewFileSourceFromData(d.Data, d.MimeType)
}

// UnmarshalJSON custom unmarshaler for GeminiInlineData to support snake_case and camelCase for MimeType
func (g *GeminiInlineData) UnmarshalJSON(data []byte) error {
	type Alias GeminiInlineData // Use type alias to avoid recursion
	var aux struct {
		Alias
		MimeTypeSnake string `json:"mime_type"`
	}

	if err := kitutil.Unmarshal(data, &aux); err != nil {
		return err
	}

	*g = GeminiInlineData(aux.Alias) // Copy other fields if any in future

	// Prioritize snake_case if present
	if aux.MimeTypeSnake != "" {
		g.MimeType = aux.MimeTypeSnake
	} else if aux.MimeType != "" { // Fallback to camelCase from Alias
		g.MimeType = aux.MimeType
	}
	// g.Data would be populated by aux.Alias.Data
	return nil
}

type FunctionCall struct {
	// ID is optional in the Gemini protocol and identifies the matching function response.
	ID           string             `json:"id,omitempty"`
	FunctionName string             `json:"name"`
	Arguments    any                `json:"args"`
	PartialArgs  []GeminiPartialArg `json:"partialArgs,omitempty"`
	WillContinue *bool              `json:"willContinue,omitempty"`
}

type GeminiPartialArg struct {
	JSONPath     string          `json:"jsonPath"`
	NumberValue  *float64        `json:"numberValue,omitempty"`
	StringValue  *string         `json:"stringValue,omitempty"`
	BoolValue    *bool           `json:"boolValue,omitempty"`
	NullValue    json.RawMessage `json:"nullValue,omitempty"`
	WillContinue *bool           `json:"willContinue,omitempty"`
}

type GeminiFunctionResponse struct {
	Name         string          `json:"name"`
	Response     map[string]any  `json:"response"`
	WillContinue json.RawMessage `json:"willContinue,omitempty"`
	Scheduling   json.RawMessage `json:"scheduling,omitempty"`
	Parts        json.RawMessage `json:"parts,omitempty"`
	ID           json.RawMessage `json:"id,omitempty"`
}

type GeminiPartExecutableCode struct {
	Language string `json:"language,omitempty"`
	Code     string `json:"code,omitempty"`
}

type GeminiPartCodeExecutionResult struct {
	Outcome string `json:"outcome,omitempty"`
	Output  string `json:"output,omitempty"`
}

type GeminiFileData struct {
	MimeType string `json:"mimeType,omitempty"`
	FileUri  string `json:"fileUri,omitempty"`
}

type GeminiPart struct {
	Text             string                  `json:"text,omitempty"`
	Thought          bool                    `json:"thought,omitempty"`
	InlineData       *GeminiInlineData       `json:"inlineData,omitempty"`
	FunctionCall     *FunctionCall           `json:"functionCall,omitempty"`
	ThoughtSignature json.RawMessage         `json:"thoughtSignature,omitempty"`
	FunctionResponse *GeminiFunctionResponse `json:"functionResponse,omitempty"`
	// Optional. Media resolution for the input media.
	MediaResolution     json.RawMessage                `json:"mediaResolution,omitempty"`
	VideoMetadata       json.RawMessage                `json:"videoMetadata,omitempty"`
	FileData            *GeminiFileData                `json:"fileData,omitempty"`
	ExecutableCode      *GeminiPartExecutableCode      `json:"executableCode,omitempty"`
	CodeExecutionResult *GeminiPartCodeExecutionResult `json:"codeExecutionResult,omitempty"`
}

// UnmarshalJSON custom unmarshaler for GeminiPart to support snake_case and camelCase for InlineData
func (p *GeminiPart) UnmarshalJSON(data []byte) error {
	// Alias to avoid recursion during unmarshalling
	type Alias GeminiPart
	var aux struct {
		Alias
		InlineDataSnake *GeminiInlineData `json:"inline_data,omitempty"` // snake_case variant
	}

	if err := kitutil.Unmarshal(data, &aux); err != nil {
		return err
	}

	// Assign fields from alias
	*p = GeminiPart(aux.Alias)

	// Prioritize snake_case for InlineData if present
	if aux.InlineDataSnake != nil {
		p.InlineData = aux.InlineDataSnake
	} else if aux.InlineData != nil { // Fallback to camelCase from Alias
		p.InlineData = aux.InlineData
	}
	// Other fields like Text, FunctionCall etc. are already populated via aux.Alias

	return nil
}

type GeminiChatContent struct {
	Role  string       `json:"role,omitempty"`
	Parts []GeminiPart `json:"parts"`
}

type GeminiChatSafetySettings struct {
	Category  string `json:"category"`
	Threshold string `json:"threshold"`
}

type GeminiChatTool struct {
	GoogleSearch          any             `json:"googleSearch,omitempty"`
	GoogleSearchRetrieval any             `json:"googleSearchRetrieval,omitempty"`
	GoogleMaps            json.RawMessage `json:"googleMaps,omitempty"`
	EnterpriseWebSearch   json.RawMessage `json:"enterpriseWebSearch,omitempty"`
	CodeExecution         any             `json:"codeExecution,omitempty"`
	FunctionDeclarations  any             `json:"functionDeclarations,omitempty"`
	URLContext            any             `json:"urlContext,omitempty"`
	FileSearch            json.RawMessage `json:"fileSearch,omitempty"`
	ComputerUse           json.RawMessage `json:"computerUse,omitempty"`
	Retrieval             json.RawMessage `json:"retrieval,omitempty"`
}

type GeminiChatGenerationConfig struct {
	Temperature                *float64              `json:"temperature,omitempty"`
	TopP                       *float64              `json:"topP,omitempty"`
	TopK                       *float64              `json:"topK,omitempty"`
	MaxOutputTokens            *uint                 `json:"maxOutputTokens,omitempty"`
	CandidateCount             *int                  `json:"candidateCount,omitempty"`
	StopSequences              []string              `json:"stopSequences,omitempty"`
	ResponseMimeType           string                `json:"responseMimeType,omitempty"`
	ResponseSchema             any                   `json:"responseSchema,omitempty"`
	ResponseJsonSchema         json.RawMessage       `json:"responseJsonSchema,omitempty"`
	PresencePenalty            *float32              `json:"presencePenalty,omitempty"`
	FrequencyPenalty           *float32              `json:"frequencyPenalty,omitempty"`
	ResponseLogprobs           *bool                 `json:"responseLogprobs,omitempty"`
	Logprobs                   *int32                `json:"logprobs,omitempty"`
	EnableEnhancedCivicAnswers *bool                 `json:"enableEnhancedCivicAnswers,omitempty"`
	MediaResolution            MediaResolution       `json:"mediaResolution,omitempty"`
	Seed                       *int64                `json:"seed,omitempty"`
	ResponseModalities         []string              `json:"responseModalities,omitempty"`
	ThinkingConfig             *GeminiThinkingConfig `json:"thinkingConfig,omitempty"`
	SpeechConfig               json.RawMessage       `json:"speechConfig,omitempty"` // RawMessage to allow flexible speech config
	ImageConfig                json.RawMessage       `json:"imageConfig,omitempty"`  // RawMessage to allow flexible image config
}

// UnmarshalJSON allows GeminiChatGenerationConfig to accept both snake_case and camelCase fields.
func (c *GeminiChatGenerationConfig) UnmarshalJSON(data []byte) error {
	type Alias GeminiChatGenerationConfig
	var aux struct {
		Alias
		TopPSnake                       *float64              `json:"top_p,omitempty"`
		TopKSnake                       *float64              `json:"top_k,omitempty"`
		MaxOutputTokensSnake            *uint                 `json:"max_output_tokens,omitempty"`
		CandidateCountSnake             *int                  `json:"candidate_count,omitempty"`
		StopSequencesSnake              []string              `json:"stop_sequences,omitempty"`
		ResponseMimeTypeSnake           string                `json:"response_mime_type,omitempty"`
		ResponseSchemaSnake             any                   `json:"response_schema,omitempty"`
		ResponseJsonSchemaSnake         json.RawMessage       `json:"response_json_schema,omitempty"`
		PresencePenaltySnake            *float32              `json:"presence_penalty,omitempty"`
		FrequencyPenaltySnake           *float32              `json:"frequency_penalty,omitempty"`
		ResponseLogprobsSnake           *bool                 `json:"response_logprobs,omitempty"`
		EnableEnhancedCivicAnswersSnake *bool                 `json:"enable_enhanced_civic_answers,omitempty"`
		MediaResolutionSnake            MediaResolution       `json:"media_resolution,omitempty"`
		ResponseModalitiesSnake         []string              `json:"response_modalities,omitempty"`
		ThinkingConfigSnake             *GeminiThinkingConfig `json:"thinking_config,omitempty"`
		SpeechConfigSnake               json.RawMessage       `json:"speech_config,omitempty"`
		ImageConfigSnake                json.RawMessage       `json:"image_config,omitempty"`
	}

	if err := kitutil.Unmarshal(data, &aux); err != nil {
		return err
	}

	*c = GeminiChatGenerationConfig(aux.Alias)

	// Prioritize snake_case if present
	if aux.TopPSnake != nil {
		c.TopP = aux.TopPSnake
	}
	if aux.TopKSnake != nil {
		c.TopK = aux.TopKSnake
	}
	if aux.MaxOutputTokensSnake != nil {
		c.MaxOutputTokens = aux.MaxOutputTokensSnake
	}
	if aux.CandidateCountSnake != nil {
		c.CandidateCount = aux.CandidateCountSnake
	}
	if len(aux.StopSequencesSnake) > 0 {
		c.StopSequences = aux.StopSequencesSnake
	}
	if aux.ResponseMimeTypeSnake != "" {
		c.ResponseMimeType = aux.ResponseMimeTypeSnake
	}
	if aux.ResponseSchemaSnake != nil {
		c.ResponseSchema = aux.ResponseSchemaSnake
	}
	if len(aux.ResponseJsonSchemaSnake) > 0 {
		c.ResponseJsonSchema = aux.ResponseJsonSchemaSnake
	}
	if aux.PresencePenaltySnake != nil {
		c.PresencePenalty = aux.PresencePenaltySnake
	}
	if aux.FrequencyPenaltySnake != nil {
		c.FrequencyPenalty = aux.FrequencyPenaltySnake
	}
	if aux.ResponseLogprobsSnake != nil {
		c.ResponseLogprobs = aux.ResponseLogprobsSnake
	}
	if aux.EnableEnhancedCivicAnswersSnake != nil {
		c.EnableEnhancedCivicAnswers = aux.EnableEnhancedCivicAnswersSnake
	}
	if aux.MediaResolutionSnake != "" {
		c.MediaResolution = aux.MediaResolutionSnake
	}
	if len(aux.ResponseModalitiesSnake) > 0 {
		c.ResponseModalities = aux.ResponseModalitiesSnake
	}
	if aux.ThinkingConfigSnake != nil {
		c.ThinkingConfig = aux.ThinkingConfigSnake
	}
	if len(aux.SpeechConfigSnake) > 0 {
		c.SpeechConfig = aux.SpeechConfigSnake
	}
	if len(aux.ImageConfigSnake) > 0 {
		c.ImageConfig = aux.ImageConfigSnake
	}

	return nil
}

type MediaResolution string

type GeminiChatCandidate struct {
	Content           GeminiChatContent        `json:"content"`
	FinishReason      *string                  `json:"finishReason"`
	Index             int64                    `json:"index"`
	SafetyRatings     []GeminiChatSafetyRating `json:"safetyRatings"`
	GroundingMetadata *GeminiGroundingMetadata `json:"groundingMetadata,omitempty"`
}

type GeminiGroundingMetadata struct {
	WebSearchQueries             []string        `json:"webSearchQueries,omitempty"`
	RetrievalQueries             []string        `json:"retrievalQueries,omitempty"`
	GroundingChunks              json.RawMessage `json:"groundingChunks,omitempty"`
	GroundingSupports            json.RawMessage `json:"groundingSupports,omitempty"`
	SearchEntryPoint             json.RawMessage `json:"searchEntryPoint,omitempty"`
	RetrievalMetadata            json.RawMessage `json:"retrievalMetadata,omitempty"`
	SourceFlaggingUris           json.RawMessage `json:"sourceFlaggingUris,omitempty"`
	GoogleMapsWidgetContextToken string          `json:"googleMapsWidgetContextToken,omitempty"`
}

type GeminiChatSafetyRating struct {
	Category    string `json:"category"`
	Probability string `json:"probability"`
}

type GeminiChatPromptFeedback struct {
	SafetyRatings []GeminiChatSafetyRating `json:"safetyRatings"`
	BlockReason   *string                  `json:"blockReason,omitempty"`
}

type GeminiChatResponse struct {
	Candidates       []GeminiChatCandidate     `json:"candidates"`
	PromptFeedback   *GeminiChatPromptFeedback `json:"promptFeedback,omitempty"`
	UsageMetadata    GeminiUsageMetadata       `json:"usageMetadata"`
	HasUsageMetadata bool                      `json:"-"`
}

// UnmarshalJSON records whether Gemini returned usageMetadata while preserving
// the historical wire shape that always marshals the usageMetadata field.
//
// IMPORTANT: aux shadows GeminiChatResponse. Any field added to
// GeminiChatResponse must also be added to aux (and copied below), otherwise it
// is silently dropped during unmarshal.
func (r *GeminiChatResponse) UnmarshalJSON(data []byte) error {
	var aux struct {
		Candidates     []GeminiChatCandidate     `json:"candidates"`
		PromptFeedback *GeminiChatPromptFeedback `json:"promptFeedback,omitempty"`
		UsageMetadata  *GeminiUsageMetadata      `json:"usageMetadata"`
	}
	if err := kitutil.Unmarshal(data, &aux); err != nil {
		return err
	}
	r.Candidates = aux.Candidates
	r.PromptFeedback = aux.PromptFeedback
	r.HasUsageMetadata = aux.UsageMetadata != nil
	if aux.UsageMetadata != nil {
		r.UsageMetadata = *aux.UsageMetadata
	} else {
		r.UsageMetadata = GeminiUsageMetadata{}
	}
	return nil
}

func (r *GeminiChatResponse) GetUsageMetadata() *GeminiUsageMetadata {
	if r == nil {
		return nil
	}
	if r.HasUsageMetadata || HasGeminiUsageMetadataTokens(&r.UsageMetadata) {
		return &r.UsageMetadata
	}
	return nil
}

type GeminiUsageMetadata struct {
	PromptTokenCount           int                         `json:"promptTokenCount"`
	ToolUsePromptTokenCount    int                         `json:"toolUsePromptTokenCount"`
	CandidatesTokenCount       int                         `json:"candidatesTokenCount"`
	TotalTokenCount            int                         `json:"totalTokenCount"`
	ThoughtsTokenCount         int                         `json:"thoughtsTokenCount"`
	CachedContentTokenCount    int                         `json:"cachedContentTokenCount"`
	PromptTokensDetails        []GeminiPromptTokensDetails `json:"promptTokensDetails"`
	ToolUsePromptTokensDetails []GeminiPromptTokensDetails `json:"toolUsePromptTokensDetails"`
	CandidatesTokensDetails    []GeminiPromptTokensDetails `json:"candidatesTokensDetails"`
	BillingUsage               *BillingUsage               `json:"billing_usage,omitempty"`
}

type GeminiPromptTokensDetails struct {
	Modality   string `json:"modality"`
	TokenCount int    `json:"tokenCount"`
}

// Imagen related structs
type GeminiImageRequest struct {
	Instances  []GeminiImageInstance `json:"instances"`
	Parameters GeminiImageParameters `json:"parameters"`
}

type GeminiImageInstance struct {
	Prompt string `json:"prompt"`
}

type GeminiImageParameters struct {
	SampleCount      int    `json:"sampleCount,omitempty"`
	AspectRatio      string `json:"aspectRatio,omitempty"`
	PersonGeneration string `json:"personGeneration,omitempty"`
	ImageSize        string `json:"imageSize,omitempty"`
}

type GeminiImageResponse struct {
	Predictions []GeminiImagePrediction `json:"predictions"`
}

type GeminiImagePrediction struct {
	MimeType           string `json:"mimeType"`
	BytesBase64Encoded string `json:"bytesBase64Encoded"`
	RaiFilteredReason  string `json:"raiFilteredReason,omitempty"`
	SafetyAttributes   any    `json:"safetyAttributes,omitempty"`
}

// Embedding related structs
type GeminiEmbeddingRequest struct {
	Model                string            `json:"model,omitempty"`
	Content              GeminiChatContent `json:"content"`
	TaskType             string            `json:"taskType,omitempty"`
	Title                string            `json:"title,omitempty"`
	OutputDimensionality int               `json:"outputDimensionality,omitempty"`
}

func (r *GeminiEmbeddingRequest) IsStream(c *http.Request) bool {
	// Gemini embedding requests are not streamed
	return false
}

func (r *GeminiEmbeddingRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var inputTexts []string
	for _, part := range r.Content.Parts {
		if part.Text != "" {
			inputTexts = append(inputTexts, part.Text)
		}
	}
	inputText := strings.Join(inputTexts, "\n")
	return &types.TokenCountMeta{
		CombineText: inputText,
	}
}

func (r *GeminiEmbeddingRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}

type GeminiBatchEmbeddingRequest struct {
	Requests []*GeminiEmbeddingRequest `json:"requests"`
}

func (r *GeminiBatchEmbeddingRequest) IsStream(c *http.Request) bool {
	// Gemini batch embedding requests are not streamed
	return false
}

func (r *GeminiBatchEmbeddingRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var inputTexts []string
	for _, request := range r.Requests {
		meta := request.GetTokenCountMeta()
		if meta != nil && meta.CombineText != "" {
			inputTexts = append(inputTexts, meta.CombineText)
		}
	}
	inputText := strings.Join(inputTexts, "\n")
	return &types.TokenCountMeta{
		CombineText: inputText,
	}
}

func (r *GeminiBatchEmbeddingRequest) SetModelName(modelName string) {
	if modelName != "" {
		for _, req := range r.Requests {
			req.SetModelName(modelName)
		}
	}
}

type GeminiEmbeddingResponse struct {
	Embedding ContentEmbedding `json:"embedding"`
}

type GeminiBatchEmbeddingResponse struct {
	Embeddings []*ContentEmbedding `json:"embeddings"`
}

type ContentEmbedding struct {
	Values []float64 `json:"values"`
}
