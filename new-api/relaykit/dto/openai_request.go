package dto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/samber/lo"
)

type ResponseFormat struct {
	Type       string          `json:"type,omitempty"`
	JsonSchema json.RawMessage `json:"json_schema,omitempty"`
}

type FormatJsonSchema struct {
	Description string          `json:"description,omitempty"`
	Name        string          `json:"name"`
	Schema      any             `json:"schema,omitempty"`
	Strict      json.RawMessage `json:"strict,omitempty"`
}

// GeneralOpenAIRequest represents a general request structure for OpenAI-compatible APIs.
// 参数增加规范：无引用的参数必须使用json.RawMessage类型，并添加omitempty标签
type GeneralOpenAIRequest struct {
	Model               string            `json:"model,omitempty"`
	Messages            []Message         `json:"messages,omitempty"`
	Prompt              any               `json:"prompt,omitempty"`
	Prefix              any               `json:"prefix,omitempty"`
	Suffix              any               `json:"suffix,omitempty"`
	Stream              *bool             `json:"stream,omitempty"`
	StreamOptions       *StreamOptions    `json:"stream_options,omitempty"`
	MaxTokens           *uint             `json:"max_tokens,omitempty"`
	MaxCompletionTokens *uint             `json:"max_completion_tokens,omitempty"`
	ReasoningEffort     string            `json:"reasoning_effort,omitempty"`
	Verbosity           json.RawMessage   `json:"verbosity,omitempty"` // gpt-5
	Temperature         *float64          `json:"temperature,omitempty"`
	TopP                *float64          `json:"top_p,omitempty"`
	TopK                *int              `json:"top_k,omitempty"`
	Stop                any               `json:"stop,omitempty"`
	N                   *int              `json:"n,omitempty"`
	Input               any               `json:"input,omitempty"`
	Instruction         string            `json:"instruction,omitempty"`
	Size                string            `json:"size,omitempty"`
	Functions           json.RawMessage   `json:"functions,omitempty"`
	FrequencyPenalty    *float64          `json:"frequency_penalty,omitempty"`
	PresencePenalty     *float64          `json:"presence_penalty,omitempty"`
	ResponseFormat      *ResponseFormat   `json:"response_format,omitempty"`
	EncodingFormat      json.RawMessage   `json:"encoding_format,omitempty"`
	Seed                *float64          `json:"seed,omitempty"`
	ParallelTooCalls    *bool             `json:"parallel_tool_calls,omitempty"`
	Tools               []ToolCallRequest `json:"tools,omitempty"`
	ToolChoice          any               `json:"tool_choice,omitempty"`
	FunctionCall        json.RawMessage   `json:"function_call,omitempty"`
	User                json.RawMessage   `json:"user,omitempty"`
	// ServiceTier specifies upstream service level and may affect billing.
	// This field is filtered by default and can be enabled via channel setting allow_service_tier.
	ServiceTier json.RawMessage `json:"service_tier,omitempty"`
	LogProbs    *bool           `json:"logprobs,omitempty"`
	TopLogProbs *int            `json:"top_logprobs,omitempty"`
	Dimensions  *int            `json:"dimensions,omitempty"`
	Modalities  json.RawMessage `json:"modalities,omitempty"`
	Audio       json.RawMessage `json:"audio,omitempty"`
	// 安全标识符，用于帮助 OpenAI 检测可能违反使用政策的应用程序用户
	// 注意：此字段会向 OpenAI 发送用户标识信息，默认过滤，可通过 allow_safety_identifier 开启
	SafetyIdentifier json.RawMessage `json:"safety_identifier,omitempty"`
	// Whether or not to store the output of this chat completion request for use in our model distillation or evals products.
	// 是否存储此次请求数据供 OpenAI 用于评估和优化产品
	// 注意：默认允许透传，可通过 disable_store 禁用；禁用后可能导致 Codex 无法正常使用
	Store json.RawMessage `json:"store,omitempty"`
	// Used by OpenAI to cache responses for similar requests to optimize your cache hit rates. Replaces the user field
	PromptCacheKey       string          `json:"prompt_cache_key,omitempty"`
	PromptCacheRetention json.RawMessage `json:"prompt_cache_retention,omitempty"`
	LogitBias            json.RawMessage `json:"logit_bias,omitempty"`
	Metadata             json.RawMessage `json:"metadata,omitempty"`
	Prediction           json.RawMessage `json:"prediction,omitempty"`
	// gemini
	ExtraBody json.RawMessage `json:"extra_body,omitempty"`
	//xai
	SearchParameters json.RawMessage `json:"search_parameters,omitempty"`
	// OpenAI Chat web search.
	WebSearchOptions *WebSearchOptions `json:"web_search_options,omitempty"`
	// OpenRouter Params
	Usage     json.RawMessage `json:"usage,omitempty"`
	Reasoning json.RawMessage `json:"reasoning,omitempty"`
	// Ali Qwen Params
	VlHighResolutionImages json.RawMessage `json:"vl_high_resolution_images,omitempty"`
	EnableThinking         json.RawMessage `json:"enable_thinking,omitempty"`
	ThinkingBudget         json.RawMessage `json:"thinking_budget,omitempty"`
	ChatTemplateKwargs     json.RawMessage `json:"chat_template_kwargs,omitempty"`
	EnableSearch           json.RawMessage `json:"enable_search,omitempty"`
	// ollama Params
	Think json.RawMessage `json:"think,omitempty"`
	// baidu v2
	WebSearch json.RawMessage `json:"web_search,omitempty"`
	// doubao,zhipu_v4
	THINKING json.RawMessage `json:"thinking,omitempty"`
	// pplx Params
	SearchDomainFilter     json.RawMessage `json:"search_domain_filter,omitempty"`
	SearchRecencyFilter    json.RawMessage `json:"search_recency_filter,omitempty"`
	ReturnImages           *bool           `json:"return_images,omitempty"`
	ReturnRelatedQuestions *bool           `json:"return_related_questions,omitempty"`
	SearchMode             json.RawMessage `json:"search_mode,omitempty"`
	// Minimax
	ReasoningSplit json.RawMessage `json:"reasoning_split,omitempty"`
	// vLLM
	ThinkingTokenBudget json.RawMessage `json:"thinking_token_budget,omitempty"`
	IncludeReasoning    json.RawMessage `json:"include_reasoning,omitempty"`
	MinP                json.RawMessage `json:"min_p,omitempty"`
	RepetitionPenalty   json.RawMessage `json:"repetition_penalty,omitempty"`
	StructuredOutputs   json.RawMessage `json:"structured_outputs,omitempty"`
	ReturnTokenIds      json.RawMessage `json:"return_token_ids,omitempty"`
	// SGLang OpenAI-compatible sampling and reasoning controls (v0.5.19).
	// Native /generate sampling_params and server routing controls are not chat fields.
	MinTokens            *uint           `json:"min_tokens,omitempty"`
	SeparateReasoning    json.RawMessage `json:"separate_reasoning,omitempty"`
	StreamReasoning      json.RawMessage `json:"stream_reasoning,omitempty"`
	Regex                json.RawMessage `json:"regex,omitempty"`
	EBNF                 json.RawMessage `json:"ebnf,omitempty"`
	StopTokenIDs         json.RawMessage `json:"stop_token_ids,omitempty"`
	StopRegex            json.RawMessage `json:"stop_regex,omitempty"`
	NoStopTrim           json.RawMessage `json:"no_stop_trim,omitempty"`
	IgnoreEOS            json.RawMessage `json:"ignore_eos,omitempty"`
	SkipSpecialTokens    json.RawMessage `json:"skip_special_tokens,omitempty"`
	ContinueFinalMessage json.RawMessage `json:"continue_final_message,omitempty"`
	CacheSalt            json.RawMessage `json:"cache_salt,omitempty"`

	// Internal conversion state; never serialized to an upstream protocol.
	ReasoningConversion *ReasoningConversionState `json:"-"`
}

