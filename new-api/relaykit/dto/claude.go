package dto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

type ClaudeMetadata struct {
	UserId string `json:"user_id"`
}

type ClaudeMediaMessage struct {
	Type         string               `json:"type,omitempty"`
	Text         *string              `json:"text,omitempty"`
	Model        string               `json:"model,omitempty"`
	Source       *ClaudeMessageSource `json:"source,omitempty"`
	Usage        *ClaudeUsage         `json:"usage,omitempty"`
	StopReason   *string              `json:"stop_reason,omitempty"`
	PartialJson  *string              `json:"partial_json,omitempty"`
	Role         string               `json:"role,omitempty"`
	Thinking     *string              `json:"thinking,omitempty"`
	Data         string               `json:"data,omitempty"`
	Signature    string               `json:"signature,omitempty"`
	Delta        string               `json:"delta,omitempty"`
	CacheControl json.RawMessage      `json:"cache_control,omitempty"`

	// Text blocks and citations_delta events.
	Citations json.RawMessage `json:"citations,omitempty"`
	Citation  json.RawMessage `json:"citation,omitempty"`

	// Server-tool and tool-result blocks.
	Caller     json.RawMessage `json:"caller,omitempty"`
	ServerName string          `json:"server_name,omitempty"`
	IsError    *bool           `json:"is_error,omitempty"`
	// ErrorCode is a relaykit compatibility extension. Claude places provider
	// error codes inside nested tool-result error content.
	ErrorCode string `json:"error_code,omitempty"`

	// Tool-use and tool-result blocks.
	Id        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	Content   any    `json:"content,omitempty"`
	ToolUseId string `json:"tool_use_id,omitempty"`
}

func (c *ClaudeMediaMessage) SetText(s string) {
	c.Text = &s
}

func (c *ClaudeMediaMessage) GetText() string {
	if c.Text == nil {
		return ""
	}
	return *c.Text
}

func (c *ClaudeMediaMessage) IsStringContent() bool {
	if c.Content == nil {
		return false
	}
	_, ok := c.Content.(string)
	if ok {
		return true
	}
	return false
}

