package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

const (
	kiroAmzTarget = "AmazonCodeWhispererStreamingService.GenerateAssistantResponse"
	maxAttempts   = 2
)

type rpcHostHTTPRequest struct {
	HostCallbackID string      `json:"host_callback_id,omitempty"`
	Method         string      `json:"method"`
	URL            string      `json:"url"`
	Headers        http.Header `json:"headers,omitempty"`
	Body           []byte      `json:"body,omitempty"`
}

type rpcHostHTTPStreamResponse struct {
	StatusCode int         `json:"status_code"`
	Headers    http.Header `json:"headers,omitempty"`
	StreamID   string      `json:"stream_id"`
}

type rpcHostHTTPStreamReadRequest struct {
	StreamID string `json:"stream_id"`
}

type rpcHostHTTPStreamReadResponse struct {
	Payload []byte `json:"payload,omitempty"`
	Error   string `json:"error,omitempty"`
	Done    bool   `json:"done,omitempty"`
}

type rpcHostHTTPStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
}

type rpcStreamEmitRequest struct {
	StreamID string `json:"stream_id"`
	Payload  []byte `json:"payload,omitempty"`
	Error    string `json:"error,omitempty"`
}

type rpcStreamCloseRequest struct {
	StreamID string `json:"stream_id"`
	Error    string `json:"error,omitempty"`
}

type upstreamRequest struct {
	URL     string
	Headers http.Header
	Body    []byte
}

func execute(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode executor request: %w", errUnmarshal)
	}
	upstream, payload, errPrepare := prepareUpstreamRequest(request.ExecutorRequest)
	if errPrepare != nil {
		return prepareRequestErrorEnvelope(errPrepare), nil
	}

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		accumulator := newAccumulator(payload)
		emitted, status, errRun := consumeUpstream(request.HostCallbackID, upstream, attempt, func(events []kiroEvent) error {
			for _, event := range events {
				if _, errAccept := accumulator.accept(event); errAccept != nil {
					return errAccept
				}
			}
			return nil
		})
		if errRun == nil {
			if errFinish := accumulator.finish(); errFinish != nil {
				lastErr = errFinish
			} else {
				body, errMarshal := accumulator.responseJSON()
				if errMarshal != nil {
					return nil, errMarshal
				}
				return okEnvelope(pluginapi.ExecutorResponse{
					Payload:  body,
					Headers:  http.Header{"Content-Type": []string{"application/json"}},
					Metadata: map[string]any{"kiro_credits": accumulator.Credits},
				})
			}
		} else {
			lastErr = errRun
		}
		if emitted || !retryableUpstream(status, lastErr) || attempt == maxAttempts {
			return upstreamErrorEnvelope(status, lastErr), nil
		}
	}
	return upstreamErrorEnvelope(0, lastErr), nil
}

func executeStream(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode streaming executor request: %w", errUnmarshal)
	}
	if strings.TrimSpace(request.StreamID) == "" {
		return errorEnvelope("invalid_request", "stream_id is required", http.StatusBadRequest, false), nil
	}
	upstream, payload, errPrepare := prepareUpstreamRequest(request.ExecutorRequest)
	if errPrepare != nil {
		return prepareRequestErrorEnvelope(errPrepare), nil
	}
	go runStream(request, upstream, payload)
	return okEnvelope(map[string]any{"headers": http.Header{"Content-Type": []string{"text/event-stream"}}})
}

func prepareRequestErrorEnvelope(errPrepare error) []byte {
	if errors.Is(errPrepare, errKiroAPIKeyUnavailable) {
		return errorEnvelope("invalid_auth", errPrepare.Error(), http.StatusUnauthorized, false)
	}
	return errorEnvelope("invalid_request", errPrepare.Error(), http.StatusBadRequest, false)
}