func (r GeneralOpenAIRequest) MarshalJSON() ([]byte, error) {
	type Alias GeneralOpenAIRequest
	if !IsQwenThinkingBudgetModel(r.Model) {
		r.ThinkingBudget = nil
	}

	hasToolLoadingMessage := false
	for _, message := range r.Messages {
		if len(message.Tools) > 0 && message.Content == nil {
			hasToolLoadingMessage = true
			break
		}
	}
	if !hasToolLoadingMessage {
		return kitutil.Marshal((*Alias)(&r))
	}

	// Kimi K3 dynamic tool loading: a system message that carries tools must not
	// carry a content key at all, otherwise the upstream rejects it. Only those
	// messages drop the key; every other message keeps emitting "content": null.
	type toolLoadingMessage struct {
		Message
		Content any `json:"content,omitempty"`
	}
	messages := make([]json.RawMessage, 0, len(r.Messages))
	for _, message := range r.Messages {
		var encoded []byte
		var err error
		if len(message.Tools) > 0 && message.Content == nil {
			encoded, err = kitutil.Marshal(toolLoadingMessage{Message: message})
		} else {
			encoded, err = kitutil.Marshal(message)
		}
		if err != nil {
			return nil, err
		}
		messages = append(messages, encoded)
	}
	return kitutil.Marshal(struct {
		*Alias
		Messages []json.RawMessage `json:"messages,omitempty"`
	}{Alias: (*Alias)(&r), Messages: messages})
}

func (r *GeneralOpenAIRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var tokenCountMeta types.TokenCountMeta
	var texts = make([]string, 0)
	var fileMeta = make([]*types.FileMeta, 0)

	if r.Prompt != nil {
		switch v := r.Prompt.(type) {
		case string:
			texts = append(texts, v)
		case []any:
			for _, item := range v {
				if str, ok := item.(string); ok {
					texts = append(texts, str)
				}
			}
		default:
			texts = append(texts, fmt.Sprintf("%v", r.Prompt))
		}
	}

	if r.Input != nil {
		inputs := r.ParseInput()
		texts = append(texts, inputs...)
	}

	maxTokens := lo.FromPtrOr(r.MaxTokens, uint(0))
	maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
	if maxCompletionTokens > maxTokens {
		tokenCountMeta.MaxTokens = int(maxCompletionTokens)
	} else {
		tokenCountMeta.MaxTokens = int(maxTokens)
	}

	var dynamicTools []ToolCallRequest
	for _, message := range r.Messages {
		tokenCountMeta.MessagesCount++
		texts = append(texts, message.Role)
		if len(message.Tools) > 0 {
			// Kimi K3 dynamic tool loading: tools declared on a message are
			// visible to the model and are counted like top-level tools.
			var messageTools []ToolCallRequest
			if err := kitutil.Unmarshal(message.Tools, &messageTools); err == nil {
				dynamicTools = append(dynamicTools, messageTools...)
			}
		}
		if message.Content != nil {
			if message.Name != nil {
				tokenCountMeta.NameCount++
				texts = append(texts, *message.Name)
			}
			arrayContent := message.ParseContent()
			for _, m := range arrayContent {
				source := m.ToFileSource()
				if source != nil {
					meta := &types.FileMeta{Source: source}
					switch m.Type {
					case ContentTypeImageURL:
						meta.FileType = types.FileTypeImage
						if img := m.GetImageMedia(); img != nil {
							meta.Detail = img.Detail
						}
					case ContentTypeInputAudio:
						meta.FileType = types.FileTypeAudio
					case ContentTypeFile:
						meta.FileType = types.FileTypeFile
					case ContentTypeVideoUrl:
						meta.FileType = types.FileTypeVideo
					}
					fileMeta = append(fileMeta, meta)
				} else if m.Type == ContentTypeText {
					texts = append(texts, m.Text)
				}
			}
		}
	}

	tools := r.Tools
	if len(dynamicTools) > 0 {
		tools = append(dynamicTools, r.Tools...)
	}
	for _, tool := range tools {
		tokenCountMeta.ToolsCount++
		texts = append(texts, tool.Function.Name)
		if tool.Function.Description != "" {
			texts = append(texts, tool.Function.Description)
		}
		if tool.Function.Parameters != nil {
			texts = append(texts, fmt.Sprintf("%v", tool.Function.Parameters))
		}
	}
	//toolTokens := CountTokenInput(countStr, request.Model)
	//tkm += 8
	//tkm += toolTokens
	tokenCountMeta.CombineText = strings.Join(texts, "\n")
	tokenCountMeta.Files = fileMeta
	return &tokenCountMeta
}

