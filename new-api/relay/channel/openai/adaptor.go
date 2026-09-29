package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/relay/channel"
	"github.com/QuantumNous/new-api/relay/channel/ai360"
	"github.com/QuantumNous/new-api/relay/channel/lingyiwanwu"
	"github.com/QuantumNous/new-api/relay/channel/openrouter"
	"github.com/QuantumNous/new-api/relaykit/dto"

	//"github.com/QuantumNous/new-api/relay/channel/minimax"
	"github.com/QuantumNous/new-api/relay/channel/xinference"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/common_handler"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	kitreasoning "github.com/QuantumNous/new-api/relaykit/relayconvert/reasoning"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/QuantumNous/new-api/setting/reasoning"
	"github.com/samber/lo"

	"github.com/gin-gonic/gin"
)

type Adaptor struct {
	ChannelType    int
	ResponseFormat string
}

func (a *Adaptor) ConvertGeminiRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeminiChatRequest) (any, error) {
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	openaiRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	if info.SupportStreamOptions && info.IsStream {
		openaiRequest.StreamOptions = &dto.StreamOptions{
			IncludeUsage: true,
		}
	}
	return a.ConvertOpenAIRequest(c, info, openaiRequest)
}

func (a *Adaptor) ConvertClaudeRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.ClaudeRequest) (any, error) {
	//if !strings.Contains(request.Model, "claude") {
	//	return nil, fmt.Errorf("you are using openai channel type with path /v1/messages, only claude model supported convert, but got %s", request.Model)
	//}
	//if common.DebugEnabled {
	//	bodyBytes := []byte(common.GetJsonString(request))
	//	err := os.WriteFile(fmt.Sprintf("claude_request_%s.txt", c.GetString(common.RequestIdKey)), bodyBytes, 0644)
	//	if err != nil {
	//		println(fmt.Sprintf("failed to save request body to file: %v", err))
	//	}
	//}
	result, err := service.ConvertRequest(c, info, types.RelayFormatOpenAI, request)
	if err != nil {
		return nil, err
	}
	aiRequest, ok := result.Value.(*dto.GeneralOpenAIRequest)
	if !ok {
		return nil, fmt.Errorf("expected OpenAI chat completions request, got %T", result.Value)
	}
	//if common.DebugEnabled {
	//	println(fmt.Sprintf("convert claude to openai request result: %s", common.GetJsonString(aiRequest)))
	//	// Save request body to file for debugging
	//	bodyBytes := []byte(common.GetJsonString(aiRequest))
	//	err = os.WriteFile(fmt.Sprintf("claude_to_openai_request_%s.txt", c.GetString(common.RequestIdKey)), bodyBytes, 0644)
	//	if err != nil {
	//		println(fmt.Sprintf("failed to save request body to file: %v", err))
	//	}
	//}
	if info.SupportStreamOptions && info.IsStream {
		aiRequest.StreamOptions = &dto.StreamOptions{
			IncludeUsage: true,
		}
	}
	return a.ConvertOpenAIRequest(c, info, aiRequest)
}

func (a *Adaptor) Init(info *relaycommon.RelayInfo) {
	a.ChannelType = info.ChannelType

	// initialize ThinkingContentInfo when thinking_to_content is enabled
	if info.ChannelSetting.ThinkingToContent {
		info.ThinkingContentInfo = relaycommon.ThinkingContentInfo{
			IsFirstThinkingContent:  true,
			SendLastThinkingContent: false,
			HasSentThinkingContent:  false,
		}
	}
}

