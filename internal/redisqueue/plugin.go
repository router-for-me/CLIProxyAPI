package redisqueue

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coresession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func init() {
	coreusage.RegisterPlugin(&usageQueuePlugin{})
}

type usageQueuePlugin struct{}

func (p *usageQueuePlugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil {
		return
	}
	if !Enabled() || !UsageStatisticsEnabled() {
		return
	}

	timestamp := record.RequestedAt
	if timestamp.IsZero() {
		timestamp = time.Now()
	}

	modelName := strings.TrimSpace(record.Model)
	if modelName == "" {
		modelName = "unknown"
	}
	aliasName := strings.TrimSpace(record.Alias)
	if aliasName == "" {
		aliasName = modelName
	}
	provider := strings.TrimSpace(record.Provider)
	if provider == "" {
		provider = "unknown"
	}
	executorType := strings.TrimSpace(record.ExecutorType)
	if executorType == "" {
		executorType = "unknown"
	}
	authType := strings.TrimSpace(record.AuthType)
	if authType == "" {
		authType = "unknown"
	}
	apiKey := strings.TrimSpace(record.APIKey)
	accessProvider := strings.TrimSpace(record.AccessProvider)
	if accessProvider == "" {
		accessProvider = strings.TrimSpace(coreusage.AccessProviderFromContext(ctx))
	}
	isNativeKey := record.IsNativeKey
	if !isNativeKey {
		if nativeExplicit, ok := coreusage.IsNativeKeyFromContext(ctx); ok {
			isNativeKey = nativeExplicit
		} else if accessProvider != "" {
			isNativeKey = coreusage.IsNativeAccessProvider(accessProvider)
		}
	}
	requestID := strings.TrimSpace(internallogging.GetRequestID(ctx))
	traceID := strings.TrimSpace(record.TraceID)
	if traceID == "" {
		traceID = requestID
	}
	executionID := strings.TrimSpace(record.RequestID)
	if executionID == "" {
		executionID = uuid.NewString()
	}
	reasoningEffort := strings.TrimSpace(record.ReasoningEffort)
	if reasoningEffort == "" {
		reasoningEffort = coreusage.ReasoningEffortFromContext(ctx)
	}
	serviceTier := strings.TrimSpace(record.ServiceTier)
	if serviceTier == "" {
		serviceTier = strings.TrimSpace(record.RequestServiceTier)
	}
	if serviceTier == "" {
		serviceTier = coreusage.ServiceTierFromContext(ctx)
	}
	effectiveServiceTier := strings.TrimSpace(record.EffectiveServiceTier)
	responseServiceTier := strings.TrimSpace(record.ResponseServiceTier)
	responseModel := strings.TrimSpace(record.ResponseModel)
	clientRequestMetadata := internallogging.GetClientRequestMetadata(ctx)
	sessionID := strings.TrimSpace(record.SessionID)
	parentSessionID := strings.TrimSpace(record.ParentSessionID)
	if sessionID == "" {
		sessionID = strings.TrimSpace(clientRequestMetadata.SessionID)
		parentSessionID = strings.TrimSpace(clientRequestMetadata.ParentSessionID)
	} else if parentSessionID == "" && sessionID == strings.TrimSpace(clientRequestMetadata.SessionID) {
		parentSessionID = strings.TrimSpace(clientRequestMetadata.ParentSessionID)
	}
	sessionID = coresession.NormalizeToCanonicalUUID(sessionID)
	parentSessionID = coresession.NormalizeToCanonicalUUID(parentSessionID)
	if sessionID == "" || sessionID == parentSessionID {
		parentSessionID = ""
	}

	usageDetail := coreusage.EnsureTokenBreakdownForProvider(record.Detail, record.Provider, record.ExecutorType)
	tokens := tokenStats{
		InputTokens:              usageDetail.InputTokens,
		OutputTokens:             usageDetail.OutputTokens,
		ReasoningTokens:          usageDetail.ReasoningTokens,
		CachedTokens:             usageDetail.CachedTokens,
		CacheReadTokens:          usageDetail.CacheReadTokens,
		CacheReadTokensPresent:   true,
		CacheCreationTokens:      usageDetail.CacheCreationTokens,
		CacheCreation5mTokens:    coreusage.CloneInt64Ptr(usageDetail.CacheCreation5mTokens),
		CacheCreation30mTokens:   coreusage.CloneInt64Ptr(usageDetail.CacheCreation30mTokens),
		CacheCreation1hTokens:    coreusage.CloneInt64Ptr(usageDetail.CacheCreation1hTokens),
		CacheCreation12hTokens:   coreusage.CloneInt64Ptr(usageDetail.CacheCreation12hTokens),
		TextInputTokens:          coreusage.CloneInt64Ptr(usageDetail.TextInputTokens),
		ImageInputTokens:         coreusage.CloneInt64Ptr(usageDetail.ImageInputTokens),
		AudioInputTokens:         coreusage.CloneInt64Ptr(usageDetail.AudioInputTokens),
		VideoInputTokens:         coreusage.CloneInt64Ptr(usageDetail.VideoInputTokens),
		FileInputTokens:          coreusage.CloneInt64Ptr(usageDetail.FileInputTokens),
		CachedTextTokens:         coreusage.CloneInt64Ptr(usageDetail.CachedTextTokens),
		TextOutputTokens:         coreusage.CloneInt64Ptr(usageDetail.TextOutputTokens),
		ImageOutputTokens:        coreusage.CloneInt64Ptr(usageDetail.ImageOutputTokens),
		AudioOutputTokens:        coreusage.CloneInt64Ptr(usageDetail.AudioOutputTokens),
		AcceptedPredictionTokens: coreusage.CloneInt64Ptr(usageDetail.AcceptedPredictionTokens),
		RejectedPredictionTokens: coreusage.CloneInt64Ptr(usageDetail.RejectedPredictionTokens),
		TotalTokens:              usageDetail.TotalTokens,
	}

	failed := record.Failed
	if !failed {
		failed = !resolveSuccess(ctx)
	}
	fail := resolveFail(ctx, record, failed)

	stream := record.Stream
	if !stream {
		stream = coreusage.StreamFromContext(ctx)
	}

	detail := requestDetail{
		Timestamp:        timestamp,
		LatencyMs:        record.Latency.Milliseconds(),
		TTFTMs:           record.TTFT.Milliseconds(),
		Source:           record.Source,
		AuthIndex:        record.AuthIndex,
		AccessTokenHash:  record.AccessTokenSHA256,
		ClientIP:         clientRequestMetadata.ClientIP,
		ResolvedClientIP: clientRequestMetadata.ResolvedClientIP,
		XForwardedFor:    clientRequestMetadata.XForwardedFor,
		UserAgent:        clientRequestMetadata.UserAgent,
		Tokens:           tokens,
		Failed:           failed,
		Generate:         coreusage.GenerateEnabled(record.Generate),
		Stream:           stream,
		Fail:             fail,
		ResponseHeaders:  record.ResponseHeaders,
	}

	payload, err := json.Marshal(queuedUsageDetail{
		requestDetail:         detail,
		AccountingVersion:     coreusage.TokenAccountingSchemaVersion,
		TokenBreakdown:        usageDetail.TokenBreakdown,
		Provider:              provider,
		ExecutorType:          executorType,
		Model:                 modelName,
		Alias:                 aliasName,
		Endpoint:              resolveEndpoint(ctx),
		AuthType:              authType,
		APIKey:                apiKey,
		IsNativeKey:           isNativeKey,
		AccessProvider:        accessProvider,
		RequestID:             requestID,
		ExecutionID:           executionID,
		TraceID:               traceID,
		SessionID:             sessionID,
		ParentSessionID:       parentSessionID,
		NodeKind:              strings.TrimSpace(clientRequestMetadata.NodeKind),
		IsFork:                clientRequestMetadata.IsFork,
		IsCompaction:          clientRequestMetadata.IsCompaction,
		ReasoningEffort:       reasoningEffort,
		ServiceTier:           serviceTier,
		EffectiveServiceTier:  effectiveServiceTier,
		ResponseServiceTier:   responseServiceTier,
		UpstreamServiceTier:   strings.TrimSpace(usageDetail.UpstreamServiceTier),
		ResponseModel:         responseModel,
		Speed:                 strings.TrimSpace(usageDetail.Speed),
		InferenceGeo:          strings.TrimSpace(usageDetail.InferenceGeo),
		CacheDetails:          usageDetail.CacheDetails.Clone(),
		WebSearchRequests:     coreusage.CloneInt64Ptr(usageDetail.WebSearchRequests),
		ImageCount:            coreusage.CloneInt64Ptr(usageDetail.ImageCount),
		RequestedImageDetails: usageDetail.RequestedImageDetails.Clone(),
		ImageDetails:          usageDetail.ImageDetails.Clone(),
		MediaCounts:           usageDetail.MediaCounts.Clone(),
		ToolUsageDetails:      usageDetail.ToolUsageDetails.Clone(),
		Iterations:            usageDetail.IterationSlice(),
		CostDetails:           usageDetail.CostDetails.Clone(),
	})
	if err != nil {
		return
	}
	Enqueue(payload)
}