func (r *GeneralOpenAIRequest) IsStream(c *http.Request) bool {
	return lo.FromPtrOr(r.Stream, false)
}

func (r *GeneralOpenAIRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}

func (r *GeneralOpenAIRequest) ToMap() map[string]any {
	result := make(map[string]any)
	data, _ := kitutil.Marshal(r)
	_ = kitutil.Unmarshal(data, &result)
	return result
}

func IsOpenAIReasoningOModel(modelName string) bool {
	return strings.HasPrefix(modelName, "o1") ||
		strings.HasPrefix(modelName, "o3") ||
		strings.HasPrefix(modelName, "o4")
}

// IsOpenAIGPT5Model identifies the GPT-5 family, independently of request capabilities.
func IsOpenAIGPT5Model(modelName string) bool {
	return modelName == "gpt-5" || strings.HasPrefix(modelName, "gpt-5-") || strings.HasPrefix(modelName, "gpt-5.")
}

// OpenAIChatCapabilities describes independent Chat Completions compatibility rules.
type OpenAIChatCapabilities struct {
	UseMaxCompletionTokens bool
	UseDeveloperRole       bool
	SupportsTemperature    bool
	SupportsTopP           bool
	SupportsLogProbs       bool // Also governs top_logprobs.
}

// GetOpenAIChatCapabilities uses the mapped model and resolved reasoning effort.
// Unrecognized models retain their parameters; future GPT generations do not
// automatically inherit the restrictions of existing models.
func GetOpenAIChatCapabilities(modelName, reasoningEffort string) OpenAIChatCapabilities {
	capabilities := OpenAIChatCapabilities{
		SupportsTemperature: true,
		SupportsTopP:        true,
		SupportsLogProbs:    true,
	}
	if IsOpenAIReasoningOModel(modelName) {
		capabilities.UseMaxCompletionTokens = true
		capabilities.UseDeveloperRole = !strings.HasPrefix(modelName, "o1-mini") && !strings.HasPrefix(modelName, "o1-preview")
		capabilities.SupportsTemperature = false
		return capabilities
	}

	isGPT5Model := IsOpenAIGPT5Model(modelName)
	if !isGPT5Model && !isOpenAIModelSnapshot(modelName, "gpt-6-astra") {
		return capabilities
	}
	capabilities.UseMaxCompletionTokens = true
	capabilities.UseDeveloperRole = true

	// These standard GPT-5 models default to none and support sampling only
	// without reasoning. Named variants (pro, codex, chat-latest, etc.) do not
	// inherit this exception. GPT-6 Astra never supports these parameters.
	// https://developers.openai.com/api/docs/guides/latest-model?model=gpt-5.2
	// https://developers.openai.com/api/docs/guides/latest-model?model=gpt-5.4
	// https://developers.openai.com/api/docs/guides/latest-model?model=gpt-6-astra
	supportsSampling := false
	if isGPT5Model && (reasoningEffort == "" || reasoningEffort == "none") {
		for _, model := range []string{"gpt-5.1", "gpt-5.2", "gpt-5.4"} {
			if isOpenAIModelSnapshot(modelName, model) {
				supportsSampling = true
				break
			}
		}
	}
	capabilities.SupportsTemperature = supportsSampling
	capabilities.SupportsTopP = supportsSampling
	capabilities.SupportsLogProbs = supportsSampling
	return capabilities
}

func isOpenAIModelSnapshot(modelName, baseModel string) bool {
	if modelName == baseModel {
		return true
	}
	snapshot, ok := strings.CutPrefix(modelName, baseModel+"-")
	if !ok {
		return false
	}
	_, err := time.Parse(time.DateOnly, snapshot)
	return err == nil
}

func IsQwenThinkingBudgetModel(modelName string) bool {
	normalized := strings.ToLower(strings.TrimSpace(modelName))
	return strings.HasPrefix(normalized, "qwen") ||
		strings.Contains(normalized, "/qwen") ||
		strings.HasPrefix(normalized, "qwq") ||
		strings.Contains(normalized, "/qwq")
}