func (a *Adaptor) GetRequestURL(info *relaycommon.RelayInfo) (string, error) {
	if info.RelayMode == relayconstant.RelayModeRealtime {
		if after, ok := strings.CutPrefix(info.ChannelBaseUrl, "https://"); ok {
			baseUrl := after
			baseUrl = "wss://" + baseUrl
			info.ChannelBaseUrl = baseUrl
		} else if after, ok := strings.CutPrefix(info.ChannelBaseUrl, "http://"); ok {
			baseUrl := after
			baseUrl = "ws://" + baseUrl
			info.ChannelBaseUrl = baseUrl
		}
	}
	switch info.ChannelType {
	case constant.ChannelTypeAzure:
		apiVersion := info.ApiVersion
		if apiVersion == "" {
			apiVersion = constant.AzureDefaultAPIVersion
		}
		// https://learn.microsoft.com/en-us/azure/cognitive-services/openai/chatgpt-quickstart?pivots=rest-api&tabs=command-line#rest-api
		requestURL := strings.Split(info.RequestURLPath, "?")[0]
		requestURL = fmt.Sprintf("%s?api-version=%s", requestURL, apiVersion)
		task := strings.TrimPrefix(requestURL, "/v1/")

		if info.RelayFormat == types.RelayFormatClaude {
			task = strings.TrimPrefix(task, "messages")
			task = "chat/completions" + task
		}

		// 特殊处理 responses API（包含 compact）
		if info.RelayMode == relayconstant.RelayModeResponses || info.RelayMode == relayconstant.RelayModeResponsesCompact {
			responsesApiVersion := "preview"

			subUrl := "/openai/v1/responses"
			if strings.Contains(info.ChannelBaseUrl, "cognitiveservices.azure.com") {
				subUrl = "/openai/responses"
				responsesApiVersion = apiVersion
			}

			if info.ChannelOtherSettings.AzureResponsesVersion != "" {
				responsesApiVersion = info.ChannelOtherSettings.AzureResponsesVersion
			}

			// compact 模式追加 /compact
			if info.RelayMode == relayconstant.RelayModeResponsesCompact {
				subUrl = subUrl + "/compact"
			}

			requestURL = fmt.Sprintf("%s?api-version=%s", subUrl, responsesApiVersion)
			return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, requestURL, info.ChannelType), nil
		}

		model_ := info.UpstreamModelName
		// 2025年5月10日后创建的渠道不移除.
		if info.ChannelCreateTime < constant.AzureNoRemoveDotTime {
			model_ = strings.Replace(model_, ".", "", -1)
		}
		// https://github.com/songquanpeng/one-api/issues/67
		requestURL = fmt.Sprintf("/openai/deployments/%s/%s", model_, task)
		if info.RelayMode == relayconstant.RelayModeRealtime {
			requestURL = fmt.Sprintf("/openai/realtime?deployment=%s&api-version=%s", model_, apiVersion)
		}
		return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, requestURL, info.ChannelType), nil
	//case constant.ChannelTypeMiniMax:
	//	return minimax.GetRequestURL(info)
	case constant.ChannelTypeCustom:
		url := info.ChannelBaseUrl
		url = strings.Replace(url, "{model}", info.UpstreamModelName, -1)
		return url, nil
	default:
		if (info.RelayFormat == types.RelayFormatClaude || info.RelayFormat == types.RelayFormatGemini) &&
			info.RelayMode != relayconstant.RelayModeResponses &&
			info.RelayMode != relayconstant.RelayModeResponsesCompact {
			return fmt.Sprintf("%s/v1/chat/completions", info.ChannelBaseUrl), nil
		}
		return relaycommon.GetFullRequestURL(info.ChannelBaseUrl, info.RequestURLPath, info.ChannelType), nil
	}
}

