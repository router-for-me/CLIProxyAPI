package ollama

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"

	"github.com/gin-gonic/gin"
	"github.com/samber/lo"
)

func toOllamaResponseFormat(responseFormat *dto.ResponseFormat) (any, error) {
	if responseFormat == nil {
		return nil, nil
	}
	switch responseFormat.Type {
	case "json", "json_object":
		return "json", nil
	case "json_schema":
		if len(responseFormat.JsonSchema) == 0 {
			return nil, nil
		}
		var jsonSchema dto.FormatJsonSchema
		if err := common.Unmarshal(responseFormat.JsonSchema, &jsonSchema); err != nil {
			return nil, fmt.Errorf("invalid ollama response format: %w", err)
		}
		return jsonSchema.Schema, nil
	default:
		return nil, nil
	}
}

func openAIChatToOllamaChat(c *gin.Context, r *dto.GeneralOpenAIRequest) (*OllamaChatRequest, error) {
	think := r.Think
	if len(think) == 0 {
		effort := r.ReasoningEffort
		if len(r.Reasoning) > 0 {
			var reasoning dto.Reasoning
			if err := common.Unmarshal(r.Reasoning, &reasoning); err != nil {
				return nil, fmt.Errorf("invalid ollama reasoning: %w", err)
			}
			effort = lo.CoalesceOrEmpty(reasoning.Effort, effort)
		}
		if effort != "" {
			var thinkValue any
			switch effort {
			case "none":
				thinkValue = false
			case "low", "medium", "high", "max":
				thinkValue = effort
			default:
				return nil, fmt.Errorf("unsupported ollama reasoning effort %q", effort)
			}
			var err error
			think, err = common.Marshal(thinkValue)
			if err != nil {
				return nil, fmt.Errorf("marshal ollama think: %w", err)
			}
		}
	}

	chatReq := &OllamaChatRequest{
		Model:   r.Model,
		Stream:  lo.FromPtrOr(r.Stream, false),
		Options: map[string]any{},
		Think:   think,
	}
	format, err := toOllamaResponseFormat(r.ResponseFormat)
	if err != nil {
		return nil, err
	}
	chatReq.Format = format

	// options mapping
	if r.Temperature != nil {
		chatReq.Options["temperature"] = r.Temperature
	}
	if r.TopP != nil {
		chatReq.Options["top_p"] = lo.FromPtr(r.TopP)
	}
	if r.TopK != nil {
		chatReq.Options["top_k"] = lo.FromPtr(r.TopK)
	}
	if r.FrequencyPenalty != nil {
		chatReq.Options["frequency_penalty"] = lo.FromPtr(r.FrequencyPenalty)
	}
	if r.PresencePenalty != nil {
		chatReq.Options["presence_penalty"] = lo.FromPtr(r.PresencePenalty)
	}
	if r.Seed != nil {
		chatReq.Options["seed"] = int(lo.FromPtr(r.Seed))
	}
	if mt := r.GetMaxTokens(); mt != 0 {
		chatReq.Options["num_predict"] = int(mt)
	}

	if r.Stop != nil {
		switch v := r.Stop.(type) {
		case string:
			chatReq.Options["stop"] = []string{v}
		case []string:
			chatReq.Options["stop"] = v
		case []any:
			arr := lo.FilterMap(v, func(item any, _ int) (string, bool) {
				value, ok := item.(string)
				return value, ok
			})
			if len(arr) > 0 {
				chatReq.Options["stop"] = arr
			}
		}
	}

	if len(r.Tools) > 0 {
		chatReq.Tools = lo.Map(r.Tools, func(tool dto.ToolCallRequest, _ int) OllamaTool {
			return OllamaTool{
				Type: "function",
				Function: OllamaToolFunction{
					Name:        tool.Function.Name,
					Description: tool.Function.Description,
					Parameters:  tool.Function.Parameters,
				},
			}
		})
	}

	chatReq.Messages = make([]OllamaChatMessage, 0, len(r.Messages))
	toolNamesByCallID := make(map[string]string)
	for _, m := range r.Messages {
		var textBuilder strings.Builder
		var images []string
		if m.IsStringContent() {
			textBuilder.WriteString(m.StringContent())
		} else {
			parts := m.ParseContent()
			for _, part := range parts {
				if part.Type == dto.ContentTypeImageURL {
					source := part.ToFileSource()
					if source != nil {
						base64Data, _, err := service.GetBase64Data(c, source, "fetch image for ollama chat")
						if err != nil {
							return nil, err
						}
						if base64Data != "" {
							images = append(images, base64Data)
						}
					}
				} else if part.Type == dto.ContentTypeText {
					textBuilder.WriteString(part.Text)
				}
			}
		}
		cm := OllamaChatMessage{Role: m.Role, Content: textBuilder.String()}
		if len(images) > 0 {
			cm.Images = images
		}
		if m.Role == "assistant" {
			if reasoning, ok := lo.Coalesce(m.ReasoningContent, m.Reasoning); ok {
				thinking, err := common.Marshal(*reasoning)
				if err != nil {
					return nil, fmt.Errorf("marshal ollama thinking: %w", err)
				}
				cm.Thinking = thinking
			}
		}
		if m.Role == "tool" {
			cm.ToolCallID = m.ToolCallId
			cm.ToolName = lo.CoalesceOrEmpty(lo.FromPtr(m.Name), toolNamesByCallID[m.ToolCallId])
		}
		if m.ToolCalls != nil && len(m.ToolCalls) > 0 {
			parsed := m.ParseToolCalls()
			if len(parsed) > 0 {
				calls := make([]OllamaToolCall, 0, len(parsed))
				for _, tc := range parsed {
					var args any
					if tc.Function.Arguments != "" {
						_ = common.Unmarshal([]byte(tc.Function.Arguments), &args)
					}
					if args == nil {
						args = map[string]any{}
					}
					oc := OllamaToolCall{ID: tc.ID}
					oc.Function.Name = tc.Function.Name
					oc.Function.Arguments = args
					calls = append(calls, oc)
					if tc.ID != "" {
						toolNamesByCallID[tc.ID] = tc.Function.Name
					}
				}
				cm.ToolCalls = calls
			}
		}
		chatReq.Messages = append(chatReq.Messages, cm)
	}
	return chatReq, nil
}

