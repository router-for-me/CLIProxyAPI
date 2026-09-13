package claudemaster

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
)

// ProbeResult deliberately excludes provider response bodies, headers, and
// account details. Stage is an in-process fixed enum, never provider-supplied
// response text or a header. These fields are safe for the CLI to print.
type ProbeResult struct {
	Status  int
	Matched bool
	Stage   BackendErrorStage
}

// Probe makes one small inference request without native Claude or a CONNECT
// proxy. The caller must hold the selected profile lock until this returns.
// Normal same-account OAuth refresh is allowed; no account fallback is added.
func Probe(ctx context.Context, profile Profile, model string) (ProbeResult, error) {
	if ctx == nil || strings.TrimSpace(model) == "" {
		return ProbeResult{}, errors.New("probe requires a context and selected model")
	}
	backend, err := NewBackend(ctx, BackendOptions{AuthDir: profile.AuthDir, Provider: profile.Provider, AuthID: profile.AuthID, Model: model})
	if err != nil {
		return ProbeResult{Stage: BackendErrorInitialization}, errors.New("cannot initialize the selected inference probe")
	}
	// The direct handler returns before closing the backend and joining refresh.
	defer func() { _ = backend.Close() }()
	return probeHandler(ctx, backend.Handler(), model)
}

const probeMarker = "INFERENCE_OK"
const probeMaxResponseBytes = 64 << 10

func probeHandler(ctx context.Context, handler http.Handler, model string) (ProbeResult, error) {
	if ctx == nil || handler == nil {
		return ProbeResult{}, errors.New("probe requires a context and inference handler")
	}
	if err := ctx.Err(); err != nil {
		return ProbeResult{Stage: classifyBackendError(err)}, err
	}
	observation := &backendErrorObservation{}
	ctx = context.WithValue(ctx, backendErrorObservationKey{}, observation)
	payload, err := json.Marshal(map[string]any{
		"model":      model,
		"stream":     false,
		"max_tokens": 32,
		"messages":   []map[string]string{{"role": "user", "content": "Reply with exactly INFERENCE_OK and nothing else."}},
	})
	if err != nil {
		return ProbeResult{}, errors.New("cannot construct inference probe")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v1/messages", bytes.NewReader(payload))
	if err != nil {
		return ProbeResult{}, errors.New("cannot construct inference probe request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	writer := &probeResponseWriter{header: make(http.Header)}
	handler.ServeHTTP(writer, request)
	status := writer.status
	if status == 0 {
		status = http.StatusOK
	}
	result := ProbeResult{Status: status, Stage: observation.result()}
	if err := ctx.Err(); err != nil {
		if result.Stage == BackendErrorNone {
			result.Stage = classifyBackendError(err)
		}
		return result, err
	}
	if writer.oversized {
		result.Stage = BackendErrorResponse
		return result, errors.New("inference probe response exceeded its capture limit")
	}
	if status != http.StatusOK {
		if result.Stage == BackendErrorNone {
			result.Stage = BackendErrorInternal
		}
		// Never parse or propagate an upstream error body, which can contain
		// provider diagnostics, account identifiers, or reflected input.
		return result, nil
	}
	var response struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(writer.body.Bytes(), &response); err != nil {
		result.Stage = BackendErrorResponse
		return result, errors.New("inference probe returned an invalid message")
	}
	if response.Type != "message" || len(response.Content) == 0 {
		return result, nil
	}
	var text strings.Builder
	for _, block := range response.Content {
		if block.Type != "text" {
			return result, nil
		}
		text.WriteString(block.Text)
	}
	result.Matched = strings.TrimSpace(text.String()) == probeMarker
	return result, nil
}

// probeResponseWriter captures a bounded diagnostic response without opening
// a port, printing content, or allowing a provider response to grow memory.
type probeResponseWriter struct {
	header    http.Header
	status    int
	body      bytes.Buffer
	oversized bool
}

func (w *probeResponseWriter) Header() http.Header { return w.header }

func (w *probeResponseWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
	}
}

func (w *probeResponseWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	remaining := probeMaxResponseBytes - w.body.Len()
	if len(data) > remaining {
		_, _ = w.body.Write(data[:remaining])
		w.oversized = true
	} else {
		_, _ = w.body.Write(data)
	}
	// Discard overflow while satisfying the handler's writer contract; the
	// probe reports the capture error once this one request has completed.
	return len(data), nil
}