func (a *Adaptor) SetupRequestHeader(c *gin.Context, header *http.Header, info *relaycommon.RelayInfo) error {
	channel.SetupApiRequestHeader(info, c, header)
	if info.ChannelType == constant.ChannelTypeAzure {
		header.Set("api-key", info.ApiKey)
		return nil
	}
	if info.ChannelType == constant.ChannelTypeOpenAI && "" != info.Organization {
		header.Set("OpenAI-Organization", info.Organization)
	}
	// 检查 Header Override 是否已设置 Authorization，如果已设置则跳过默认设置
	// 这样可以避免在 Header Override 应用时被覆盖（虽然 Header Override 会在之后应用，但这里作为额外保护）
	hasAuthOverride := false
	if len(info.HeadersOverride) > 0 {
		for k := range info.HeadersOverride {
			if strings.EqualFold(k, "Authorization") {
				hasAuthOverride = true
				break
			}
		}
	}
	if info.RelayMode == relayconstant.RelayModeRealtime {
		// OpenAI 已下线 Realtime Beta API,GA 模型收到 beta 标识会以 beta_api_shape_disabled 拒绝;
		// 仅对遗留 preview 模型保留 beta 标识
		legacyRealtimeBeta := strings.Contains(info.UpstreamModelName, "-realtime-preview")
		swp := c.Request.Header.Get("Sec-WebSocket-Protocol")
		if swp != "" {
			items := []string{
				"realtime",
				"openai-insecure-api-key." + info.ApiKey,
			}
			if legacyRealtimeBeta {
				items = append(items, "openai-beta.realtime-v1")
			}
			header.Set("Sec-WebSocket-Protocol", strings.Join(items, ","))
			//req.Header.Set("Sec-WebSocket-Key", c.Request.Header.Get("Sec-WebSocket-Key"))
			//req.Header.Set("Sec-Websocket-Extensions", c.Request.Header.Get("Sec-Websocket-Extensions"))
			//req.Header.Set("Sec-Websocket-Version", c.Request.Header.Get("Sec-Websocket-Version"))
		} else {
			if legacyRealtimeBeta {
				header.Set("openai-beta", "realtime=v1")
			}
			if !hasAuthOverride {
				header.Set("Authorization", "Bearer "+info.ApiKey)
			}
		}
	} else {
		if !hasAuthOverride {
			header.Set("Authorization", "Bearer "+info.ApiKey)
		}
	}
	if info.ChannelType == constant.ChannelTypeOpenRouter {
		if header.Get("HTTP-Referer") == "" {
			header.Set("HTTP-Referer", "https://www.newapi.ai")
		}
		if header.Get("X-OpenRouter-Title") == "" {
			header.Set("X-OpenRouter-Title", "New API")
		}
	}
	return nil
}