type queuedUsageDetail struct {
	requestDetail
	AccountingVersion     int                         `json:"accounting_version"`
	TokenBreakdown        coreusage.TokenBreakdown    `json:"token_breakdown"`
	Provider              string                      `json:"provider"`
	ExecutorType          string                      `json:"executor_type"`
	Model                 string                      `json:"model"`
	Alias                 string                      `json:"alias"`
	Endpoint              string                      `json:"endpoint"`
	AuthType              string                      `json:"auth_type"`
	APIKey                string                      `json:"api_key"`
	IsNativeKey           bool                        `json:"is_native_key"`
	AccessProvider        string                      `json:"access_provider,omitempty"`
	RequestID             string                      `json:"request_id"`
	ExecutionID           string                      `json:"execution_id,omitempty"`
	TraceID               string                      `json:"trace_id,omitempty"`
	SessionID             string                      `json:"session_id,omitempty"`
	ParentSessionID       string                      `json:"parent_session_id,omitempty"`
	NodeKind              string                      `json:"node_kind,omitempty"`
	IsFork                bool                        `json:"is_fork,omitempty"`
	IsCompaction          bool                        `json:"is_compaction,omitempty"`
	ReasoningEffort       string                      `json:"reasoning_effort"`
	ServiceTier           string                      `json:"service_tier"`
	EffectiveServiceTier  string                      `json:"effective_service_tier,omitempty"`
	ResponseServiceTier   string                      `json:"response_service_tier,omitempty"`
	UpstreamServiceTier   string                      `json:"upstream_service_tier,omitempty"`
	ResponseModel         string                      `json:"response_model,omitempty"`
	Speed                 string                      `json:"speed,omitempty"`
	InferenceGeo          string                      `json:"inference_geo,omitempty"`
	CacheDetails          *coreusage.CacheDetails     `json:"cache_details,omitempty"`
	WebSearchRequests     *int64                      `json:"web_search_requests,omitempty"`
	ImageCount            *int64                      `json:"image_count,omitempty"`
	RequestedImageDetails *coreusage.ImageDetails     `json:"requested_image_details,omitempty"`
	ImageDetails          *coreusage.ImageDetails     `json:"image_details,omitempty"`
	MediaCounts           *coreusage.MediaCounts      `json:"media_counts,omitempty"`
	ToolUsageDetails      *coreusage.ToolUsageDetails `json:"tool_usage_details,omitempty"`
	Iterations            []coreusage.IterationUsage  `json:"iterations,omitempty"`
	CostDetails           *coreusage.CostDetails      `json:"cost_details,omitempty"`
}

