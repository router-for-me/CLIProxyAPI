package executor

import (
	"context"
	"net/http"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// executeDevinResponsesCompaction handles Codex remote compaction v2 for the
// native Devin route. Devin has no native compaction endpoint, so the summary
// is generated with the same model through the normal Execute path and sealed
// into a CPA compaction capsule, mirroring the Kimi/Meta/OpenAI-compat routes.
func (e *DevinExecutor) executeDevinResponsesCompaction(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	var resp cliproxyexecutor.Response
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryPayload := helps.PrepareAntigravityCompactionSummaryPayload(helps.ResponsesCompactionSourcePayload(req, opts), baseModel)
	summaryReq := cliproxyexecutor.Request{
		Model:    req.Model,
		Payload:  summaryPayload,
		Metadata: req.Metadata,
	}
	summaryOpts := opts
	summaryOpts.Alt = ""
	summaryOpts.Stream = false
	summaryOpts.OriginalRequest = nil
	summaryOpts.SourceFormat = sdktranslator.FormatOpenAIResponse
	summaryOpts.ResponseFormat = sdktranslator.FormatOpenAIResponse

	summaryResp, errSummary := e.Execute(ctx, auth, summaryReq, summaryOpts)
	if errSummary != nil {
		return resp, errSummary
	}
	respPayload, errBuild := helps.BuildResponsesCompactionResponseFromSummary(baseModel, summaryResp.Payload)
	if errBuild != nil {
		return resp, statusErr{code: http.StatusBadGateway, msg: errBuild.Error()}
	}
	return cliproxyexecutor.Response{
		Payload: respPayload,
		Headers: summaryResp.Headers,
	}, nil
}

// executeDevinResponsesCompactionStream emits the sealed compaction response
// as the Responses SSE event sequence Codex expects for remote compaction v2.
func (e *DevinExecutor) executeDevinResponsesCompactionStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	summaryResp, errSummary := e.executeDevinResponsesCompaction(ctx, auth, req, opts)
	if errSummary != nil {
		return nil, errSummary
	}
	chunks, errChunks := helps.BuildResponsesCompactionStreamChunksFromResponse(baseModel, summaryResp.Payload)
	if errChunks != nil {
		return nil, statusErr{code: http.StatusBadGateway, msg: errChunks.Error()}
	}
	out := make(chan cliproxyexecutor.StreamChunk, len(chunks))
	for _, chunk := range chunks {
		out <- cliproxyexecutor.StreamChunk{Payload: chunk}
	}
	close(out)

	headers := summaryResp.Headers.Clone()
	if headers == nil {
		headers = make(http.Header)
	}
	headers.Set("Content-Type", "text/event-stream")
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}