func runStream(request rpcExecutorRequest, upstream upstreamRequest, payload *kiroPayload) {
	var terminalErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			terminalErr = fmt.Errorf("Kiro stream panic: %v", recovered)
		}
		closePluginStream(request.StreamID, terminalErr)
	}()

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		accumulator := newAccumulator(payload)
		writer := newClaudeSSEWriter(accumulator)
		clientEmitted := false
		emitted, status, errRun := consumeUpstream(request.HostCallbackID, upstream, attempt, func(events []kiroEvent) error {
			for _, event := range events {
				blocks, errAccept := accumulator.accept(event)
				if errAccept != nil {
					return errAccept
				}
				if len(blocks) == 0 {
					continue
				}
				if !clientEmitted {
					frames, errStart := writer.start()
					if errStart != nil {
						return errStart
					}
					if errEmit := emitFrames(request.StreamID, frames); errEmit != nil {
						return errEmit
					}
					clientEmitted = true
				}
				frames, errBlocks := writer.blocks(blocks)
				if errBlocks != nil {
					return errBlocks
				}
				if errEmit := emitFrames(request.StreamID, frames); errEmit != nil {
					return errEmit
				}
			}
			return nil
		})
		if errRun == nil {
			blockCount := len(accumulator.Blocks)
			if errFinish := accumulator.finish(); errFinish != nil {
				errRun = errFinish
			} else {
				if !clientEmitted {
					frames, errStart := writer.start()
					if errStart != nil {
						terminalErr = errStart
						return
					}
					if errEmit := emitFrames(request.StreamID, frames); errEmit != nil {
						terminalErr = errEmit
						return
					}
					clientEmitted = true
				}
				if blockCount < len(accumulator.Blocks) {
					frames, errBlocks := writer.blocks(accumulator.Blocks[blockCount:])
					if errBlocks != nil {
						terminalErr = errBlocks
						return
					}
					if errEmit := emitFrames(request.StreamID, frames); errEmit != nil {
						terminalErr = errEmit
						return
					}
				}
				frames, errFinishFrames := writer.finish()
				if errFinishFrames != nil {
					terminalErr = errFinishFrames
					return
				}
				terminalErr = emitFrames(request.StreamID, frames)
				return
			}
		}
		terminalErr = errRun
		if clientEmitted || emitted || !retryableUpstream(status, errRun) || attempt == maxAttempts {
			return
		}
	}
}

func prepareUpstreamRequest(request pluginapi.ExecutorRequest) (upstreamRequest, *kiroPayload, error) {
	credential, handled, errCredential := decodeCredential(request.StorageJSON)
	if errCredential != nil {
		return upstreamRequest{}, nil, errCredential
	}
	if !handled {
		return upstreamRequest{}, nil, fmt.Errorf("selected auth is not a Kiro API-key credential")
	}
	key, errKey := resolveAPIKey(credential)
	if errKey != nil {
		return upstreamRequest{}, nil, errKey
	}
	payload, _, errTranslate := claudeToKiro(request.Payload, request.Model)
	if errTranslate != nil {
		return upstreamRequest{}, nil, errTranslate
	}
	body, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return upstreamRequest{}, nil, fmt.Errorf("encode Kiro request: %w", errMarshal)
	}
	endpoint := "https://runtime." + credential.Region + ".kiro.dev/"
	parsed, errParse := url.Parse(endpoint)
	if errParse != nil || parsed.Scheme != "https" || parsed.Hostname() != "runtime."+credential.Region+".kiro.dev" {
		return upstreamRequest{}, nil, fmt.Errorf("invalid Kiro endpoint")
	}
	cfg := loadedConfig()
	machine := machineID(key)
	userAgent := fmt.Sprintf("aws-sdk-js/1.0.34 ua/2.1 os/%s lang/js md/nodejs#%s api/codewhispererstreaming#1.0.34 m/E KiroIDE-%s-%s", cfg.SystemVersion, cfg.NodeVersion, cfg.KiroVersion, machine)
	amzUserAgent := fmt.Sprintf("aws-sdk-js/1.0.34 KiroIDE-%s-%s", cfg.KiroVersion, machine)
	headers := http.Header{
		"Accept":                      []string{"*/*"},
		"Authorization":               []string{"Bearer " + key},
		"Content-Type":                []string{"application/x-amz-json-1.0"},
		"Tokentype":                   []string{"API_KEY"},
		"User-Agent":                  []string{userAgent},
		"X-Amz-Target":                []string{kiroAmzTarget},
		"X-Amz-User-Agent":            []string{amzUserAgent},
		"X-Amzn-Codewhisperer-Optout": []string{"false"},
	}
	return upstreamRequest{URL: endpoint, Headers: headers, Body: body}, payload, nil
}