func (r *GeneralOpenAIRequest) GetSystemRoleName() string {
	if GetOpenAIChatCapabilities(r.Model, r.ReasoningEffort).UseDeveloperRole {
		return "developer"
	}
	return "system"
}

const CustomType = "custom"

type ToolCallRequest struct {
	ID       string          `json:"id,omitempty"`
	Type     string          `json:"type"`
	Function FunctionRequest `json:"function"`
	Custom   json.RawMessage `json:"custom,omitempty"`
}

type FunctionRequest struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name"`
	Parameters  any    `json:"parameters,omitempty"`
	Arguments   string `json:"arguments,omitempty"`
	Strict      *bool  `json:"strict,omitempty"`
}

type StreamOptions struct {
	IncludeUsage bool `json:"include_usage,omitempty"`
	// IncludeObfuscation is only for /v1/responses stream payload.
	// This field is filtered by default and can be enabled via channel setting allow_include_obfuscation.
	IncludeObfuscation bool `json:"include_obfuscation,omitempty"`
	// ContinuousUsageStats is a vLLM stream_options extension that emits
	// usage on intermediate chunks. Optional so an explicit false is kept.
	// ContinuousUsageStats *bool `json:"continuous_usage_stats,omitempty"`
}

func (r *GeneralOpenAIRequest) GetMaxTokens() uint {
	maxCompletionTokens := lo.FromPtrOr(r.MaxCompletionTokens, uint(0))
	if maxCompletionTokens != 0 {
		return maxCompletionTokens
	}
	return lo.FromPtrOr(r.MaxTokens, uint(0))
}

func (r *GeneralOpenAIRequest) ParseInput() []string {
	if r.Input == nil {
		return nil
	}
	var input []string
	switch r.Input.(type) {
	case string:
		input = []string{r.Input.(string)}
	case []any:
		input = make([]string, 0, len(r.Input.([]any)))
		for _, item := range r.Input.([]any) {
			if str, ok := item.(string); ok {
				input = append(input, str)
			}
		}
	}
	return input
}

type Message struct {
	Role             string          `json:"role"`
	Content          any             `json:"content"`
	Name             *string         `json:"name,omitempty"`
	Prefix           *bool           `json:"prefix,omitempty"`
	ReasoningContent *string         `json:"reasoning_content,omitempty"`
	Reasoning        *string         `json:"reasoning,omitempty"`
	ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
	ToolCallId       string          `json:"tool_call_id,omitempty"`
	// Tools carries Kimi K3 dynamic tool loading declarations on a system message.
	// Same shape as the top-level tools array; passthrough-only for OpenAI-compatible upstreams.
	Tools json.RawMessage `json:"tools,omitempty"`
	// Annotations is an official Chat response field. Keeping it on the shared
	// message type also preserves annotations when clients replay assistant output.
	Annotations   json.RawMessage `json:"annotations,omitempty"`
	parsedContent []MediaContent
	//parsedStringContent *string
}

type MediaContent struct {
	Type       string `json:"type"`
	Text       string `json:"text,omitempty"`
	ImageUrl   any    `json:"image_url,omitempty"`
	InputAudio any    `json:"input_audio,omitempty"`
	File       any    `json:"file,omitempty"`
	VideoUrl   any    `json:"video_url,omitempty"`
	// OpenRouter Params
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
}

func (m *MediaContent) GetImageMedia() *MessageImageUrl {
	if m.ImageUrl != nil {
		if _, ok := m.ImageUrl.(*MessageImageUrl); ok {
			return m.ImageUrl.(*MessageImageUrl)
		}
		if itemMap, ok := m.ImageUrl.(map[string]any); ok {
			out := &MessageImageUrl{
				Url:      kitutil.Interface2String(itemMap["url"]),
				Detail:   kitutil.Interface2String(itemMap["detail"]),
				MimeType: kitutil.Interface2String(itemMap["mime_type"]),
			}
			return out
		}
	}
	return nil
}

func (m *MediaContent) GetInputAudio() *MessageInputAudio {
	if m.InputAudio != nil {
		if _, ok := m.InputAudio.(*MessageInputAudio); ok {
			return m.InputAudio.(*MessageInputAudio)
		}
		if itemMap, ok := m.InputAudio.(map[string]any); ok {
			out := &MessageInputAudio{
				Data:   kitutil.Interface2String(itemMap["data"]),
				Format: kitutil.Interface2String(itemMap["format"]),
			}
			return out
		}
	}
	return nil
}

func (m *MediaContent) GetFile() *MessageFile {
	if m.File != nil {
		if _, ok := m.File.(*MessageFile); ok {
			return m.File.(*MessageFile)
		}
		if itemMap, ok := m.File.(map[string]any); ok {
			out := &MessageFile{
				FileName: kitutil.Interface2String(itemMap["file_name"]),
				FileData: kitutil.Interface2String(itemMap["file_data"]),
				FileId:   kitutil.Interface2String(itemMap["file_id"]),
			}
			return out
		}
	}
	return nil
}

func (m *MediaContent) GetVideoUrl() *MessageVideoUrl {
	if m.VideoUrl != nil {
		if _, ok := m.VideoUrl.(*MessageVideoUrl); ok {
			return m.VideoUrl.(*MessageVideoUrl)
		}
		if itemMap, ok := m.VideoUrl.(map[string]any); ok {
			out := &MessageVideoUrl{
				Url: kitutil.Interface2String(itemMap["url"]),
			}
			return out
		}
	}
	return nil
}