func (c *ClaudeMediaMessage) GetStringContent() string {
	if c.Content == nil {
		return ""
	}
	switch c.Content.(type) {
	case string:
		return c.Content.(string)
	case []any:
		var contentStr strings.Builder
		for _, contentItem := range c.Content.([]any) {
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

func (c *ClaudeMediaMessage) GetJsonRowString() string {
	jsonContent, _ := kitutil.Marshal(c)
	return string(jsonContent)
}

func (c *ClaudeMediaMessage) SetContent(content any) {
	c.Content = content
}

func (c *ClaudeMediaMessage) ParseMediaContent() []ClaudeMediaMessage {
	mediaContent, _ := kitutil.Any2Type[[]ClaudeMediaMessage](c.Content)
	return mediaContent
}

func (m *ClaudeMediaMessage) ToFileSource() types.FileSource {
	if m.Source == nil {
		return nil
	}
	data := m.Source.Url
	if data == "" {
		data = kitutil.Interface2String(m.Source.Data)
	}
	if data == "" {
		return nil
	}
	return types.NewFileSourceFromData(data, m.Source.MediaType)
}

type ClaudeMessageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      any    `json:"data,omitempty"`
	Url       string `json:"url,omitempty"`
}

type ClaudeMessage struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
	// OutputConfig carries per-message effort (beta): an effort-only system
	// message has empty content and the new level in output_config.effort.
	OutputConfig json.RawMessage `json:"output_config,omitempty"`
}

func (c *ClaudeMessage) IsStringContent() bool {
	if c.Content == nil {
		return false
	}
	_, ok := c.Content.(string)
	return ok
}

func (c *ClaudeMessage) GetStringContent() string {
	if c.Content == nil {
		return ""
	}
	switch c.Content.(type) {
	case string:
		return c.Content.(string)
	case []any:
		var contentStr strings.Builder
		for _, contentItem := range c.Content.([]any) {
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

func (c *ClaudeMessage) SetStringContent(content string) {
	c.Content = content
}

func (c *ClaudeMessage) SetContent(content any) {
	c.Content = content
}

func (c *ClaudeMessage) ParseContent() ([]ClaudeMediaMessage, error) {
	return kitutil.Any2Type[[]ClaudeMediaMessage](c.Content)
}

type Tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"input_schema"`
	Strict      *bool          `json:"strict,omitempty"`
}

type InputSchema struct {
	Type       string `json:"type"`
	Properties any    `json:"properties,omitempty"`
	Required   any    `json:"required,omitempty"`
}

type ClaudeWebSearchTool struct {
	Type              string                       `json:"type"`
	Name              string                       `json:"name"`
	MaxUses           int                          `json:"max_uses,omitempty"`
	AllowedDomains    []string                     `json:"allowed_domains,omitempty"`
	BlockedDomains    []string                     `json:"blocked_domains,omitempty"`
	AllowedCallers    []string                     `json:"allowed_callers,omitempty"`
	ResponseInclusion string                       `json:"response_inclusion,omitempty"`
	UserLocation      *ClaudeWebSearchUserLocation `json:"user_location,omitempty"`
}

type ClaudeWebSearchUserLocation struct {
	Type     string `json:"type"`
	Timezone string `json:"timezone,omitempty"`
	Country  string `json:"country,omitempty"`
	Region   string `json:"region,omitempty"`
	City     string `json:"city,omitempty"`
}

type ClaudeToolChoice struct {
	Type                   string `json:"type"`
	Name                   string `json:"name,omitempty"`
	DisableParallelToolUse bool   `json:"disable_parallel_tool_use,omitempty"`
}

type ClaudeRequest struct {
	Model        string          `json:"model"`
	Prompt       string          `json:"prompt,omitempty"`
	System       any             `json:"system,omitempty"`
	Messages     []ClaudeMessage `json:"messages,omitempty"`
	CacheControl json.RawMessage `json:"cache_control,omitempty"`
	// InferenceGeo controls Claude data residency region.
	// This field is filtered by default and can be enabled via channel setting allow_inference_geo.
	InferenceGeo      string          `json:"inference_geo,omitempty"`
	MaxTokens         *uint           `json:"max_tokens,omitempty"`
	MaxTokensToSample *uint           `json:"max_tokens_to_sample,omitempty"`
	StopSequences     []string        `json:"stop_sequences,omitempty"`
	Temperature       *float64        `json:"temperature,omitempty"`
	TopP              *float64        `json:"top_p,omitempty"`
	TopK              *int            `json:"top_k,omitempty"`
	Stream            *bool           `json:"stream,omitempty"`
	Tools             any             `json:"tools,omitempty"`
	Safeguards        json.RawMessage `json:"safeguards,omitempty"`
	ContextManagement json.RawMessage `json:"context_management,omitempty"`
	OutputConfig      json.RawMessage `json:"output_config,omitempty"`
	OutputFormat      json.RawMessage `json:"output_format,omitempty"`
	Container         json.RawMessage `json:"container,omitempty"`
	ToolChoice        any             `json:"tool_choice,omitempty"`
	Thinking          *Thinking       `json:"thinking,omitempty"`
	McpServers        json.RawMessage `json:"mcp_servers,omitempty"`
	Metadata          json.RawMessage `json:"metadata,omitempty"`
	// vLLM Messages extension; forwarded verbatim when present.
	ChatTemplateKwargs json.RawMessage `json:"chat_template_kwargs,omitempty"`
	// Speed specifies the Claude inference speed mode.
	// This field is filtered by default and can be enabled via channel setting allow_speed.
	Speed json.RawMessage `json:"speed,omitempty"`
	// ServiceTier specifies upstream service level and may affect billing.
	// This field is filtered by default and can be enabled via channel setting allow_service_tier.
	ServiceTier string `json:"service_tier,omitempty"`
}

// OutputConfigForEffort just for extract effort
type OutputConfigForEffort struct {
	Effort string `json:"effort,omitempty"`
}

func (c *ClaudeRequest) GetTokenCountMeta() *types.TokenCountMeta {
	maxTokens := 0
	if c.MaxTokens != nil {
		maxTokens = int(*c.MaxTokens)
	}
	var tokenCountMeta = types.TokenCountMeta{
		TokenType: types.TokenTypeTokenizer,
		MaxTokens: maxTokens,
	}

	var texts = make([]string, 0)
	var fileMeta = make([]*types.FileMeta, 0)

	// system
	if c.System != nil {
		if c.IsStringSystem() {
			sys := c.GetStringSystem()
			if sys != "" {
				texts = append(texts, sys)
			}
		} else {
			systemMedia := c.ParseSystem()
			for _, media := range systemMedia {
				switch media.Type {
				case "text":
					texts = append(texts, media.GetText())
				case "image":
					if source := media.ToFileSource(); source != nil {
						fileMeta = append(fileMeta, &types.FileMeta{
							FileType: types.FileTypeImage,
							Source:   source,
						})
					}
				}
			}
		}
	}

	// messages
	for _, message := range c.Messages {
		tokenCountMeta.MessagesCount++
		texts = append(texts, message.Role)
		if message.IsStringContent() {
			content := message.GetStringContent()
			if content != "" {
				texts = append(texts, content)
			}
			continue
		}

		content, _ := message.ParseContent()
		for _, media := range content {
			switch media.Type {
			case "text":
				texts = append(texts, media.GetText())
			case "image":
				if source := media.ToFileSource(); source != nil {
					fileMeta = append(fileMeta, &types.FileMeta{
						FileType: types.FileTypeImage,
						Source:   source,
					})
				}
			case "tool_use":
				if media.Name != "" {
					texts = append(texts, media.Name)
				}
				if media.Input != nil {
					b, _ := kitutil.Marshal(media.Input)
					texts = append(texts, string(b))
				}
			case "tool_result":
				if media.Content != nil {
					b, _ := kitutil.Marshal(media.Content)
					texts = append(texts, string(b))
				}
			}
		}
	}

	// tools
	if c.Tools != nil {
		tools := c.GetTools()
		normalTools, webSearchTools := ProcessTools(tools)
		if normalTools != nil {
			for _, t := range normalTools {
				tokenCountMeta.ToolsCount++
				if t.Name != "" {
					texts = append(texts, t.Name)
				}
				if t.Description != "" {
					texts = append(texts, t.Description)
				}
				if t.InputSchema != nil {
					b, _ := kitutil.Marshal(t.InputSchema)
					texts = append(texts, string(b))
				}
			}
		}
		if webSearchTools != nil {
			for _, t := range webSearchTools {
				tokenCountMeta.ToolsCount++
				if t.Name != "" {
					texts = append(texts, t.Name)
				}
				if t.UserLocation != nil {
					b, _ := kitutil.Marshal(t.UserLocation)
					texts = append(texts, string(b))
				}
			}
		}
	}

	tokenCountMeta.CombineText = strings.Join(texts, "\n")
	tokenCountMeta.Files = fileMeta
	return &tokenCountMeta
}

func (c *ClaudeRequest) IsStream(ctx *http.Request) bool {
	if c.Stream == nil {
		return false
	}
	return *c.Stream
}

func (c *ClaudeRequest) SetModelName(modelName string) {
	if modelName != "" {
		c.Model = modelName
	}
}

// AddTool 添加工具到请求中
func (c *ClaudeRequest) AddTool(tool any) {
	if c.Tools == nil {
		c.Tools = make([]any, 0)
	}

	switch tools := c.Tools.(type) {
	case []any:
		c.Tools = append(tools, tool)
	default:
		// 如果Tools不是[]any类型，重新初始化为[]any
		c.Tools = []any{tool}
	}
}

// GetTools 获取工具列表
func (c *ClaudeRequest) GetTools() []any {
	if c.Tools == nil {
		return nil
	}

	switch tools := c.Tools.(type) {
	case []any:
		return tools
	default:
		return nil
	}
}

func (c *ClaudeRequest) GetEfforts() string {
	var OutputConfig OutputConfigForEffort
	if err := kitutil.Unmarshal(c.OutputConfig, &OutputConfig); err == nil {
		effort := OutputConfig.Effort
		return effort
	}
	return ""
}

// ProcessTools 处理工具列表，支持类型断言
func ProcessTools(tools []any) ([]*Tool, []*ClaudeWebSearchTool) {
	var normalTools []*Tool
	var webSearchTools []*ClaudeWebSearchTool

	for _, tool := range tools {
		switch t := tool.(type) {
		case *Tool:
			normalTools = append(normalTools, t)
		case *ClaudeWebSearchTool:
			webSearchTools = append(webSearchTools, t)
		case Tool:
			normalTools = append(normalTools, &t)
		case ClaudeWebSearchTool:
			webSearchTools = append(webSearchTools, &t)
		default:
			// 未知类型，跳过
			continue
		}
	}

	return normalTools, webSearchTools
}

type Thinking struct {
	Type         string `json:"type,omitempty"`
	BudgetTokens *int   `json:"budget_tokens,omitempty"`
	// Display controls whether thinking content is returned in the response.
	// Used with adaptive thinking on Claude Opus 4.7+: "summarized" restores
	// the visible summary that was default on Opus 4.6; "omitted" (default on
	// 4.7) suppresses it. Pass-through field from upstream Anthropic API.
	Display string `json:"display,omitempty"`
}

func (c *Thinking) GetBudgetTokens() int {
	if c.BudgetTokens == nil {
		return 0
	}
	return *c.BudgetTokens
}

func (c *ClaudeRequest) IsStringSystem() bool {
	_, ok := c.System.(string)
	return ok
}

func (c *ClaudeRequest) GetStringSystem() string {
	if c.IsStringSystem() {
		return c.System.(string)
	}
	return ""
}

func (c *ClaudeRequest) SetStringSystem(system string) {
	c.System = system
}

func (c *ClaudeRequest) ParseSystem() []ClaudeMediaMessage {
	mediaContent, _ := kitutil.Any2Type[[]ClaudeMediaMessage](c.System)
	return mediaContent
}

type ClaudeErrorWithStatusCode struct {
	Error      types.ClaudeError `json:"error"`
	StatusCode int               `json:"status_code"`
	LocalError bool
}

type ClaudeResponse struct {
	Id           string               `json:"id,omitempty"`
	Type         string               `json:"type"`
	Role         string               `json:"role,omitempty"`
	Content      []ClaudeMediaMessage `json:"content,omitempty"`
	Completion   string               `json:"completion,omitempty"`
	StopReason   string               `json:"stop_reason,omitempty"`
	Model        string               `json:"model,omitempty"`
	Error        any                  `json:"error,omitempty"`
	Usage        *ClaudeUsage         `json:"usage,omitempty"`
	Index        *int                 `json:"index,omitempty"`
	ContentBlock *ClaudeMediaMessage  `json:"content_block,omitempty"`
	Delta        *ClaudeMediaMessage  `json:"delta,omitempty"`
	Message      *ClaudeMediaMessage  `json:"message,omitempty"`
}

// set index
func (c *ClaudeResponse) SetIndex(i int) {
	c.Index = &i
}

// get index
func (c *ClaudeResponse) GetIndex() int {
	if c.Index == nil {
		return 0
	}
	return *c.Index
}

// GetClaudeError 从动态错误类型中提取ClaudeError结构
func (c *ClaudeResponse) GetClaudeError() *types.ClaudeError {
	if c.Error == nil {
		return nil
	}

	switch err := c.Error.(type) {
	case types.ClaudeError:
		return &err
	case *types.ClaudeError:
		return err
	case map[string]any:
		// 处理从JSON解析来的map结构
		claudeErr := &types.ClaudeError{}
		if errType, ok := err["type"].(string); ok {
			claudeErr.Type = errType
		}
		if errMsg, ok := err["message"].(string); ok {
			claudeErr.Message = errMsg
		}
		return claudeErr
	case string:
		// 处理简单字符串错误
		return &types.ClaudeError{
			Type:    "upstream_error",
			Message: err,
		}
	default:
		// 未知类型，尝试转换为字符串
		return &types.ClaudeError{
			Type:    "unknown_upstream_error",
			Message: fmt.Sprintf("unknown_error: %v", err),
		}
	}
}

type ClaudeUsage struct {
	InputTokens              int                       `json:"input_tokens"`
	CacheCreationInputTokens int                       `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int                       `json:"cache_read_input_tokens"`
	OutputTokens             int                       `json:"output_tokens"`
	CacheCreation            *ClaudeCacheCreationUsage `json:"cache_creation,omitempty"`
	// claude cache 1h
	ClaudeCacheCreation5mTokens int                  `json:"claude_cache_creation_5_m_tokens"`
	ClaudeCacheCreation1hTokens int                  `json:"claude_cache_creation_1_h_tokens"`
	ServerToolUse               *ClaudeServerToolUse `json:"server_tool_use,omitempty"`
	BillingUsage                *BillingUsage        `json:"billing_usage,omitempty"`
}

type ClaudeCacheCreationUsage struct {
	Ephemeral5mInputTokens int `json:"ephemeral_5m_input_tokens,omitempty"`
	Ephemeral1hInputTokens int `json:"ephemeral_1h_input_tokens,omitempty"`
}

func (u *ClaudeUsage) GetCacheCreation5mTokens() int {
	if u == nil || u.CacheCreation == nil {
		return 0
	}
	return u.CacheCreation.Ephemeral5mInputTokens
}

func (u *ClaudeUsage) GetCacheCreation1hTokens() int {
	if u == nil || u.CacheCreation == nil {
		return 0
	}
	return u.CacheCreation.Ephemeral1hInputTokens
}

func (u *ClaudeUsage) GetCacheCreationTotalTokens() int {
	if u == nil {
		return 0
	}
	if u.CacheCreationInputTokens > 0 {
		return u.CacheCreationInputTokens
	}
	return u.GetCacheCreation5mTokens() + u.GetCacheCreation1hTokens()
}

type ClaudeServerToolUse struct {
	WebSearchRequests     int `json:"web_search_requests,omitempty"`
	WebFetchRequests      int `json:"web_fetch_requests,omitempty"`
	CodeExecutionRequests int `json:"code_execution_requests,omitempty"`
	ToolSearchRequests    int `json:"tool_search_requests,omitempty"`
}