func consumeUpstream(hostCallbackID string, request upstreamRequest, attempt int, onEvents func([]kiroEvent) error) (bool, int, error) {
	headers := request.Headers.Clone()
	headers.Set("Amz-Sdk-Request", fmt.Sprintf("attempt=%d; max=%d", attempt, maxAttempts))
	headers.Set("Amz-Sdk-Invocation-Id", randomUUID())
	rawResponse, errCall := invokeHost(pluginabi.MethodHostHTTPDoStream, rpcHostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         http.MethodPost,
		URL:            request.URL,
		Headers:        headers,
		Body:           request.Body,
	})
	if errCall != nil {
		return false, 0, fmt.Errorf("start Kiro upstream stream: %w", errCall)
	}
	var response rpcHostHTTPStreamResponse
	if errUnmarshal := json.Unmarshal(rawResponse, &response); errUnmarshal != nil {
		return false, 0, fmt.Errorf("decode Kiro upstream response: %w", errUnmarshal)
	}
	if strings.TrimSpace(response.StreamID) == "" {
		return false, response.StatusCode, fmt.Errorf("Kiro upstream returned no stream")
	}
	defer closeHostHTTPStream(response.StreamID)
	if response.StatusCode != http.StatusOK {
		body, _ := readErrorBody(response.StreamID, 4096)
		return false, response.StatusCode, fmt.Errorf("Kiro upstream HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}

	decoder := &eventStreamDecoder{}
	emitted := false
	for {
		readRaw, errReadCall := invokeHost(pluginabi.MethodHostHTTPStreamRead, rpcHostHTTPStreamReadRequest{StreamID: response.StreamID})
		if errReadCall != nil {
			return emitted, response.StatusCode, fmt.Errorf("read Kiro upstream stream: %w", errReadCall)
		}
		var readResponse rpcHostHTTPStreamReadResponse
		if errUnmarshal := json.Unmarshal(readRaw, &readResponse); errUnmarshal != nil {
			return emitted, response.StatusCode, fmt.Errorf("decode Kiro stream read: %w", errUnmarshal)
		}
		if readResponse.Error != "" {
			return emitted, response.StatusCode, fmt.Errorf("Kiro upstream stream: %s", readResponse.Error)
		}
		if len(readResponse.Payload) > 0 {
			events, errFeed := decoder.Feed(readResponse.Payload)
			if errFeed != nil {
				return emitted, response.StatusCode, errFeed
			}
			if len(events) > 0 {
				emitted = true
				if errEvents := onEvents(events); errEvents != nil {
					return emitted, response.StatusCode, errEvents
				}
			}
		}
		if readResponse.Done {
			break
		}
	}
	if errFinish := decoder.Finish(); errFinish != nil {
		return emitted, response.StatusCode, errFinish
	}
	return emitted, response.StatusCode, nil
}

func readErrorBody(streamID string, maxBytes int) ([]byte, error) {
	var body bytes.Buffer
	for body.Len() < maxBytes {
		raw, errCall := invokeHost(pluginabi.MethodHostHTTPStreamRead, rpcHostHTTPStreamReadRequest{StreamID: streamID})
		if errCall != nil {
			return body.Bytes(), errCall
		}
		var response rpcHostHTTPStreamReadResponse
		if errUnmarshal := json.Unmarshal(raw, &response); errUnmarshal != nil {
			return body.Bytes(), errUnmarshal
		}
		remaining := maxBytes - body.Len()
		if len(response.Payload) > remaining {
			response.Payload = response.Payload[:remaining]
		}
		body.Write(response.Payload)
		if response.Done || response.Error != "" {
			break
		}
	}
	return body.Bytes(), nil
}

func closeHostHTTPStream(streamID string) {
	if strings.TrimSpace(streamID) == "" {
		return
	}
	_, _ = invokeHost(pluginabi.MethodHostHTTPStreamClose, rpcHostHTTPStreamCloseRequest{StreamID: streamID})
}

func emitFrames(streamID string, frames [][]byte) error {
	for _, frame := range frames {
		if len(frame) == 0 {
			continue
		}
		if _, errCall := invokeHost(pluginabi.MethodHostStreamEmit, rpcStreamEmitRequest{StreamID: streamID, Payload: frame}); errCall != nil {
			return errCall
		}
	}
	return nil
}

func closePluginStream(streamID string, errValue error) {
	message := ""
	if errValue != nil {
		message = errValue.Error()
	}
	_, _ = invokeHost(pluginabi.MethodHostStreamClose, rpcStreamCloseRequest{StreamID: streamID, Error: message})
}

func retryableUpstream(status int, errValue error) bool {
	if errValue == nil {
		return false
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusPaymentRequired || status == http.StatusBadRequest {
		return false
	}
	return status == 0 || status == http.StatusTooManyRequests || status >= 500
}

func upstreamErrorEnvelope(status int, errValue error) []byte {
	if errValue == nil {
		errValue = fmt.Errorf("Kiro upstream request failed")
	}
	if status == 0 {
		status = http.StatusBadGateway
	}
	code := "upstream_error"
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		code = "invalid_auth"
	} else if status == http.StatusTooManyRequests {
		code = "quota_exhausted"
	} else if status == http.StatusPaymentRequired {
		code = "billing_required"
	}
	return errorEnvelope(code, errValue.Error(), status, retryableUpstream(status, errValue))
}

func countTokens(raw []byte) ([]byte, error) {
	var request rpcExecutorRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	var payload any
	if errUnmarshal := json.Unmarshal(request.Payload, &payload); errUnmarshal != nil {
		return errorEnvelope("invalid_request", "invalid Claude token-count request", http.StatusBadRequest, false), nil
	}
	serialized, _ := json.Marshal(payload)
	characters := utf8.RuneCount(serialized)
	estimate := (characters + 3) / 4
	return okEnvelope(pluginapi.ExecutorResponse{
		Payload:  []byte(fmt.Sprintf(`{"input_tokens":%d}`, estimate)),
		Headers:  http.Header{"Content-Type": []string{"application/json"}},
		Metadata: map[string]any{"estimated": true},
	})
}

func executorHTTPRequest(raw []byte) ([]byte, error) {
	var request rpcExecutorHTTPRequest
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	credential, handled, errCredential := decodeCredential(request.StorageJSON)
	if errCredential != nil || !handled {
		return errorEnvelope("invalid_auth", "selected auth is not a Kiro credential", http.StatusUnauthorized, false), nil
	}
	endpoint := "https://runtime." + credential.Region + ".kiro.dev/"
	parsed, errParse := url.Parse(request.URL)
	if errParse != nil || parsed.Scheme != "https" || parsed.Hostname() != "runtime."+credential.Region+".kiro.dev" {
		return errorEnvelope("invalid_request", "Kiro HTTP bridge only permits the configured runtime host", http.StatusBadRequest, false), nil
	}
	key, errKey := resolveAPIKey(credential)
	if errKey != nil {
		return errorEnvelope("invalid_auth", errKey.Error(), http.StatusUnauthorized, false), nil
	}
	headers := request.Headers.Clone()
	headers.Set("Authorization", "Bearer "+key)
	headers.Set("tokentype", "API_KEY")
	resultRaw, errCall := invokeHost(pluginabi.MethodHostHTTPDo, rpcHostHTTPRequest{
		HostCallbackID: request.HostCallbackID,
		Method:         request.Method,
		URL:            firstNonEmpty(request.URL, endpoint),
		Headers:        headers,
		Body:           request.Body,
	})
	if errCall != nil {
		return upstreamErrorEnvelope(0, errCall), nil
	}
	var response pluginapi.ExecutorHTTPResponse
	if errUnmarshal := json.Unmarshal(resultRaw, &response); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	return okEnvelope(response)
}