func (a *Adaptor) ConvertOpenAIRequest(c *gin.Context, info *relaycommon.RelayInfo, request *dto.GeneralOpenAIRequest) (any, error) {
	if request == nil {
		return nil, errors.New("request is nil")
	}
	if info.ChannelType != constant.ChannelTypeOpenAI && info.ChannelType != constant.ChannelTypeAzure {
		request.StreamOptions = nil
	}
	// Nested reasoning is an OpenRouter-compatible input dialect and needs
	// projection even without a protocol conversion hop. Native top-level
	// reasoning_effort stays untouched unless a modifier or conversion applies.
	// OpenRouter retains its own dialect normalization below.
	preserveSuffix := model_setting.ShouldPreserveThinkingSuffix(info.OriginModelName) || model_setting.ShouldPreserveThinkingSuffix(info.UpstreamModelName)
	upstreamEffort, _ := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(info.UpstreamModelName)
	originEffort, _ := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(info.OriginModelName)
	renderReasoning := len(request.Reasoning) > 0 || len(info.RequestConversionChain) > 1 || request.ReasoningConversion != nil || info.ReasoningState() != nil ||
		!preserveSuffix && (upstreamEffort != "" || originEffort != "")
	if info.ChannelType != constant.ChannelTypeOpenRouter && !renderReasoning {
		info.SetReasoningEffort(request.ReasoningEffort)
	}
	if info.ChannelType == constant.ChannelTypeOpenRouter {
		initialIntent, diagnostics, err := kitreasoning.FromOpenAIChat(request)
		if err != nil {
			return nil, kitreasoning.AsClientError(err)
		}
		recordReasoningDiagnostics(c, info, diagnostics)
		if request.THINKING != nil && strings.HasPrefix(info.UpstreamModelName, "anthropic") {
			var thinking dto.Thinking
			if err := common.Unmarshal(request.THINKING, &thinking); err != nil {
				return nil, fmt.Errorf("error Unmarshal thinking: %w", err)
			}
			legacyIntent, diagnostics, err := kitreasoning.FromClaude(&dto.ClaudeRequest{Thinking: &thinking})
			if err != nil {
				return nil, kitreasoning.AsClientError(err)
			}
			recordReasoningDiagnostics(c, info, diagnostics)
			initialIntent, diagnostics, err = kitreasoning.MergeExplicit(initialIntent, legacyIntent, request.Model)
			if err != nil {
				return nil, kitreasoning.AsClientError(err)
			}
			recordReasoningDiagnostics(c, info, diagnostics)
			request.THINKING = nil
		}
		if len(request.Usage) == 0 {
			request.Usage = json.RawMessage(`{"include":true}`)
		}
		// 合并 effort 尾巴产生的意图
		mergeEffortSuffix := func(modelName string) error {
			rawEffort, _ := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(modelName)
			if rawEffort == "" {
				return nil
			}
			effort, err := kitreasoning.ParseEffort(rawEffort)
			if err != nil {
				return err
			}
			mode := kitreasoning.ModeEnabled
			if effort == kitreasoning.EffortNone {
				mode = kitreasoning.ModeDisabled
			}
			var diagnostics []types.ConversionDiagnostic
			initialIntent, diagnostics, err = kitreasoning.MergeExplicitAndSuffix(initialIntent, kitreasoning.Intent{Mode: mode, Effort: effort, Source: kitreasoning.SourceSuffix}, modelName)
			recordReasoningDiagnostics(c, info, diagnostics)
			return err
		}
		if !preserveSuffix {
			if err := mergeEffortSuffix(info.UpstreamModelName); err != nil {
				return nil, kitreasoning.AsClientError(err)
			}
			if _, baseModel := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(info.UpstreamModelName); baseModel != info.UpstreamModelName {
				info.UpstreamModelName = baseModel
				request.Model = baseModel
			}
			if info.OriginModelName != info.UpstreamModelName {
				if err := mergeEffortSuffix(info.OriginModelName); err != nil {
					return nil, kitreasoning.AsClientError(err)
				}
			}
		}
		if !initialIntent.IsEmpty() {
			reasoningConfig := make(map[string]any)
			if len(request.Reasoning) > 0 {
				if err := common.Unmarshal(request.Reasoning, &reasoningConfig); err != nil {
					return nil, fmt.Errorf("error unmarshalling reasoning: %w", err)
				}
				if reasoningConfig == nil {
					reasoningConfig = make(map[string]any)
				}
			}
			disabled := initialIntent.Mode == kitreasoning.ModeDisabled || initialIntent.Effort == kitreasoning.EffortNone
			if initialIntent.HasStrength() {
				reasoningConfig["enabled"] = !disabled
				if disabled {
					delete(reasoningConfig, "effort")
					delete(reasoningConfig, "max_tokens")
				}
			}
			if !disabled && initialIntent.BudgetTokens != nil {
				reasoningConfig["max_tokens"] = *initialIntent.BudgetTokens
				delete(reasoningConfig, "effort")
			} else if !disabled && initialIntent.Effort != "" && initialIntent.Effort != kitreasoning.EffortNone {
				reasoningConfig["effort"] = string(initialIntent.Effort)
				delete(reasoningConfig, "max_tokens")
			}
			if initialIntent.IncludeThoughts != nil {
				reasoningConfig["exclude"] = !*initialIntent.IncludeThoughts
			}
			marshal, err := common.Marshal(reasoningConfig)
			if err != nil {
				return nil, fmt.Errorf("error marshalling reasoning: %w", err)
			}
			request.Reasoning = marshal
		}
		request.ReasoningEffort = ""
		effectiveEffort := kitreasoning.EffectiveEffort(initialIntent)
		if initialIntent.BudgetTokens != nil {
			effectiveEffort = kitreasoning.EffortFromBudget(*initialIntent.BudgetTokens)
		}
		info.SetReasoningEffort(string(effectiveEffort))

	}
	if info.ChannelType != constant.ChannelTypeOpenRouter && renderReasoning {
		effort, baseModel := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(info.UpstreamModelName)
		if preserveSuffix {
			effort = ""
		}
		currentIntent, diagnostics, err := kitreasoning.FromOpenAIChat(request)
		if err != nil {
			return nil, kitreasoning.AsClientError(err)
		}
		recordReasoningDiagnostics(c, info, diagnostics)
		mergeSuffix := func(modelName, rawEffort string) error {
			if rawEffort == "" {
				return nil
			}
			suffixEffort, err := kitreasoning.ParseEffort(rawEffort)
			if err != nil {
				return err
			}
			mode := kitreasoning.ModeEnabled
			if suffixEffort == kitreasoning.EffortNone {
				mode = kitreasoning.ModeDisabled
			}
			var diagnostics []types.ConversionDiagnostic
			currentIntent, diagnostics, err = kitreasoning.MergeExplicitAndSuffix(currentIntent, kitreasoning.Intent{Mode: mode, Effort: suffixEffort, Source: kitreasoning.SourceSuffix}, modelName)
			recordReasoningDiagnostics(c, info, diagnostics)
			return err
		}
		if err := mergeSuffix(info.UpstreamModelName, effort); err != nil {
			return nil, kitreasoning.AsClientError(err)
		}
		if !preserveSuffix && info.OriginModelName != info.UpstreamModelName {
			originEffort, _ := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(info.OriginModelName)
			if err := mergeSuffix(info.OriginModelName, originEffort); err != nil {
				return nil, kitreasoning.AsClientError(err)
			}
		}
		if effort != "" {
			info.UpstreamModelName = baseModel
			request.Model = baseModel
		}
		if canonicalEffort := kitreasoning.EffectiveEffort(currentIntent); canonicalEffort != "" {
			request.ReasoningEffort = string(canonicalEffort)
			info.SetReasoningEffort(string(canonicalEffort))
		}
		if info.ChannelType == constant.ChannelTypeOpenAI || info.ChannelType == constant.ChannelTypeAzure {
			request.Reasoning = nil
		}
	}

	capabilities := dto.GetOpenAIChatCapabilities(info.UpstreamModelName, info.ReasoningEffort)
	if capabilities.UseMaxCompletionTokens {
		if lo.FromPtrOr(request.MaxCompletionTokens, uint(0)) == 0 && lo.FromPtrOr(request.MaxTokens, uint(0)) != 0 {
			request.MaxCompletionTokens = request.MaxTokens
			request.MaxTokens = nil
		}
	}
	if !capabilities.SupportsTemperature {
		request.Temperature = nil
	}
	if !capabilities.SupportsTopP {
		request.TopP = nil
	}
	if !capabilities.SupportsLogProbs {
		request.LogProbs = nil
		request.TopLogProbs = nil
	}
	if capabilities.UseDeveloperRole && len(request.Messages) > 0 && request.Messages[0].Role == "system" {
		request.Messages[0].Role = "developer"
	}

	return request, nil
}