func (m *MediaContent) ToFileSource() types.FileSource {
	switch m.Type {
	case ContentTypeImageURL:
		img := m.GetImageMedia()
		if img == nil || img.Url == "" {
			return nil
		}
		return types.NewFileSourceFromData(img.Url, img.MimeType)
	case ContentTypeInputAudio:
		audio := m.GetInputAudio()
		if audio == nil || audio.Data == "" {
			return nil
		}
		mimeType := ""
		if audio.Format != "" {
			mimeType = "audio/" + audio.Format
		}
		return types.NewFileSourceFromData(audio.Data, mimeType)
	case ContentTypeFile:
		file := m.GetFile()
		if file == nil || file.FileData == "" {
			return nil
		}
		return types.NewFileSourceFromData(file.FileData, "")
	case ContentTypeVideoUrl:
		video := m.GetVideoUrl()
		if video == nil || video.Url == "" {
			return nil
		}
		return types.NewFileSourceFromData(video.Url, "")
	}
	return nil
}

type MessageImageUrl struct {
	Url      string `json:"url"`
	Detail   string `json:"detail,omitempty"`
	MimeType string
}

func (m *MessageImageUrl) IsRemoteImage() bool {
	return strings.HasPrefix(m.Url, "http")
}

type MessageInputAudio struct {
	Data   string `json:"data"` //base64
	Format string `json:"format"`
}

type MessageFile struct {
	FileName string `json:"filename,omitempty"`
	FileData string `json:"file_data,omitempty"`
	FileId   string `json:"file_id,omitempty"`
}

type MessageVideoUrl struct {
	Url string `json:"url"`
}

const (
	ContentTypeText       = "text"
	ContentTypeImageURL   = "image_url"
	ContentTypeInputAudio = "input_audio"
	ContentTypeFile       = "file"
	ContentTypeVideoUrl   = "video_url" // 阿里百炼视频识别
	//ContentTypeAudioUrl   = "audio_url"
)

func (m *Message) GetReasoningContent() string {
	if m.ReasoningContent == nil && m.Reasoning == nil {
		return ""
	}
	if m.ReasoningContent != nil {
		return *m.ReasoningContent
	}
	return *m.Reasoning
}

func (m *Message) GetPrefix() bool {
	if m.Prefix == nil {
		return false
	}
	return *m.Prefix
}

func (m *Message) SetPrefix(prefix bool) {
	m.Prefix = &prefix
}

func (m *Message) ParseToolCalls() []ToolCallRequest {
	if m.ToolCalls == nil {
		return nil
	}
	var toolCalls []ToolCallRequest
	if err := kitutil.Unmarshal(m.ToolCalls, &toolCalls); err == nil {
		return toolCalls
	}
	return toolCalls
}

func (m *Message) SetToolCalls(toolCalls any) {
	toolCallsJson, _ := kitutil.Marshal(toolCalls)
	m.ToolCalls = toolCallsJson
}

func (m *Message) StringContent() string {
	switch m.Content.(type) {
	case string:
		return m.Content.(string)
	case []any:
		var contentStr strings.Builder
		for _, contentItem := range m.Content.([]any) {
			contentMap, ok := contentItem.(map[string]any)
			if !ok {
				continue
			}
			if contentMap["type"] == ContentTypeText {
				if subStr, ok := contentMap["text"].(string); ok {
					contentStr.WriteString(subStr)
				}
			}
		}
		return contentStr.String()
	}

	return ""
}

func (m *Message) SetNullContent() {
	m.Content = nil
	m.parsedContent = nil
}

func (m *Message) SetStringContent(content string) {
	m.Content = content
	m.parsedContent = nil
}

func (m *Message) SetMediaContent(content []MediaContent) {
	m.Content = content
	m.parsedContent = content
}

func (m *Message) IsStringContent() bool {
	_, ok := m.Content.(string)
	if ok {
		return true
	}
	return false
}