// openAIToGenerate converts OpenAI completions request to Ollama generate
func openAIToGenerate(c *gin.Context, r *dto.GeneralOpenAIRequest) (*OllamaGenerateRequest, error) {
	gen := &OllamaGenerateRequest{
		Model:   r.Model,
		Stream:  lo.FromPtrOr(r.Stream, false),
		Options: map[string]any{},
		Think:   r.Think,
	}
	// Prompt may be in r.Prompt (string or []any)
	if r.Prompt != nil {
		switch v := r.Prompt.(type) {
		case string:
			gen.Prompt = v
		case []any:
			var sb strings.Builder
			for _, it := range v {
				if s, ok := it.(string); ok {
					sb.WriteString(s)
				}
			}
			gen.Prompt = sb.String()
		default:
			gen.Prompt = fmt.Sprintf("%v", r.Prompt)
		}
	}
	if r.Suffix != nil {
		if s, ok := r.Suffix.(string); ok {
			gen.Suffix = s
		}
	}
	format, err := toOllamaResponseFormat(r.ResponseFormat)
	if err != nil {
		return nil, err
	}
	gen.Format = format
	if r.Temperature != nil {
		gen.Options["temperature"] = r.Temperature
	}
	if r.TopP != nil {
		gen.Options["top_p"] = lo.FromPtr(r.TopP)
	}
	if r.TopK != nil {
		gen.Options["top_k"] = lo.FromPtr(r.TopK)
	}
	if r.FrequencyPenalty != nil {
		gen.Options["frequency_penalty"] = lo.FromPtr(r.FrequencyPenalty)
	}
	if r.PresencePenalty != nil {
		gen.Options["presence_penalty"] = lo.FromPtr(r.PresencePenalty)
	}
	if r.Seed != nil {
		gen.Options["seed"] = int(lo.FromPtr(r.Seed))
	}
	if mt := r.GetMaxTokens(); mt != 0 {
		gen.Options["num_predict"] = int(mt)
	}
	if r.Stop != nil {
		switch v := r.Stop.(type) {
		case string:
			gen.Options["stop"] = []string{v}
		case []string:
			gen.Options["stop"] = v
		case []any:
			arr := lo.FilterMap(v, func(item any, _ int) (string, bool) {
				value, ok := item.(string)
				return value, ok
			})
			if len(arr) > 0 {
				gen.Options["stop"] = arr
			}
		}
	}
	return gen, nil
}

