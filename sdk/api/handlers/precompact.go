package handlers

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/precompact"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	precompactMu       sync.Mutex
	precompactByHandle = map[*BaseAPIHandler]*precompact.Interceptor{}
)

// precompactConfig returns the live pre-compact config (zero value = disabled).
func (h *BaseAPIHandler) precompactConfig() config.PreCompactConfig {
	if h == nil || h.Cfg == nil {
		return config.PreCompactConfig{}
	}
	return h.Cfg.PreCompact
}

func (h *BaseAPIHandler) precompactEnabled() bool {
	return h.precompactConfig().Enabled
}

// precompactInterceptor lazily builds one interceptor per handler so the
// per-session summary cache survives across requests and config reloads.
func (h *BaseAPIHandler) precompactInterceptor() *precompact.Interceptor {
	if h == nil {
		return nil
	}
	precompactMu.Lock()
	defer precompactMu.Unlock()
	if it, ok := precompactByHandle[h]; ok {
		return it
	}
	it := precompact.New(h.precompactConfig(), registry.GetGlobalRegistry(), &auxSummarizer{h: h})
	precompactByHandle[h] = it
	return it
}

// applyPreCompact replaces req.Body when the request exceeds the selected
// model's budget. It never returns an error: on any failure the original body
// is forwarded and the failure is logged inside the precompact package.
func (h *BaseAPIHandler) applyPreCompact(ctx context.Context, req coreexecutor.RequestAfterAuthInterceptRequest) []byte {
	cfg := h.precompactConfig()
	if !cfg.Enabled {
		return nil
	}
	body, _ := h.precompactInterceptor().Apply(ctx, cfg, req)
	return body
}

// auxSummarizer runs the summary call through the handler's own execution
// path (InternalSource is set by ExecuteModel, so pre-compaction skips it).
type auxSummarizer struct {
	h *BaseAPIHandler
}

func (s *auxSummarizer) Summarize(ctx context.Context, model, previous, transcript string) (string, error) {
	if s == nil || s.h == nil {
		return "", fmt.Errorf("precompact: no handler")
	}
	body := []byte(`{"max_tokens":4096,"messages":[]}`)
	body, _ = sjson.SetBytes(body, "model", model)
	body, _ = sjson.SetBytes(body, "system", precompact.SummaryInstruction)
	msg, _ := sjson.Set(`{"role":"user"}`, "content", precompact.BuildPrompt(previous, transcript))
	body, _ = sjson.SetRawBytes(body, "messages.-1", []byte(msg))

	resp, errMsg := s.h.ExecuteModel(ctx, ModelExecutionRequest{
		EntryProtocol: "claude",
		Model:         model,
		Body:          body,
	})
	if errMsg != nil {
		if errMsg.Error != nil {
			return "", fmt.Errorf("aux call status %d: %w", errMsg.StatusCode, errMsg.Error)
		}
		return "", fmt.Errorf("aux call status %d", errMsg.StatusCode)
	}
	var b strings.Builder
	for _, block := range gjson.GetBytes(resp.Body, "content").Array() {
		if block.Get("type").String() == "text" {
			b.WriteString(block.Get("text").String())
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("aux call returned no text")
	}
	return out, nil
}
