package gemini

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel/openai"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/relayconvert"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

func buildUsageFromGeminiMetadata(metadata *dto.GeminiUsageMetadata, fallbackPromptTokens int) dto.Usage {
	usage := relayconvert.UsageFromGeminiMetadata(metadata, fallbackPromptTokens)
	if usage == nil {
		return dto.Usage{}
	}
	return *usage
}

func attachEstimatedGeminiBillingUsage(usage *dto.Usage) *dto.Usage {
	if usage != nil && usage.BillingUsage == nil {
		usage.BillingUsage = dto.NewEstimatedGeminiChatBillingUsage(usage)
	}
	return usage
}

// patchGeminiZeroCompletionUsage estimates completion tokens locally when upstream
// usageMetadata was billable but reported zero completion tokens even though output
// content was actually received. Typical case: the client aborts a stream before the
// final chunk that carries candidatesTokenCount, leaving prompt-only metadata; without
// this patch the output side would settle at zero quota.
func patchGeminiZeroCompletionUsage(c *gin.Context, info *relaycommon.RelayInfo, usage *dto.Usage, responseText string, imageCount int) {
	if usage == nil || usage.CompletionTokens > 0 {
		return
	}
	if responseText == "" && imageCount == 0 {
		return
	}
	estimated := service.ResponseText2Usage(c, responseText, info.UpstreamModelName, usage.PromptTokens)
	usage.CompletionTokens = estimated.CompletionTokens
	if imageCount != 0 && usage.CompletionTokens == 0 {
		usage.CompletionTokens = imageCount * 1400
	}
	usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
	// Settlement prefers BillingUsage, so fill the missing completion in the
	// original upstream dialect without discarding cache or modality details.
	if usage.BillingUsage != nil {
		usage.BillingUsage = dto.CloneBillingUsageWithEstimatedCompletion(usage.BillingUsage, usage.CompletionTokens)
	} else {
		usage.BillingUsage = dto.NewEstimatedGeminiChatBillingUsage(usage)
	}
}

func geminiResponseUsageText(response *dto.GeminiChatResponse) string {
	if response == nil {
		return ""
	}
	var text strings.Builder
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.Text != "" {
				text.WriteString(part.Text)
			}
		}
	}
	return text.String()
}

func markGeminiGoogleSearchCall(c *gin.Context, response *dto.GeminiChatResponse) {
	if c == nil || response == nil {
		return
	}
	for _, candidate := range response.Candidates {
		if candidate.GroundingMetadata != nil && len(candidate.GroundingMetadata.WebSearchQueries) > 0 {
			c.Set("gemini_google_search_call", true)
			return
		}
	}
}

func countGeminiBillableFunctionCalls(info *relaycommon.RelayInfo, response *dto.GeminiChatResponse) {
	if info == nil || response == nil {
		return
	}
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.FunctionCall == nil {
				continue
			}
			if part.FunctionCall.WillContinue != nil && *part.FunctionCall.WillContinue {
				continue
			}
			info.CountBillableToolCall(dto.BuildInCallFunctionCall, part.FunctionCall.FunctionName)
		}
	}
}

func buildUsageFromGeminiResponse(c *gin.Context, info *relaycommon.RelayInfo, response *dto.GeminiChatResponse) dto.Usage {
	metadata := response.GetUsageMetadata()
	if dto.HasGeminiUsageMetadataTokens(metadata) {
		usage := buildUsageFromGeminiMetadata(metadata, info.GetEstimatePromptTokens())
		patchGeminiZeroCompletionUsage(c, info, &usage, geminiResponseUsageText(response), geminiResponseInlineImageCount(response))
		return usage
	}
	usage := service.ResponseText2Usage(c, geminiResponseUsageText(response), info.UpstreamModelName, info.GetEstimatePromptTokens())
	attachEstimatedGeminiBillingUsage(usage)
	return *usage
}

func geminiResponseInlineImageCount(response *dto.GeminiChatResponse) int {
	if response == nil {
		return 0
	}
	count := 0
	for _, candidate := range response.Candidates {
		for _, part := range candidate.Content.Parts {
			if part.InlineData != nil && part.InlineData.MimeType != "" {
				count++
			}
		}
	}
	return count
}

func responseGeminiChat2OpenAI(c *gin.Context, response *dto.GeminiChatResponse) *dto.OpenAITextResponse {
	return relayconvert.ResponseGeminiChat2OpenAI(helper.GetResponseID(c), common.GetTimestamp(), response)
}