func requestOpenAI2Embeddings(r dto.EmbeddingRequest) *OllamaEmbeddingRequest {
	opts := map[string]any{}
	if r.Temperature != nil {
		opts["temperature"] = r.Temperature
	}
	if r.TopP != nil {
		opts["top_p"] = lo.FromPtr(r.TopP)
	}
	if r.FrequencyPenalty != nil {
		opts["frequency_penalty"] = lo.FromPtr(r.FrequencyPenalty)
	}
	if r.PresencePenalty != nil {
		opts["presence_penalty"] = lo.FromPtr(r.PresencePenalty)
	}
	if r.Seed != nil {
		opts["seed"] = int(lo.FromPtr(r.Seed))
	}
	dimensions := lo.FromPtrOr(r.Dimensions, 0)
	if r.Dimensions != nil {
		opts["dimensions"] = dimensions
	}
	input := r.ParseInput()
	if len(input) == 1 {
		return &OllamaEmbeddingRequest{Model: r.Model, Input: input[0], Options: opts, Dimensions: dimensions}
	}
	return &OllamaEmbeddingRequest{Model: r.Model, Input: input, Options: opts, Dimensions: dimensions}
}

func ollamaEmbeddingHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	var oResp OllamaEmbeddingResponse
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)
	if err = common.Unmarshal(body, &oResp); err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if oResp.Error != "" {
		return nil, types.NewOpenAIError(fmt.Errorf("ollama error: %s", oResp.Error), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	data := make([]dto.OpenAIEmbeddingResponseItem, 0, len(oResp.Embeddings))
	for i, emb := range oResp.Embeddings {
		data = append(data, dto.OpenAIEmbeddingResponseItem{Index: i, Object: "embedding", Embedding: emb})
	}
	usage := &dto.Usage{PromptTokens: oResp.PromptEvalCount, CompletionTokens: 0, TotalTokens: oResp.PromptEvalCount}
	embResp := &dto.OpenAIEmbeddingResponse{Object: "list", Data: data, Model: info.UpstreamModelName, Usage: *usage}
	out, _ := common.Marshal(embResp)
	service.IOCopyBytesGracefully(c, resp, out)
	return usage, nil
}

func FetchOllamaModels(baseURL, apiKey string) ([]OllamaModel, error) {
	url := fmt.Sprintf("%s/api/tags", baseURL)

	client := &http.Client{}
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("创建请求失败: %v", err)
	}

	// Ollama 通常不需要 Bearer token，但为了兼容性保留
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return nil, fmt.Errorf("服务器返回错误 %d: %s", response.StatusCode, string(body))
	}

	var tagsResponse OllamaTagsResponse
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, fmt.Errorf("读取响应失败: %v", err)
	}

	err = common.Unmarshal(body, &tagsResponse)
	if err != nil {
		return nil, fmt.Errorf("解析响应失败: %v", err)
	}

	return tagsResponse.Models, nil
}