func (m *Message) ParseContent() []MediaContent {
	if m.Content == nil {
		return nil
	}
	if len(m.parsedContent) > 0 {
		return m.parsedContent
	}

	var contentList []MediaContent
	// 先尝试解析为字符串
	content, ok := m.Content.(string)
	if ok {
		contentList = []MediaContent{{
			Type: ContentTypeText,
			Text: content,
		}}
		m.parsedContent = contentList
		return contentList
	}

	if content, ok := m.Content.([]MediaContent); ok {
		m.parsedContent = content
		return content
	}

	// 尝试解析为数组
	//var arrayContent []map[string]interface{}

	arrayContent, ok := m.Content.([]any)
	if !ok {
		return contentList
	}

	for _, contentItemAny := range arrayContent {
		mediaItem, ok := contentItemAny.(MediaContent)
		if ok {
			contentList = append(contentList, mediaItem)
			continue
		}

		contentItem, ok := contentItemAny.(map[string]any)
		if !ok {
			continue
		}
		contentType, ok := contentItem["type"].(string)
		if !ok {
			continue
		}

		switch contentType {
		case ContentTypeText:
			if text, ok := contentItem["text"].(string); ok {
				contentList = append(contentList, MediaContent{
					Type: ContentTypeText,
					Text: text,
				})
			}

		case ContentTypeImageURL:
			imageUrl := contentItem["image_url"]
			temp := &MessageImageUrl{
				Detail: "high",
			}
			switch v := imageUrl.(type) {
			case string:
				temp.Url = v
			case map[string]any:
				url, ok1 := v["url"].(string)
				detail, ok2 := v["detail"].(string)
				if ok2 {
					temp.Detail = detail
				}
				if ok1 {
					temp.Url = url
				}
			}
			contentList = append(contentList, MediaContent{
				Type:     ContentTypeImageURL,
				ImageUrl: temp,
			})

		case ContentTypeInputAudio:
			if audioData, ok := contentItem["input_audio"].(map[string]any); ok {
				data, ok1 := audioData["data"].(string)
				format, ok2 := audioData["format"].(string)
				if ok1 && ok2 {
					temp := &MessageInputAudio{
						Data:   data,
						Format: format,
					}
					contentList = append(contentList, MediaContent{
						Type:       ContentTypeInputAudio,
						InputAudio: temp,
					})
				}
			}
		case ContentTypeFile:
			if fileData, ok := contentItem["file"].(map[string]any); ok {
				fileId, ok3 := fileData["file_id"].(string)
				if ok3 {
					contentList = append(contentList, MediaContent{
						Type: ContentTypeFile,
						File: &MessageFile{
							FileId: fileId,
						},
					})
				} else {
					fileName, ok1 := fileData["filename"].(string)
					fileDataStr, ok2 := fileData["file_data"].(string)
					if ok1 && ok2 {
						contentList = append(contentList, MediaContent{
							Type: ContentTypeFile,
							File: &MessageFile{
								FileName: fileName,
								FileData: fileDataStr,
							},
						})
					}
				}
			}
		case ContentTypeVideoUrl:
			if videoUrl, ok := contentItem["video_url"].(string); ok {
				contentList = append(contentList, MediaContent{
					Type: ContentTypeVideoUrl,
					VideoUrl: &MessageVideoUrl{
						Url: videoUrl,
					},
				})
			}
		}
	}

	if len(contentList) > 0 {
		m.parsedContent = contentList
	}
	return contentList
}

// old code
/*func (m *Message) StringContent() string {
	if m.parsedStringContent != nil {
		return *m.parsedStringContent
	}

	var stringContent string
	if err := kitutil.Unmarshal(m.Content, &stringContent); err == nil {
		m.parsedStringContent = &stringContent
		return stringContent
	}

	contentStr := new(strings.Builder)
	arrayContent := m.ParseContent()
	for _, content := range arrayContent {
		if content.Type == ContentTypeText {
			contentStr.WriteString(content.Text)
		}
	}
	stringContent = contentStr.String()
	m.parsedStringContent = &stringContent

	return stringContent
}

func (m *Message) SetNullContent() {
	m.Content = nil
	m.parsedStringContent = nil
	m.parsedContent = nil
}

func (m *Message) SetStringContent(content string) {
	jsonContent, _ := kitutil.Marshal(content)
	m.Content = jsonContent
	m.parsedStringContent = &content
	m.parsedContent = nil
}

func (m *Message) SetMediaContent(content []MediaContent) {
	jsonContent, _ := kitutil.Marshal(content)
	m.Content = jsonContent
	m.parsedContent = nil
	m.parsedStringContent = nil
}

func (m *Message) IsStringContent() bool {
	if m.parsedStringContent != nil {
		return true
	}
	var stringContent string
	if err := kitutil.Unmarshal(m.Content, &stringContent); err == nil {
		m.parsedStringContent = &stringContent
		return true
	}
	return false
}

func (m *Message) ParseContent() []MediaContent {
	if m.parsedContent != nil {
		return m.parsedContent
	}

	var contentList []MediaContent

	// 先尝试解析为字符串
	var stringContent string
	if err := kitutil.Unmarshal(m.Content, &stringContent); err == nil {
		contentList = []MediaContent{{
			Type: ContentTypeText,
			Text: stringContent,
		}}
		m.parsedContent = contentList
		return contentList
	}

	// 尝试解析为数组
	var arrayContent []map[string]interface{}
	if err := kitutil.Unmarshal(m.Content, &arrayContent); err == nil {
		for _, contentItem := range arrayContent {
			contentType, ok := contentItem["type"].(string)
			if !ok {
				continue
			}

			switch contentType {
			case ContentTypeText:
				if text, ok := contentItem["text"].(string); ok {
					contentList = append(contentList, MediaContent{
						Type: ContentTypeText,
						Text: text,
					})
				}

			case ContentTypeImageURL:
				imageUrl := contentItem["image_url"]
				temp := &MessageImageUrl{
					Detail: "high",
				}
				switch v := imageUrl.(type) {
				case string:
					temp.Url = v
				case map[string]interface{}:
					url, ok1 := v["url"].(string)
					detail, ok2 := v["detail"].(string)
					if ok2 {
						temp.Detail = detail
					}
					if ok1 {
						temp.Url = url
					}
				}
				contentList = append(contentList, MediaContent{
					Type:     ContentTypeImageURL,
					ImageUrl: temp,
				})

			case ContentTypeInputAudio:
				if audioData, ok := contentItem["input_audio"].(map[string]interface{}); ok {
					data, ok1 := audioData["data"].(string)
					format, ok2 := audioData["format"].(string)
					if ok1 && ok2 {
						temp := &MessageInputAudio{
							Data:   data,
							Format: format,
						}
						contentList = append(contentList, MediaContent{
							Type:       ContentTypeInputAudio,
							InputAudio: temp,
						})
					}
				}
			case ContentTypeFile:
				if fileData, ok := contentItem["file"].(map[string]interface{}); ok {
					fileId, ok3 := fileData["file_id"].(string)
					if ok3 {
						contentList = append(contentList, MediaContent{
							Type: ContentTypeFile,
							File: &MessageFile{
								FileId: fileId,
							},
						})
					} else {
						fileName, ok1 := fileData["filename"].(string)
						fileDataStr, ok2 := fileData["file_data"].(string)
						if ok1 && ok2 {
							contentList = append(contentList, MediaContent{
								Type: ContentTypeFile,
								File: &MessageFile{
									FileName: fileName,
									FileData: fileDataStr,
								},
							})
						}
					}
				}
			case ContentTypeVideoUrl:
				if videoUrl, ok := contentItem["video_url"].(string); ok {
					contentList = append(contentList, MediaContent{
						Type: ContentTypeVideoUrl,
						VideoUrl: &MessageVideoUrl{
							Url: videoUrl,
						},
					})
				}
			}
		}
	}

	if len(contentList) > 0 {
		m.parsedContent = contentList
	}
	return contentList
}*/

