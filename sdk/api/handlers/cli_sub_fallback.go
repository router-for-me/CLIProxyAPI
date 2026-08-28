package handlers

import (
	"bytes"
	"context"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	cliSubscriptionsOwner  = "cli-subscriptions"
	cliProxyUpstreamHeader = "x-cliproxy-upstream"
)

var cliSubEffortSuffix = regexp.MustCompile(`(?i)(?:-(?:thinking|non-reasoning|reasoning))?(?:-(?:xhigh|extra-high|high|medium|low|minimal|none|max|pro|agent))?(?:-fast)?$`)

var cliSubVendorRenames = []struct{ from, to string }{
	{"claude-4.5-opus", "claude-opus-4-5"},
	{"claude-4.6-opus", "claude-opus-4-6"},
	{"claude-4.5-sonnet", "claude-sonnet-4-5"},
	{"claude-4.6-sonnet", "claude-sonnet-4-6"},
	{"claude-4-sonnet", "claude-sonnet-4"},
}

var cliSubEffortRank = []string{"max", "xhigh", "extra-high", "high", "medium", "low", "minimal", "none"}

type cliSubFallbackTarget struct {
	Model        string
	Providers    []string
	PrimaryOwner string
}

var (
	cliSubContextSuffix   = regexp.MustCompile(`\[\d+m\]$`)
	cliSubPreviewSuffix   = regexp.MustCompile(`-preview.*$`)
	cliSubDateSuffix      = regexp.MustCompile(`-\d{8}$`)
	cliSubGPTVersion      = regexp.MustCompile(`^gpt-(\d)-(\d)`)
	cliSubThinkingPrimary = regexp.MustCompile(`^claude-(opus|sonnet)-[45]`)
)

func normalizeCLISubModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	model = strings.TrimPrefix(model, "custom/")
	model = cliSubContextSuffix.ReplaceAllString(model, "")
	model = strings.TrimPrefix(model, "cursor-")
	model = strings.TrimPrefix(model, "droid-")
	for range 2 {
		model = cliSubEffortSuffix.ReplaceAllString(model, "")
	}
	for _, rename := range cliSubVendorRenames {
		model = strings.ReplaceAll(model, rename.from, rename.to)
	}
	if strings.HasPrefix(model, "opus-") || strings.HasPrefix(model, "sonnet-") || strings.HasPrefix(model, "haiku-") {
		model = "claude-" + model
	}
	model = cliSubPreviewSuffix.ReplaceAllString(model, "")
	model = cliSubDateSuffix.ReplaceAllString(model, "")
	model = strings.ReplaceAll(model, ".", "-")
	model = cliSubGPTVersion.ReplaceAllString(model, `gpt-$1.$2`)
	return model
}

func cliSubEffortTier(model string) int {
	lower := strings.ToLower(model)
	fastTrimmed := strings.TrimSuffix(lower, "-fast")
	for i, name := range cliSubEffortRank {
		if strings.HasSuffix(fastTrimmed, "-"+name) {
			return i
		}
	}
	return len(cliSubEffortRank)
}

func cliSubCandidateScore(model string, wantThinking bool) [4]int {
	lower := strings.ToLower(model)
	thinking := strings.Contains(lower, "-thinking") || strings.Contains(lower, "-reasoning")
	thinkingPenalty := 1
	if thinking == wantThinking {
		thinkingPenalty = 0
	}
	tier := cliSubEffortTier(lower)
	fast := 0
	if strings.HasSuffix(lower, "-fast") {
		fast = 1
	}
	return [4]int{thinkingPenalty, tier, fast, len(lower)}
}

func lessCLISubCandidate(a, b string, wantThinking bool) bool {
	sa, sb := cliSubCandidateScore(a, wantThinking), cliSubCandidateScore(b, wantThinking)
	for i := range sa {
		if sa[i] != sb[i] {
			return sa[i] < sb[i]
		}
	}
	return a < b
}