type requestDetail struct {
	Timestamp        time.Time   `json:"timestamp"`
	LatencyMs        int64       `json:"latency_ms"`
	TTFTMs           int64       `json:"ttft_ms"`
	Source           string      `json:"source"`
	AuthIndex        string      `json:"auth_index"`
	AccessTokenHash  string      `json:"access_token_sha256,omitempty"`
	ClientIP         string      `json:"client_ip"`
	ResolvedClientIP string      `json:"resolved_client_ip"`
	XForwardedFor    string      `json:"x_forwarded_for"`
	UserAgent        string      `json:"user_agent"`
	Tokens           tokenStats  `json:"tokens"`
	Failed           bool        `json:"failed"`
	Generate         bool        `json:"generate"`
	Stream           bool        `json:"stream"`
	Fail             failDetail  `json:"fail"`
	ResponseHeaders  http.Header `json:"response_headers,omitempty"`
}

type tokenStats struct {
	InputTokens              int64  `json:"input_tokens"`
	OutputTokens             int64  `json:"output_tokens"`
	ReasoningTokens          int64  `json:"reasoning_tokens"`
	CachedTokens             int64  `json:"cached_tokens"`
	CacheReadTokens          int64  `json:"cache_read_tokens"`
	CacheReadTokensPresent   bool   `json:"cache_read_tokens_present"`
	CacheCreationTokens      int64  `json:"cache_creation_tokens"`
	CacheCreation5mTokens    *int64 `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation30mTokens   *int64 `json:"cache_creation_30m_tokens,omitempty"`
	CacheCreation1hTokens    *int64 `json:"cache_creation_1h_tokens,omitempty"`
	CacheCreation12hTokens   *int64 `json:"cache_creation_12h_tokens,omitempty"`
	TextInputTokens          *int64 `json:"text_input_tokens,omitempty"`
	ImageInputTokens         *int64 `json:"image_input_tokens,omitempty"`
	AudioInputTokens         *int64 `json:"audio_input_tokens,omitempty"`
	VideoInputTokens         *int64 `json:"video_input_tokens,omitempty"`
	FileInputTokens          *int64 `json:"file_input_tokens,omitempty"`
	CachedTextTokens         *int64 `json:"cached_text_tokens,omitempty"`
	TextOutputTokens         *int64 `json:"text_output_tokens,omitempty"`
	ImageOutputTokens        *int64 `json:"image_output_tokens,omitempty"`
	AudioOutputTokens        *int64 `json:"audio_output_tokens,omitempty"`
	AcceptedPredictionTokens *int64 `json:"accepted_prediction_tokens,omitempty"`
	RejectedPredictionTokens *int64 `json:"rejected_prediction_tokens,omitempty"`
	TotalTokens              int64  `json:"total_tokens"`
}

type failDetail struct {
	StatusCode int    `json:"status_code"`
	Body       string `json:"body"`
	Code       string `json:"code,omitempty"`
}

func resolveFail(ctx context.Context, record coreusage.Record, failed bool) failDetail {
	fail := failDetail{
		StatusCode: record.Fail.StatusCode,
		Body:       strings.TrimSpace(record.Fail.Body),
		Code:       strings.TrimSpace(record.Fail.Code),
	}
	if !failed {
		return failDetail{StatusCode: 200}
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = internallogging.GetResponseStatus(ctx)
	}
	if fail.StatusCode <= 0 {
		fail.StatusCode = 500
	}
	return fail
}

func resolveSuccess(ctx context.Context) bool {
	status := internallogging.GetResponseStatus(ctx)
	if status == 0 {
		return true
	}
	return status < httpStatusBadRequest
}

func resolveEndpoint(ctx context.Context) string {
	return strings.TrimSpace(internallogging.GetEndpoint(ctx))
}

const httpStatusBadRequest = 400