type WebSearchOptions struct {
	SearchContextSize string          `json:"search_context_size,omitempty"`
	UserLocation      json.RawMessage `json:"user_location,omitempty"`
}

// https://platform.openai.com/docs/api-reference/responses/create
type OpenAIResponsesRequest struct {
	Model   string          `json:"model"`
	Input   json.RawMessage `json:"input,omitempty"`
	Include json.RawMessage `json:"include,omitempty"`
	// 在后台运行推理，暂时还不支持依赖的接口
	// Background         json.RawMessage `json:"background,omitempty"`
	Conversation      json.RawMessage `json:"conversation,omitempty"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
	Instructions      json.RawMessage `json:"instructions,omitempty"`
	MaxOutputTokens   *uint           `json:"max_output_tokens,omitempty"`
	TopLogProbs       *int            `json:"top_logprobs,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	Moderation        json.RawMessage `json:"moderation,omitempty"`
	ParallelToolCalls json.RawMessage `json:"parallel_tool_calls,omitempty"`
	// FrequencyPenalty/PresencePenalty are not part of the official OpenAI
	// Responses API; they are forwarded verbatim for OpenAI-compatible upstreams
	// (e.g. vLLM) that accept them.
	FrequencyPenalty   json.RawMessage `json:"frequency_penalty,omitempty"`
	PresencePenalty    json.RawMessage `json:"presence_penalty,omitempty"`
	PreviousResponseID string          `json:"previous_response_id,omitempty"`
	Reasoning          *Reasoning      `json:"reasoning,omitempty"`
	// ServiceTier specifies upstream service level and may affect billing.
	// This field is filtered by default and can be enabled via channel setting allow_service_tier.
	ServiceTier string `json:"service_tier,omitempty"`
	// Store controls whether upstream may store request/response data.
	// This field is allowed by default and can be disabled via channel setting disable_store.
	Store                json.RawMessage `json:"store,omitempty"`
	PromptCacheKey       json.RawMessage `json:"prompt_cache_key,omitempty"`
	PromptCacheOptions   json.RawMessage `json:"prompt_cache_options,omitempty"`
	PromptCacheRetention json.RawMessage `json:"prompt_cache_retention,omitempty"`
	// SafetyIdentifier carries client identity for policy abuse detection.
	// This field is filtered by default and can be enabled via channel setting allow_safety_identifier.
	SafetyIdentifier json.RawMessage `json:"safety_identifier,omitempty"`
	Stream           *bool           `json:"stream,omitempty"`
	StreamOptions    *StreamOptions  `json:"stream_options,omitempty"`
	Temperature      *float64        `json:"temperature,omitempty"`
	Text             json.RawMessage `json:"text,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	Tools            json.RawMessage `json:"tools,omitempty"` // 需要处理的参数很少，MCP 参数太多不确定，所以用 map
	TopP             *float64        `json:"top_p,omitempty"`
	Truncation       json.RawMessage `json:"truncation,omitempty"`
	User             json.RawMessage `json:"user,omitempty"`
	MaxToolCalls     *uint           `json:"max_tool_calls,omitempty"`
	Prompt           json.RawMessage `json:"prompt,omitempty"`
	// Codex Responses metadata/client_metadata:
	// https://github.com/openai/codex/commit/14df0e8833aad0d6d78287954b61ffac67af936c
	ClientMetadata json.RawMessage `json:"client_metadata,omitempty"`
	// qwen
	EnableThinking json.RawMessage `json:"enable_thinking,omitempty"`
	ThinkingBudget json.RawMessage `json:"thinking_budget,omitempty"`
	// vLLM
	ChatTemplateKwargs json.RawMessage `json:"chat_template_kwargs,omitempty"`
	// SGLang Responses sampling extensions.
	TopK              json.RawMessage `json:"top_k,omitempty"`
	MinP              json.RawMessage `json:"min_p,omitempty"`
	RepetitionPenalty json.RawMessage `json:"repetition_penalty,omitempty"`
	Stop              json.RawMessage `json:"stop,omitempty"`
	CacheSalt         json.RawMessage `json:"cache_salt,omitempty"`
	// perplexity
	Preset json.RawMessage `json:"preset,omitempty"`

	// Internal conversion state; never serialized to an upstream protocol.
	ReasoningConversion *ReasoningConversionState `json:"-"`
}

func (r OpenAIResponsesRequest) MarshalJSON() ([]byte, error) {
	type Alias OpenAIResponsesRequest
	if !IsQwenThinkingBudgetModel(r.Model) {
		r.ThinkingBudget = nil
	}
	return kitutil.Marshal((*Alias)(&r))
}

func (r *OpenAIResponsesRequest) GetTokenCountMeta() *types.TokenCountMeta {
	var fileMeta = make([]*types.FileMeta, 0)
	var texts = make([]string, 0)

	if r.Input != nil {
		inputs := r.ParseInput()
		for _, input := range inputs {
			if input.Type == "input_image" {
				if input.ImageUrl != "" {
					fileMeta = append(fileMeta, &types.FileMeta{
						FileType: types.FileTypeImage,
						Source:   types.NewFileSourceFromData(input.ImageUrl, ""),
						Detail:   input.Detail,
					})
				}
			} else if input.Type == "input_file" {
				if input.FileUrl != "" {
					fileMeta = append(fileMeta, &types.FileMeta{
						FileType: types.FileTypeFile,
						Source:   types.NewFileSourceFromData(input.FileUrl, ""),
					})
				}
			} else {
				texts = append(texts, input.Text)
			}
		}
	}

	if len(r.Instructions) > 0 {
		texts = append(texts, string(r.Instructions))
	}

	if len(r.Metadata) > 0 {
		texts = append(texts, string(r.Metadata))
	}

	if len(r.Text) > 0 {
		texts = append(texts, string(r.Text))
	}

	if len(r.ToolChoice) > 0 {
		texts = append(texts, string(r.ToolChoice))
	}

	if len(r.Prompt) > 0 {
		texts = append(texts, string(r.Prompt))
	}

	if len(r.Tools) > 0 {
		texts = append(texts, string(r.Tools))
	}

	return &types.TokenCountMeta{
		CombineText: strings.Join(texts, "\n"),
		Files:       fileMeta,
		MaxTokens:   int(lo.FromPtrOr(r.MaxOutputTokens, uint(0))),
	}
}

func (r *OpenAIResponsesRequest) IsStream(c *http.Request) bool {
	return lo.FromPtrOr(r.Stream, false)
}

func (r *OpenAIResponsesRequest) SetModelName(modelName string) {
	if modelName != "" {
		r.Model = modelName
	}
}

func (r *OpenAIResponsesRequest) GetToolsMap() []map[string]any {
	var toolsMap []map[string]any
	if len(r.Tools) > 0 {
		_ = kitutil.Unmarshal(r.Tools, &toolsMap)
	}
	return toolsMap
}

type Reasoning struct {
	Effort  string          `json:"effort,omitempty"`
	Summary string          `json:"summary,omitempty"`
	Mode    json.RawMessage `json:"mode,omitempty"`
	Context json.RawMessage `json:"context,omitempty"`
}

type Input struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"`
}