// 拉取 Ollama 模型 (非流式)
func PullOllamaModel(baseURL, apiKey, modelName string) error {
	url := fmt.Sprintf("%s/api/pull", baseURL)

	pullRequest := OllamaPullRequest{
		Name:   modelName,
		Stream: false, // 非流式，简化处理
	}

	requestBody, err := common.Marshal(pullRequest)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %v", err)
	}

	client := &http.Client{
		Timeout: 30 * 60 * 1000 * time.Millisecond, // 30分钟超时，支持大模型
	}
	request, err := http.NewRequest("POST", url, strings.NewReader(string(requestBody)))
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("请求失败: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("拉取模型失败 %d: %s", response.StatusCode, string(body))
	}

	return nil
}

// 流式拉取 Ollama 模型 (支持进度回调)
func PullOllamaModelStream(baseURL, apiKey, modelName string, progressCallback func(OllamaPullResponse)) error {
	url := fmt.Sprintf("%s/api/pull", baseURL)

	pullRequest := OllamaPullRequest{
		Name:   modelName,
		Stream: true, // 启用流式
	}

	requestBody, err := common.Marshal(pullRequest)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %v", err)
	}

	client := &http.Client{
		Timeout: 60 * 60 * 1000 * time.Millisecond, // 1小时超时，支持超大模型
	}
	request, err := http.NewRequest("POST", url, strings.NewReader(string(requestBody)))
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("请求失败: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("拉取模型失败 %d: %s", response.StatusCode, string(body))
	}

	// 读取流式响应
	scanner := helper.NewStreamScanner(response.Body)
	successful := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}

		var pullResponse OllamaPullResponse
		if err := common.Unmarshal([]byte(line), &pullResponse); err != nil {
			continue // 忽略解析失败的行
		}

		if progressCallback != nil {
			progressCallback(pullResponse)
		}

		// 检查是否出现错误或完成
		if strings.EqualFold(pullResponse.Status, "error") {
			return fmt.Errorf("拉取模型失败: %s", strings.TrimSpace(line))
		}
		if strings.EqualFold(pullResponse.Status, "success") {
			successful = true
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("读取流式响应失败: %v", err)
	}

	if !successful {
		return fmt.Errorf("拉取模型未完成: 未收到成功状态")
	}

	return nil
}

// 删除 Ollama 模型
func DeleteOllamaModel(baseURL, apiKey, modelName string) error {
	url := fmt.Sprintf("%s/api/delete", baseURL)

	deleteRequest := OllamaDeleteRequest{
		Name: modelName,
	}

	requestBody, err := common.Marshal(deleteRequest)
	if err != nil {
		return fmt.Errorf("序列化请求失败: %v", err)
	}

	client := &http.Client{}
	request, err := http.NewRequest("DELETE", url, strings.NewReader(string(requestBody)))
	if err != nil {
		return fmt.Errorf("创建请求失败: %v", err)
	}

	request.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("请求失败: %v", err)
	}
	defer response.Body.Close()

	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		return fmt.Errorf("删除模型失败 %d: %s", response.StatusCode, string(body))
	}

	return nil
}

func FetchOllamaVersion(baseURL, apiKey string) (string, error) {
	trimmedBase := strings.TrimRight(baseURL, "/")
	if trimmedBase == "" {
		return "", fmt.Errorf("baseURL 为空")
	}

	url := fmt.Sprintf("%s/api/version", trimmedBase)

	client := &http.Client{Timeout: 10 * time.Second}
	request, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return "", fmt.Errorf("创建请求失败: %v", err)
	}

	if apiKey != "" {
		request.Header.Set("Authorization", "Bearer "+apiKey)
	}

	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("请求失败: %v", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(response.Body)
	if err != nil {
		return "", fmt.Errorf("读取响应失败: %v", err)
	}

	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("查询版本失败 %d: %s", response.StatusCode, string(body))
	}

	var versionResp struct {
		Version string `json:"version"`
	}

	if err := common.Unmarshal(body, &versionResp); err != nil {
		return "", fmt.Errorf("解析响应失败: %v", err)
	}

	if versionResp.Version == "" {
		return "", fmt.Errorf("未返回版本信息")
	}

	return versionResp.Version, nil
}