func (a *Adaptor) ConvertRerankRequest(c *gin.Context, relayMode int, request dto.RerankRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertEmbeddingRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.EmbeddingRequest) (any, error) {
	return request, nil
}

func (a *Adaptor) ConvertAudioRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.AudioRequest) (io.Reader, error) {
	a.ResponseFormat = request.ResponseFormat
	if info.RelayMode == relayconstant.RelayModeAudioSpeech {
		jsonData, err := common.Marshal(request)
		if err != nil {
			return nil, fmt.Errorf("error marshalling object: %w", err)
		}
		return bytes.NewReader(jsonData), nil
	} else {
		var requestBody bytes.Buffer
		writer := multipart.NewWriter(&requestBody)

		writer.WriteField("model", request.Model)

		formData, err2 := common.ParseMultipartFormReusable(c)
		if err2 != nil {
			return nil, fmt.Errorf("error parsing multipart form: %w", err2)
		}

		// 打印类似 curl 命令格式的信息
		logger.LogDebug(c.Request.Context(), "--form 'model=\"%s\"'", request.Model)

		// 遍历表单字段并打印输出
		for key, values := range formData.Value {
			if key == "model" {
				continue
			}
			for _, value := range values {
				writer.WriteField(key, value)
				logger.LogDebug(c.Request.Context(), "--form '%s=\"%s\"'", key, value)
			}
		}

		// 从 formData 中获取文件
		fileHeaders := formData.File["file"]
		if len(fileHeaders) == 0 {
			return nil, errors.New("file is required")
		}

		// 使用 formData 中的第一个文件
		fileHeader := fileHeaders[0]
		logger.LogDebug(c.Request.Context(), "--form 'file=@\"%s\"' (size: %d bytes, content-type: %s)",
			fileHeader.Filename, fileHeader.Size, fileHeader.Header.Get("Content-Type"))

		file, err := fileHeader.Open()
		if err != nil {
			return nil, fmt.Errorf("error opening audio file: %v", err)
		}
		defer file.Close()

		part, err := writer.CreateFormFile("file", fileHeader.Filename)
		if err != nil {
			return nil, errors.New("create form file failed")
		}
		if _, err := io.Copy(part, file); err != nil {
			return nil, errors.New("copy file failed")
		}

		// 关闭 multipart 编写器以设置分界线
		writer.Close()
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		logger.LogDebug(c.Request.Context(), "--header 'Content-Type: %s'", writer.FormDataContentType())
		return &requestBody, nil
	}
}