type MediaInput struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	FileUrl  string `json:"file_url,omitempty"`
	ImageUrl string `json:"image_url,omitempty"`
	Detail   string `json:"detail,omitempty"` // 仅 input_image 有效
}

// ParseInput parses the Responses API `input` field into a normalized slice of MediaInput.
// Reference implementation mirrors Message.ParseContent:
//   - input can be a string, treated as an input_text item
//   - input can be an array of objects with a `type` field
//     supported types: input_text, input_image, input_file
func (r *OpenAIResponsesRequest) ParseInput() []MediaInput {
	if r.Input == nil {
		return nil
	}

	var mediaInputs []MediaInput

	// Try string first
	// if str, ok := kitutil.GetJsonType(r.Input); ok {
	// 	inputs = append(inputs, MediaInput{Type: "input_text", Text: str})
	// 	return inputs
	// }
	if kitutil.GetJsonType(r.Input) == "string" {
		var str string
		_ = kitutil.Unmarshal(r.Input, &str)
		mediaInputs = append(mediaInputs, MediaInput{Type: "input_text", Text: str})
		return mediaInputs
	}

	// Try array of parts
	if kitutil.GetJsonType(r.Input) == "array" {
		var inputs []Input
		_ = kitutil.Unmarshal(r.Input, &inputs)
		for _, input := range inputs {
			if kitutil.GetJsonType(input.Content) == "string" {
				var str string
				_ = kitutil.Unmarshal(input.Content, &str)
				mediaInputs = append(mediaInputs, MediaInput{Type: "input_text", Text: str})
			}

			if kitutil.GetJsonType(input.Content) == "array" {
				var array []any
				_ = kitutil.Unmarshal(input.Content, &array)
				for _, itemAny := range array {
					// Already parsed MediaContent
					if media, ok := itemAny.(MediaInput); ok {
						mediaInputs = append(mediaInputs, media)
						continue
					}

					// Generic map
					item, ok := itemAny.(map[string]any)
					if !ok {
						continue
					}

					typeVal, ok := item["type"].(string)
					if !ok {
						continue
					}
					switch typeVal {
					case "input_text":
						text, _ := item["text"].(string)
						mediaInputs = append(mediaInputs, MediaInput{Type: "input_text", Text: text})
					case "input_image":
						// image_url may be string or object with url field
						var imageUrl string
						switch v := item["image_url"].(type) {
						case string:
							imageUrl = v
						case map[string]any:
							if url, ok := v["url"].(string); ok {
								imageUrl = url
							}
						}
						mediaInputs = append(mediaInputs, MediaInput{Type: "input_image", ImageUrl: imageUrl})
					case "input_file":
						// file_url may be string or object with url field
						var fileUrl string
						switch v := item["file_url"].(type) {
						case string:
							fileUrl = v
						case map[string]any:
							if url, ok := v["url"].(string); ok {
								fileUrl = url
							}
						}
						mediaInputs = append(mediaInputs, MediaInput{Type: "input_file", FileUrl: fileUrl})
					}
				}
			}
		}
	}

	return mediaInputs
}