func streamResponseGeminiChat2OpenAI(geminiResponse *dto.GeminiChatResponse) (*dto.ChatCompletionsStreamResponse, bool) {
	return relayconvert.StreamResponseGeminiChat2OpenAI(geminiResponse)
}

func handleStream(c *gin.Context, info *relaycommon.RelayInfo, resp *dto.ChatCompletionsStreamResponse) error {
	streamData, err := common.Marshal(resp)
	if err != nil {
		return fmt.Errorf("failed to marshal stream response: %w", err)
	}
	err = openai.HandleStreamFormat(c, info, string(streamData), info.ChannelSetting.ForceFormat, info.ChannelSetting.ThinkingToContent)
	if err != nil {
		return fmt.Errorf("failed to handle stream format: %w", err)
	}
	return nil
}

func handleFinalStream(c *gin.Context, info *relaycommon.RelayInfo, resp *dto.ChatCompletionsStreamResponse) error {
	streamData, err := common.Marshal(resp)
	if err != nil {
		return fmt.Errorf("failed to marshal stream response: %w", err)
	}
	openai.HandleFinalResponse(c, info, string(streamData), resp.Id, resp.Created, resp.Model, resp.GetSystemFingerprint(), resp.Usage, false)
	return nil
}

func geminiStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response, callback func(data string, geminiResponse *dto.GeminiChatResponse) bool) (*dto.Usage, *types.NewAPIError) {
	var usage = &dto.Usage{}
	var imageCount int
	var hasBillableUsageMetadata bool
	var streamErr error
	var accumulatedUsageMetadata *dto.GeminiUsageMetadata
	responseText := strings.Builder{}

	helper.StreamScannerHandler(c, resp, info, func(data string, sr *helper.StreamResult) {
		var geminiResponse dto.GeminiChatResponse
		if err := common.UnmarshalJsonStr(data, &geminiResponse); err != nil {
			streamErr = fmt.Errorf("unmarshal Gemini stream response: %w", err)
			sr.Stop(streamErr)
			return
		}

		if len(geminiResponse.Candidates) == 0 && geminiResponse.PromptFeedback != nil && geminiResponse.PromptFeedback.BlockReason != nil {
			info.PerformanceBusinessRejection = true
			common.SetContextKey(c, constant.ContextKeyAdminRejectReason, fmt.Sprintf("gemini_block_reason=%s", *geminiResponse.PromptFeedback.BlockReason))
		}
		info.ObserveResponseModel(gjson.Get(data, "modelVersion").Str)
		for _, candidate := range geminiResponse.Candidates {
			if candidate.FinishReason == nil || *candidate.FinishReason == "" || *candidate.FinishReason == "FINISH_REASON_UNSPECIFIED" {
				continue
			}
			switch *candidate.FinishReason {
			case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "SPII", "IMAGE_SAFETY", "IMAGE_PROHIBITED_CONTENT", "IMAGE_RECITATION":
				info.PerformanceBusinessRejection = true
			}
			info.StreamStatus.MarkCompleted()
		}

		markGeminiGoogleSearchCall(c, &geminiResponse)
		countGeminiBillableFunctionCalls(info, &geminiResponse)

		// 统计图片数量
		for _, candidate := range geminiResponse.Candidates {
			for _, part := range candidate.Content.Parts {
				if part.InlineData != nil && part.InlineData.MimeType != "" {
					imageCount++
				}
				if part.Text != "" {
					responseText.WriteString(part.Text)
				}
			}
		}

		// 更新使用量统计
		if metadata := geminiResponse.GetUsageMetadata(); dto.HasGeminiUsageMetadataTokens(metadata) {
			accumulatedUsageMetadata = dto.MergeGeminiUsageMetadataNonZero(accumulatedUsageMetadata, metadata)
			mappedUsage := buildUsageFromGeminiMetadata(accumulatedUsageMetadata, info.GetEstimatePromptTokens())
			*usage = mappedUsage
			hasBillableUsageMetadata = true
		}

		if !callback(data, &geminiResponse) {
			if isGeminiDownstreamStop(c, info) {
				sr.Stop(nil)
				return
			}
			streamErr = errors.New("Gemini stream callback stopped")
			sr.Stop(streamErr)
		}
	})
	info.StreamStatus.RequireTerminal()

	if !hasBillableUsageMetadata {
		if info.ReceivedResponseCount > 0 {
			usage = service.ResponseText2Usage(c, responseText.String(), info.UpstreamModelName, info.GetEstimatePromptTokens())
		} else {
			usage = &dto.Usage{}
		}
		if imageCount != 0 && usage.CompletionTokens == 0 {
			usage.CompletionTokens = imageCount * 1400
			usage.TotalTokens = usage.PromptTokens + usage.CompletionTokens
			common.SetContextKey(c, constant.ContextKeyLocalCountTokens, true)
		}
		attachEstimatedGeminiBillingUsage(usage)
	} else {
		patchGeminiZeroCompletionUsage(c, info, usage, responseText.String(), imageCount)
	}

	if streamErr != nil {
		return usage, types.NewOpenAIError(streamErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	if info.StreamStatus != nil && !info.StreamStatus.IsNormalEnd() {
		logger.LogWarn(c, fmt.Sprintf("Gemini stream ended unexpectedly: %s", info.StreamStatus.Summary()))
	}

	return usage, nil
}

func isGeminiDownstreamStop(c *gin.Context, info *relaycommon.RelayInfo) bool {
	if c != nil && c.Request != nil && c.Request.Context().Err() != nil {
		return true
	}
	return info != nil && info.StreamStatus != nil &&
		info.StreamStatus.EndReason == relaycommon.StreamEndReasonClientGone
}

func GeminiChatStreamHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	id := helper.GetResponseID(c)
	createAt := common.GetTimestamp()
	finishReason := constant.FinishReasonStop
	toolCallIndexByChoice := make(map[int]map[string]int)
	nextToolCallIndexByChoice := make(map[int]int)

	usage, err := geminiStreamHandler(c, info, resp, func(data string, geminiResponse *dto.GeminiChatResponse) bool {
		response, isStop := streamResponseGeminiChat2OpenAI(geminiResponse)

		response.Id = id
		response.Created = createAt
		response.Model = info.UpstreamModelName
		if response.IsToolCall() {
			finishReason = constant.FinishReasonToolCalls
			if info.RelayFormat == types.RelayFormatClaude {
				for choiceIdx := range response.Choices {
					response.Choices[choiceIdx].FinishReason = nil
				}
			}
		}
		for choiceIdx := range response.Choices {
			choiceKey := response.Choices[choiceIdx].Index
			for toolIdx := range response.Choices[choiceIdx].Delta.ToolCalls {
				tool := &response.Choices[choiceIdx].Delta.ToolCalls[toolIdx]
				if tool.ID == "" {
					continue
				}
				m := toolCallIndexByChoice[choiceKey]
				if m == nil {
					m = make(map[string]int)
					toolCallIndexByChoice[choiceKey] = m
				}
				if idx, ok := m[tool.ID]; ok {
					tool.SetIndex(idx)
					continue
				}
				idx := nextToolCallIndexByChoice[choiceKey]
				nextToolCallIndexByChoice[choiceKey] = idx + 1
				m[tool.ID] = idx
				tool.SetIndex(idx)
			}
		}

		logger.LogDebug(c, "info.SendResponseCount = %d", info.SendResponseCount)
		if info.SendResponseCount == 0 {
			// send first response
			emptyResponse := helper.GenerateStartEmptyResponse(id, createAt, info.UpstreamModelName, nil)
			// Claude message_start is emitted from this first OpenAI chunk.
			// Carry upstream usage when the current Gemini frame provided it.
			emptyResponse.Usage = response.Usage
			if response.IsToolCall() {
				if len(emptyResponse.Choices) > 0 && len(response.Choices) > 0 {
					toolCalls := response.Choices[0].Delta.ToolCalls
					copiedToolCalls := make([]dto.ToolCallResponse, len(toolCalls))
					for idx := range toolCalls {
						copiedToolCalls[idx] = toolCalls[idx]
						copiedToolCalls[idx].Function.Arguments = ""
					}
					emptyResponse.Choices[0].Delta.ToolCalls = copiedToolCalls
				}
				finishReason = constant.FinishReasonToolCalls
				err := handleStream(c, info, emptyResponse)
				if err != nil {
					logger.LogError(c, err.Error())
				}

				response.ClearToolCalls()
				if response.IsFinished() {
					response.Choices[0].FinishReason = nil
				}
			} else {
				err := handleStream(c, info, emptyResponse)
				if err != nil {
					logger.LogError(c, err.Error())
				}
			}
		}

		err := handleStream(c, info, response)
		if err != nil {
			logger.LogError(c, err.Error())
		}
		if isStop {
			if info.RelayFormat != types.RelayFormatClaude {
				_ = handleStream(c, info, helper.GenerateStopResponse(id, createAt, info.UpstreamModelName, finishReason))
			}
		}
		return true
	})

	if err != nil {
		return usage, err
	}

	response := helper.GenerateFinalUsageResponse(id, createAt, info.UpstreamModelName, *usage)
	if info.RelayFormat == types.RelayFormatClaude && info.ClaudeConvertInfo != nil && !info.ClaudeConvertInfo.Done {
		response = helper.GenerateStopResponse(id, createAt, info.UpstreamModelName, finishReason)
		response.Usage = usage
	}
	handleErr := handleFinalStream(c, info, response)
	if handleErr != nil {
		common.SysLog("send final response failed: " + handleErr.Error())
	}
	return usage, nil
}

func GeminiChatHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	service.CloseResponseBodyGracefully(resp)
	logger.LogDebug(c, "Gemini response body: %s", responseBody)
	var geminiResponse dto.GeminiChatResponse
	err = common.Unmarshal(responseBody, &geminiResponse)
	if err != nil {
		return nil, types.NewOpenAIError(err, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	info.ObserveResponseModel(gjson.GetBytes(responseBody, "modelVersion").Str)
	markGeminiGoogleSearchCall(c, &geminiResponse)
	countGeminiBillableFunctionCalls(info, &geminiResponse)
	if len(geminiResponse.Candidates) == 0 {
		usage := buildUsageFromGeminiResponse(c, info, &geminiResponse)

		var newAPIError *types.NewAPIError
		if geminiResponse.PromptFeedback != nil && geminiResponse.PromptFeedback.BlockReason != nil {
			info.PerformanceBusinessRejection = true
			common.SetContextKey(c, constant.ContextKeyAdminRejectReason, fmt.Sprintf("gemini_block_reason=%s", *geminiResponse.PromptFeedback.BlockReason))
			newAPIError = types.NewOpenAIError(
				errors.New("request blocked by Gemini API: "+*geminiResponse.PromptFeedback.BlockReason),
				types.ErrorCodePromptBlocked,
				http.StatusBadRequest,
			)
		} else {
			common.SetContextKey(c, constant.ContextKeyAdminRejectReason, "gemini_empty_candidates")
			newAPIError = types.NewOpenAIError(
				errors.New("empty response from Gemini API"),
				types.ErrorCodeEmptyResponse,
				http.StatusInternalServerError,
			)
		}

		service.ResetStatusCode(newAPIError, c.GetString("status_code_mapping"))

		switch info.RelayFormat {
		case types.RelayFormatClaude:
			c.JSON(newAPIError.StatusCode, gin.H{
				"type":  "error",
				"error": newAPIError.ToClaudeError(),
			})
		default:
			c.JSON(newAPIError.StatusCode, gin.H{
				"error": newAPIError.ToOpenAIError(),
			})
		}
		return &usage, nil
	}
	fullTextResponse := responseGeminiChat2OpenAI(c, &geminiResponse)
	fullTextResponse.Model = info.UpstreamModelName
	usage := buildUsageFromGeminiResponse(c, info, &geminiResponse)

	fullTextResponse.Usage = usage

	switch info.RelayFormat {
	case types.RelayFormatOpenAI:
		responseBody, err = common.Marshal(fullTextResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
	case types.RelayFormatClaude:
		convertResult, err := service.ConvertResponse(c, info, types.RelayFormatClaude, fullTextResponse)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		claudeRespStr, err := common.Marshal(convertResult.Value)
		if err != nil {
			return nil, types.NewError(err, types.ErrorCodeBadResponseBody)
		}
		responseBody = claudeRespStr
	case types.RelayFormatGemini:
		break
	}

	service.IOCopyBytesGracefully(c, resp, responseBody)

	return &usage, nil
}

func GeminiEmbeddingHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)

	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, types.NewOpenAIError(readErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	var geminiResponse dto.GeminiBatchEmbeddingResponse
	if jsonErr := common.Unmarshal(responseBody, &geminiResponse); jsonErr != nil {
		return nil, types.NewOpenAIError(jsonErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	// convert to openai format response
	openAIResponse := dto.OpenAIEmbeddingResponse{
		Object: "list",
		Data:   make([]dto.OpenAIEmbeddingResponseItem, 0, len(geminiResponse.Embeddings)),
		Model:  info.UpstreamModelName,
	}

	for i, embedding := range geminiResponse.Embeddings {
		openAIResponse.Data = append(openAIResponse.Data, dto.OpenAIEmbeddingResponseItem{
			Object:    "embedding",
			Embedding: embedding.Values,
			Index:     i,
		})
	}

	// calculate usage
	// https://ai.google.dev/gemini-api/docs/pricing?hl=zh-cn#text-embedding-004
	// Google has not yet clarified how embedding models will be billed
	// refer to openai billing method to use input tokens billing
	// https://platform.openai.com/docs/guides/embeddings#what-are-embeddings
	usage := service.ResponseText2Usage(c, "", info.UpstreamModelName, info.GetEstimatePromptTokens())
	openAIResponse.Usage = *usage

	jsonResponse, jsonErr := common.Marshal(openAIResponse)
	if jsonErr != nil {
		return nil, types.NewOpenAIError(jsonErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	service.IOCopyBytesGracefully(c, resp, jsonResponse)
	return usage, nil
}

func GeminiImageHandler(c *gin.Context, info *relaycommon.RelayInfo, resp *http.Response) (*dto.Usage, *types.NewAPIError) {
	responseBody, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return nil, types.NewOpenAIError(readErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}
	_ = resp.Body.Close()

	var geminiResponse dto.GeminiImageResponse
	if jsonErr := common.Unmarshal(responseBody, &geminiResponse); jsonErr != nil {
		return nil, types.NewOpenAIError(jsonErr, types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	if len(geminiResponse.Predictions) == 0 {
		return nil, types.NewOpenAIError(errors.New("no images generated"), types.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	}

	// convert to openai format response
	openAIResponse := dto.ImageResponse{
		Created: common.GetTimestamp(),
		Data:    make([]dto.ImageData, 0, len(geminiResponse.Predictions)),
	}

	for _, prediction := range geminiResponse.Predictions {
		if prediction.RaiFilteredReason != "" {
			continue // skip filtered image
		}
		openAIResponse.Data = append(openAIResponse.Data, dto.ImageData{
			B64Json: prediction.BytesBase64Encoded,
		})
	}

	jsonResponse, jsonErr := common.Marshal(openAIResponse)
	if jsonErr != nil {
		return nil, types.NewError(jsonErr, types.ErrorCodeBadResponseBody)
	}

	c.Writer.Header().Set("Content-Type", "application/json")
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = c.Writer.Write(jsonResponse)

	// https://github.com/google-gemini/cookbook/blob/719a27d752aac33f39de18a8d3cb42a70874917e/quickstarts/Counting_Tokens.ipynb
	// each image has fixed 258 tokens
	const imageTokens = 258
	generatedImages := len(openAIResponse.Data)

	usage := &dto.Usage{
		PromptTokens:     imageTokens * generatedImages, // each generated image has fixed 258 tokens
		CompletionTokens: 0,                             // image generation does not calculate completion tokens
		TotalTokens:      imageTokens * generatedImages,
	}

	return usage, nil
}

type GeminiModelsResponse struct {
	Models        []dto.GeminiModel `json:"models"`
	NextPageToken string            `json:"nextPageToken"`
}

func FetchGeminiModels(baseURL, apiKey, proxyURL string) ([]string, error) {
	client, err := service.GetHttpClientWithProxy(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("创建HTTP客户端失败: %v", err)
	}

	allModels := make([]string, 0)
	nextPageToken := ""
	maxPages := 100 // Safety limit to prevent infinite loops

	for range maxPages {
		url := fmt.Sprintf("%s/v1beta/models", baseURL)
		if nextPageToken != "" {
			url = fmt.Sprintf("%s?pageToken=%s", url, nextPageToken)
		}

		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		request, err := http.NewRequestWithContext(ctx, "GET", url, nil)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("创建请求失败: %v", err)
		}

		request.Header.Set("x-goog-api-key", apiKey)

		response, err := client.Do(request)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("请求失败: %v", err)
		}

		if response.StatusCode != http.StatusOK {
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			cancel()
			return nil, fmt.Errorf("服务器返回错误 %d: %s", response.StatusCode, string(body))
		}

		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		cancel()
		if err != nil {
			return nil, fmt.Errorf("读取响应失败: %v", err)
		}

		var modelsResponse GeminiModelsResponse
		if err = common.Unmarshal(body, &modelsResponse); err != nil {
			return nil, fmt.Errorf("解析响应失败: %v", err)
		}

		for _, model := range modelsResponse.Models {
			modelNameValue, ok := model.Name.(string)
			if !ok {
				continue
			}
			modelName := strings.TrimPrefix(modelNameValue, "models/")
			allModels = append(allModels, modelName)
		}

		nextPageToken = modelsResponse.NextPageToken
		if nextPageToken == "" {
			break
		}
	}

	return allModels, nil
}