func (a *Adaptor) ConvertImageRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.ImageRequest) (any, error) {
	switch info.RelayMode {
	case relayconstant.RelayModeImagesEdits:
		if isJSONRequest(c) {
			return request, nil
		}

		var requestBody bytes.Buffer
		writer := multipart.NewWriter(&requestBody)

		writer.WriteField("model", request.Model)
		// 使用已解析的 multipart 表单，避免重复解析
		mf := c.Request.MultipartForm
		if mf == nil {
			form, err := common.ParseMultipartFormReusable(c)
			if err != nil {
				return nil, fmt.Errorf("failed to parse multipart form: %w", err)
			}
			c.Request.MultipartForm = form
			c.Request.PostForm = url.Values(form.Value)
			mf = form
		}

		// 写入所有非文件字段
		if mf != nil {
			for key, values := range mf.Value {
				if key == "model" {
					continue
				}
				for _, value := range values {
					writer.WriteField(key, value)
				}
			}
		}

		if mf != nil && mf.File != nil {
			// Check if "image" field exists in any form, including array notation
			var imageFiles []*multipart.FileHeader
			var exists bool

			// First check for standard "image" field
			if imageFiles, exists = mf.File["image"]; !exists || len(imageFiles) == 0 {
				// If not found, check for "image[]" field
				if imageFiles, exists = mf.File["image[]"]; !exists || len(imageFiles) == 0 {
					// If still not found, iterate through all fields to find any that start with "image["
					foundArrayImages := false
					for fieldName, files := range mf.File {
						if strings.HasPrefix(fieldName, "image[") && len(files) > 0 {
							foundArrayImages = true
							imageFiles = append(imageFiles, files...)
						}
					}

					// If no image fields found at all
					if !foundArrayImages && (len(imageFiles) == 0) {
						return nil, errors.New("image is required")
					}
				}
			}

			// Process all image files
			for i, fileHeader := range imageFiles {
				file, err := fileHeader.Open()
				if err != nil {
					return nil, fmt.Errorf("failed to open image file %d: %w", i, err)
				}

				// If multiple images, use image[] as the field name
				fieldName := "image"
				if len(imageFiles) > 1 {
					fieldName = "image[]"
				}

				// Determine MIME type based on file extension
				mimeType := detectImageMimeType(fileHeader.Filename)

				// Create a form file with the appropriate content type
				h := make(textproto.MIMEHeader)
				h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, fieldName, fileHeader.Filename))
				h.Set("Content-Type", mimeType)

				part, err := writer.CreatePart(h)
				if err != nil {
					return nil, fmt.Errorf("create form part failed for image %d: %w", i, err)
				}

				if _, err := io.Copy(part, file); err != nil {
					return nil, fmt.Errorf("copy file failed for image %d: %w", i, err)
				}

				// 复制完立即关闭，避免在循环内使用 defer 占用资源
				_ = file.Close()
			}

			// Handle mask file if present
			if maskFiles, exists := mf.File["mask"]; exists && len(maskFiles) > 0 {
				maskFile, err := maskFiles[0].Open()
				if err != nil {
					return nil, errors.New("failed to open mask file")
				}
				// 复制完立即关闭，避免在循环内使用 defer 占用资源

				// Determine MIME type for mask file
				mimeType := detectImageMimeType(maskFiles[0].Filename)

				// Create a form file with the appropriate content type
				h := make(textproto.MIMEHeader)
				h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="mask"; filename="%s"`, maskFiles[0].Filename))
				h.Set("Content-Type", mimeType)

				maskPart, err := writer.CreatePart(h)
				if err != nil {
					return nil, errors.New("create form file failed for mask")
				}

				if _, err := io.Copy(maskPart, maskFile); err != nil {
					return nil, errors.New("copy mask file failed")
				}
				_ = maskFile.Close()
			}
		} else {
			return nil, errors.New("no multipart form data found")
		}

		// 关闭 multipart 编写器以设置分界线
		writer.Close()
		c.Request.Header.Set("Content-Type", writer.FormDataContentType())
		return &requestBody, nil

	default:
		return request, nil
	}
}

func isJSONRequest(c *gin.Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	return strings.HasPrefix(c.Request.Header.Get("Content-Type"), "application/json")
}

// detectImageMimeType determines the MIME type based on the file extension
func detectImageMimeType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	default:
		// Try to detect from extension if possible
		if strings.HasPrefix(ext, ".jp") {
			return "image/jpeg"
		}
		// Default to png as a fallback
		return "image/png"
	}
}

func (a *Adaptor) ConvertOpenAIResponsesRequest(c *gin.Context, info *relaycommon.RelayInfo, request dto.OpenAIResponsesRequest) (any, error) {
	//  转换模型推理力度后缀
	effort, originModel := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(request.Model)
	preserveSuffix := model_setting.ShouldPreserveThinkingSuffix(request.Model) || (info != nil && model_setting.ShouldPreserveThinkingSuffix(info.OriginModelName))
	if preserveSuffix {
		effort = ""
	}
	originEffort := ""
	if info != nil && !preserveSuffix {
		originEffort, _ = reasoning.ParseOpenAIReasoningEffortFromModelSuffix(info.OriginModelName)
	}
	crossProtocol := info != nil && len(info.RequestConversionChain) > 1
	if (info == nil || info.ChannelType != constant.ChannelTypeOpenRouter) && !crossProtocol && effort == "" && originEffort == "" && request.ReasoningConversion == nil && info.ReasoningState() == nil {
		if info != nil {
			rawEffort := ""
			if request.Reasoning != nil {
				rawEffort = request.Reasoning.Effort
			}
			info.SetReasoningEffort(rawEffort)
		}
		return request, nil
	}
	currentIntent, diagnostics, err := kitreasoning.FromOpenAIResponses(&request)
	if err != nil {
		return nil, kitreasoning.AsClientError(err)
	}
	recordReasoningDiagnostics(c, info, diagnostics)
	mergeSuffix := func(modelName, rawEffort string) error {
		if rawEffort == "" {
			return nil
		}
		suffixEffort, err := kitreasoning.ParseEffort(rawEffort)
		if err != nil {
			return err
		}
		mode := kitreasoning.ModeEnabled
		if suffixEffort == kitreasoning.EffortNone {
			mode = kitreasoning.ModeDisabled
		}
		var diagnostics []types.ConversionDiagnostic
		currentIntent, diagnostics, err = kitreasoning.MergeExplicitAndSuffix(currentIntent, kitreasoning.Intent{Mode: mode, Effort: suffixEffort, Source: kitreasoning.SourceSuffix}, modelName)
		recordReasoningDiagnostics(c, info, diagnostics)
		return err
	}
	if err := mergeSuffix(request.Model, effort); err != nil {
		return nil, kitreasoning.AsClientError(err)
	}
	if !preserveSuffix && info != nil && info.OriginModelName != request.Model {
		originEffort, _ := reasoning.ParseOpenAIReasoningEffortFromModelSuffix(info.OriginModelName)
		if err := mergeSuffix(info.OriginModelName, originEffort); err != nil {
			return nil, kitreasoning.AsClientError(err)
		}
	}
	if effort != "" {
		request.Model = originModel
		if info != nil {
			info.UpstreamModelName = originModel
		}
	}
	if canonicalEffort := kitreasoning.EffectiveEffort(currentIntent); canonicalEffort != "" {
		if request.Reasoning == nil {
			request.Reasoning = &dto.Reasoning{}
		}
		request.Reasoning.Effort = string(canonicalEffort)
		if info != nil {
			info.SetReasoningEffort(string(canonicalEffort))
		}
	}
	return request, nil
}

func (a *Adaptor) DoRequest(c *gin.Context, info *relaycommon.RelayInfo, requestBody io.Reader) (any, error) {
	if info.RelayMode == relayconstant.RelayModeAudioTranscription ||
		info.RelayMode == relayconstant.RelayModeAudioTranslation ||
		(info.RelayMode == relayconstant.RelayModeImagesEdits && !isJSONRequest(c)) {
		return channel.DoFormRequest(a, c, info, requestBody)
	} else if info.RelayMode == relayconstant.RelayModeRealtime {
		return channel.DoWssRequest(a, c, info, requestBody)
	} else {
		return channel.DoApiRequest(a, c, info, requestBody)
	}
}

func (a *Adaptor) DoResponse(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (usage any, err *types.NewAPIError) {
	switch info.RelayMode {
	case relayconstant.RelayModeRealtime:
		err, usage = OpenaiRealtimeHandler(c, info)
	case relayconstant.RelayModeAudioSpeech:
		usage = OpenaiTTSHandler(c, resp, info)
	case relayconstant.RelayModeAudioTranslation:
		fallthrough
	case relayconstant.RelayModeAudioTranscription:
		err, usage = OpenaiSTTHandler(c, resp, info, a.ResponseFormat)
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits:
		if info.IsStream {
			usage, err = OpenaiImageStreamHandler(c, info, resp)
		} else {
			usage, err = OpenaiImageHandler(c, info, resp)
		}
	case relayconstant.RelayModeRerank:
		usage, err = common_handler.RerankHandler(c, info, resp)
	case relayconstant.RelayModeResponses:
		if info.IsStream {
			usage, err = OaiResponsesStreamHandler(c, info, resp)
		} else {
			usage, err = OaiResponsesHandler(c, info, resp)
		}
	case relayconstant.RelayModeResponsesCompact:
		usage, err = OaiResponsesCompactionHandler(c, resp)
	default:
		if info.IsStream {
			usage, err = OaiStreamHandler(c, info, resp)
		} else {
			usage, err = OpenaiHandler(c, info, resp)
		}
	}
	return
}

func (a *Adaptor) GetModelList() []string {
	switch a.ChannelType {
	case constant.ChannelType360:
		return ai360.ModelList
	case constant.ChannelTypeLingYiWanWu:
		return lingyiwanwu.ModelList
	//case constant.ChannelTypeMiniMax:
	//	return minimax.ModelList
	case constant.ChannelTypeXinference:
		return xinference.ModelList
	case constant.ChannelTypeOpenRouter:
		return openrouter.ModelList
	default:
		return ModelList
	}
}

func (a *Adaptor) GetChannelName() string {
	switch a.ChannelType {
	case constant.ChannelType360:
		return ai360.ChannelName
	case constant.ChannelTypeLingYiWanWu:
		return lingyiwanwu.ChannelName
	//case constant.ChannelTypeMiniMax:
	//	return minimax.ChannelName
	case constant.ChannelTypeXinference:
		return xinference.ChannelName
	case constant.ChannelTypeOpenRouter:
		return openrouter.ChannelName
	default:
		return ChannelName
	}
}

// recordReasoningDiagnostics attaches best-effort reasoning resolutions made
// while merging request fields with model-name effort tails to the request log.
func recordReasoningDiagnostics(c *gin.Context, info *relaycommon.RelayInfo, diagnostics []types.ConversionDiagnostic) {
	if info == nil || len(diagnostics) == 0 {
		return
	}
	for i := range diagnostics {
		diagnostics[i].From = info.RelayFormat
	}
	info.RecordConversionDiagnostics(c, diagnostics)
}