func buildCLISubFallbackChain(reg *registry.ModelRegistry, requestedModel string) []cliSubFallbackTarget {
	if reg == nil || strings.TrimSpace(requestedModel) == "" {
		return nil
	}
	requestedInfo := reg.GetModelInfo(requestedModel, "")
	if requestedInfo == nil || strings.EqualFold(strings.TrimSpace(requestedInfo.OwnedBy), cliSubscriptionsOwner) {
		return nil
	}
	primaryOwner := strings.ToLower(strings.TrimSpace(requestedInfo.OwnedBy))
	key := normalizeCLISubModelID(requestedModel)
	if key == "" {
		return nil
	}
	wantThinking := strings.Contains(strings.ToLower(requestedModel), "thinking") || cliSubThinkingPrimary.MatchString(strings.ToLower(requestedModel))
	groups := map[string][]string{"cursor": nil, "droid": nil, "other": nil}
	for _, info := range reg.GetAvailableModelInfos() {
		if info == nil || !strings.EqualFold(strings.TrimSpace(info.OwnedBy), cliSubscriptionsOwner) || normalizeCLISubModelID(info.ID) != key {
			continue
		}
		switch {
		case strings.HasPrefix(strings.ToLower(info.ID), "cursor-"):
			groups["cursor"] = append(groups["cursor"], info.ID)
		case strings.HasPrefix(strings.ToLower(info.ID), "droid-"):
			groups["droid"] = append(groups["droid"], info.ID)
		default:
			groups["other"] = append(groups["other"], info.ID)
		}
	}
	chain := make([]cliSubFallbackTarget, 0, 3)
	for _, group := range []string{"cursor", "droid", "other"} {
		models := groups[group]
		if len(models) == 0 {
			continue
		}
		sort.Slice(models, func(i, j int) bool { return lessCLISubCandidate(models[i], models[j], wantThinking) })
		model := models[0]
		providers := reg.GetModelProviders(model)
		if len(providers) == 0 {
			continue
		}
		chain = append(chain, cliSubFallbackTarget{Model: model, Providers: providers, PrimaryOwner: primaryOwner})
	}
	return chain
}

func fallbackEligible(err error) bool {
	if err == nil {
		return false
	}
	if isAuthSelectionUnavailable(err) {
		return true
	}
	switch statusFromError(err) {
	case http.StatusPaymentRequired, http.StatusTooManyRequests, http.StatusServiceUnavailable:
		return true
	default:
		return false
	}
}

var fallbackResponseModelPaths = []string{"model", "modelVersion", "response.model", "response.modelVersion", "message.model", "usage.model"}

func rewriteFallbackJSONModel(data []byte, targetModel string) ([]byte, string) {
	if len(data) == 0 || strings.TrimSpace(targetModel) == "" || !gjson.ValidBytes(data) {
		return data, ""
	}
	upstream := ""
	for _, path := range fallbackResponseModelPaths {
		value := gjson.GetBytes(data, path)
		if !value.Exists() {
			continue
		}
		if upstream == "" && value.Type == gjson.String && !strings.EqualFold(value.String(), targetModel) {
			upstream = value.String()
		}
		data, _ = sjson.SetBytes(data, path, targetModel)
	}
	return data, upstream
}

func rewriteFallbackResponseModel(data []byte, targetModel string) ([]byte, string) {
	return rewriteFallbackJSONModel(data, targetModel)
}

func rewriteFallbackStreamFrame(frame []byte, targetModel string) ([]byte, string) {
	if len(frame) == 0 {
		return frame, ""
	}
	lines := bytes.Split(frame, []byte("\n"))
	upstream := ""
	for i, line := range lines {
		prefix := []byte(nil)
		jsonData := line
		if bytes.HasPrefix(line, []byte("data: ")) {
			prefix, jsonData = []byte("data: "), line[len("data: "):]
		} else if bytes.HasPrefix(line, []byte("data:")) {
			prefix, jsonData = []byte("data:"), line[len("data:"):]
		}
		rewritten, found := rewriteFallbackJSONModel(jsonData, targetModel)
		if found != "" && upstream == "" {
			upstream = found
		}
		if prefix != nil {
			lines[i] = append(append([]byte{}, prefix...), rewritten...)
		} else {
			lines[i] = rewritten
		}
	}
	return bytes.Join(lines, []byte("\n")), upstream
}

func withFallbackModel(req coreexecutor.Request, opts coreexecutor.Options, requestedModel, fallbackModel string) (coreexecutor.Request, coreexecutor.Options) {
	req.Model = fallbackModel
	if len(req.Payload) > 0 && gjson.ValidBytes(req.Payload) && gjson.GetBytes(req.Payload, "model").Exists() {
		req.Payload, _ = sjson.SetBytes(req.Payload, "model", fallbackModel)
	}
	opts.Metadata = cloneAnyMap(opts.Metadata)
	opts.Metadata[coreexecutor.RequestedModelMetadataKey] = requestedModel
	return req, opts
}

func cloneAnyMap(src map[string]any) map[string]any {
	out := make(map[string]any, len(src)+1)
	for key, value := range src {
		out[key] = value
	}
	return out
}

func preserveCLIProxyUpstreamHeader(downstream, raw http.Header) http.Header {
	value := strings.TrimSpace(raw.Get(cliProxyUpstreamHeader))
	if value == "" {
		return downstream
	}
	if downstream == nil {
		downstream = make(http.Header)
	}
	downstream.Set(cliProxyUpstreamHeader, value)
	return downstream
}

func (h *BaseAPIHandler) executeCLISubFallback(ctx context.Context, requestedModel string, req coreexecutor.Request, opts coreexecutor.Options, initialErr error) (coreexecutor.Response, error) {
	if h == nil || h.AuthManager == nil || !fallbackEligible(initialErr) {
		return coreexecutor.Response{}, initialErr
	}
	for _, target := range buildCLISubFallbackChain(registry.GetGlobalRegistry(), requestedModel) {
		fallbackReq, fallbackOpts := withFallbackModel(req, opts, requestedModel, target.Model)
		resp, errExec := h.AuthManager.Execute(ctx, target.Providers, fallbackReq, fallbackOpts)
		if errExec != nil {
			if fallbackEligible(errExec) {
				continue
			}
			return coreexecutor.Response{}, errExec
		}
		var upstream string
		resp.Payload, upstream = rewriteFallbackResponseModel(resp.Payload, requestedModel)
		if upstream == "" {
			upstream = target.Model
		}
		if resp.Headers == nil {
			resp.Headers = make(http.Header)
		}
		resp.Headers.Set(cliProxyUpstreamHeader, upstream)
		log.WithFields(log.Fields{"model": requestedModel, "upstream": upstream}).Info("CLI-sub fallback served request")
		return resp, nil
	}
	return coreexecutor.Response{}, initialErr
}

func readCLISubFallbackStreamBootstrap(ctx context.Context, chunks <-chan coreexecutor.StreamChunk) ([]coreexecutor.StreamChunk, bool, error) {
	buffered := make([]coreexecutor.StreamChunk, 0, 1)
	for {
		var (
			chunk coreexecutor.StreamChunk
			ok    bool
		)
		if ctx != nil {
			select {
			case <-ctx.Done():
				return nil, false, ctx.Err()
			case chunk, ok = <-chunks:
			}
		} else {
			chunk, ok = <-chunks
		}
		if !ok {
			return buffered, true, nil
		}
		if chunk.Err != nil {
			return nil, false, chunk.Err
		}
		buffered = append(buffered, chunk)
		if len(chunk.Payload) > 0 {
			return buffered, false, nil
		}
	}
}

func (h *BaseAPIHandler) executeCLISubFallbackStream(ctx context.Context, requestedModel string, req coreexecutor.Request, opts coreexecutor.Options, initialErr error) (*coreexecutor.StreamResult, error) {
	if h == nil || h.AuthManager == nil || !fallbackEligible(initialErr) {
		return nil, initialErr
	}
	for _, target := range buildCLISubFallbackChain(registry.GetGlobalRegistry(), requestedModel) {
		fallbackReq, fallbackOpts := withFallbackModel(req, opts, requestedModel, target.Model)
		result, errExec := h.AuthManager.ExecuteStream(ctx, target.Providers, fallbackReq, fallbackOpts)
		if errExec != nil {
			if fallbackEligible(errExec) {
				continue
			}
			return nil, errExec
		}
		if result == nil || result.Chunks == nil {
			continue
		}
		headers := result.Headers.Clone()
		if headers == nil {
			headers = make(http.Header)
		}
		buffered, closed, errBootstrap := readCLISubFallbackStreamBootstrap(ctx, result.Chunks)
		if errBootstrap != nil {
			if fallbackEligible(errBootstrap) {
				continue
			}
			return nil, errBootstrap
		}
		upstream := target.Model
		for i := range buffered {
			if len(buffered[i].Payload) == 0 {
				continue
			}
			var found string
			buffered[i].Payload, found = rewriteFallbackStreamFrame(buffered[i].Payload, requestedModel)
			if found != "" {
				upstream = found
			}
		}
		headers.Set(cliProxyUpstreamHeader, upstream)
		out := make(chan coreexecutor.StreamChunk)
		go func(chunks <-chan coreexecutor.StreamChunk, initial []coreexecutor.StreamChunk, streamClosed bool) {
			defer close(out)
			for _, chunk := range initial {
				out <- chunk
			}
			if streamClosed {
				return
			}
			for chunk := range chunks {
				if len(chunk.Payload) > 0 {
					chunk.Payload, _ = rewriteFallbackStreamFrame(chunk.Payload, requestedModel)
				}
				out <- chunk
			}
		}(result.Chunks, buffered, closed)
		log.WithFields(log.Fields{"model": requestedModel, "upstream": upstream}).Info("CLI-sub fallback served streaming request")
		return &coreexecutor.StreamResult{Headers: headers, Chunks: out}, nil
	}
	return nil, initialErr
}
