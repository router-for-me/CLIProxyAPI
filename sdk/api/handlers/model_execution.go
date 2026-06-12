package handlers

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"golang.org/x/net/context"
)

const (
	modelExecutionMetadataSourceKey = "source"
	modelExecutionInternalSource    = "plugin_host_model_callback"
)

type modelExecutionOptions struct {
	Headers                 http.Header
	Query                   url.Values
	InternalSource          bool
	SkipInterceptorPluginID string
}

// ModelExecutionRequest describes an internal model execution request.
type ModelExecutionRequest struct {
	EntryProtocol           string
	ExitProtocol            string
	Model                   string
	Stream                  bool
	Body                    []byte
	Headers                 http.Header
	Query                   url.Values
	Alt                     string
	SkipInterceptorPluginID string
}

// ModelExecutionResponse describes a non-streaming internal model execution response.
type ModelExecutionResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// ModelExecutionStream describes a streaming internal model execution response.
type ModelExecutionStream struct {
	StatusCode int
	Headers    http.Header
	Chunks     <-chan ModelExecutionChunk
}

// ModelExecutionChunk carries either a streaming payload or a terminal stream error.
type ModelExecutionChunk struct {
	Payload []byte
	Err     *ModelExecutionStreamError
}

// ModelExecutionStreamError carries a JSON-friendly terminal stream error.
type ModelExecutionStreamError struct {
	StatusCode int         `json:"status_code"`
	Message    string      `json:"message"`
	Headers    http.Header `json:"headers"`
}

// Error returns the stream error message or the HTTP status text.
func (e *ModelExecutionStreamError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return http.StatusText(e.StatusCode)
}

// ExecuteModel executes an internal non-streaming model request.
// Host model callbacks are non-recursive for their caller: when
// SkipInterceptorPluginID is set, that plugin's interceptors are skipped for the
// nested model execution while other plugins may still run.
func (h *BaseAPIHandler) ExecuteModel(ctx context.Context, req ModelExecutionRequest) (ModelExecutionResponse, *interfaces.ErrorMessage) {
	if req.Stream {
		return ModelExecutionResponse{}, modelExecutionModeError("ExecuteModel requires Stream=false")
	}
	body, headers, errMsg := h.executeWithAuthManagerFormats(ctx, req.EntryProtocol, req.ExitProtocol, req.Model, cloneBytes(req.Body), req.Alt, false, modelExecutionOptions{
		Headers:                 req.Headers,
		Query:                   req.Query,
		InternalSource:          true,
		SkipInterceptorPluginID: req.SkipInterceptorPluginID,
	})
	if errMsg != nil {
		return ModelExecutionResponse{}, errMsg
	}
	return ModelExecutionResponse{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Body:       cloneBytes(body),
	}, nil
}

// ExecuteModelStream executes an internal streaming model request.
// Host model callbacks are non-recursive for their caller: when
// SkipInterceptorPluginID is set, that plugin's interceptors are skipped for the
// nested model execution while other plugins may still run.
func (h *BaseAPIHandler) ExecuteModelStream(ctx context.Context, req ModelExecutionRequest) (ModelExecutionStream, *interfaces.ErrorMessage) {
	if !req.Stream {
		return ModelExecutionStream{}, modelExecutionModeError("ExecuteModelStream requires Stream=true")
	}
	dataChan, headers, errChan := h.executeStreamWithAuthManagerFormats(ctx, req.EntryProtocol, req.ExitProtocol, req.Model, cloneBytes(req.Body), req.Alt, false, modelExecutionOptions{
		Headers:                 req.Headers,
		Query:                   req.Query,
		InternalSource:          true,
		SkipInterceptorPluginID: req.SkipInterceptorPluginID,
	})
	chunks, errMsg := prepareModelExecutionStream(ctx, dataChan, errChan)
	if errMsg != nil {
		return ModelExecutionStream{}, errMsg
	}
	return ModelExecutionStream{
		StatusCode: http.StatusOK,
		Headers:    cloneHeader(headers),
		Chunks:     chunks,
	}, nil
}

func modelExecutionModeError(message string) *interfaces.ErrorMessage {
	return &interfaces.ErrorMessage{StatusCode: http.StatusBadRequest, Error: errors.New(message)}
}

func modelExecutionResponseProtocol(entryProtocol, exitProtocol string) string {
	if exitProtocol == "" {
		return entryProtocol
	}
	return exitProtocol
}

func modelExecutionHeaders(ctx context.Context, headers http.Header) http.Header {
	if len(headers) > 0 {
		return cloneHeader(headers)
	}
	return headersFromContext(ctx)
}

func cloneURLValues(src url.Values) url.Values {
	if src == nil {
		return nil
	}
	dst := make(url.Values, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func addModelExecutionSourceMetadata(meta map[string]any, internalSource bool) {
	if !internalSource || meta == nil {
		return
	}
	meta[modelExecutionMetadataSourceKey] = modelExecutionInternalSource
}

func prepareModelExecutionStream(ctx context.Context, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage) (<-chan ModelExecutionChunk, *interfaces.ErrorMessage) {
	pending, nextDataChan, nextErrChan, errMsg := receiveInitialModelExecutionChunk(ctx, dataChan, errChan)
	if errMsg != nil {
		return nil, errMsg
	}
	return wrapModelExecutionChunks(ctx, nextDataChan, nextErrChan, pending), nil
}

func receiveInitialModelExecutionChunk(ctx context.Context, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage) ([]ModelExecutionChunk, <-chan []byte, <-chan *interfaces.ErrorMessage, *interfaces.ErrorMessage) {
	var done <-chan struct{}
	if ctx != nil {
		done = ctx.Done()
	}
	for dataChan != nil || errChan != nil {
		select {
		case payload, ok := <-dataChan:
			if !ok {
				dataChan = nil
				continue
			}
			return []ModelExecutionChunk{{Payload: cloneBytes(payload)}}, dataChan, errChan, nil
		case errMsg, ok := <-errChan:
			if !ok {
				errChan = nil
				continue
			}
			if errMsg != nil {
				return nil, dataChan, errChan, errMsg
			}
		case <-done:
			return nil, dataChan, errChan, nil
		}
	}
	return nil, dataChan, errChan, nil
}

func wrapModelExecutionChunks(ctx context.Context, dataChan <-chan []byte, errChan <-chan *interfaces.ErrorMessage, pending []ModelExecutionChunk) <-chan ModelExecutionChunk {
	chunks := make(chan ModelExecutionChunk)
	go func() {
		defer close(chunks)
		var done <-chan struct{}
		if ctx != nil {
			done = ctx.Done()
		}
		for _, chunk := range pending {
			if !sendModelExecutionChunk(ctx, chunks, chunk) {
				return
			}
		}
		for dataChan != nil || errChan != nil {
			select {
			case <-done:
				return
			case payload, ok := <-dataChan:
				if !ok {
					dataChan = nil
					continue
				}
				if !sendModelExecutionChunk(ctx, chunks, ModelExecutionChunk{Payload: cloneBytes(payload)}) {
					return
				}
			case errMsg, ok := <-errChan:
				if !ok {
					errChan = nil
					continue
				}
				if errMsg != nil {
					_ = sendModelExecutionChunk(ctx, chunks, ModelExecutionChunk{Err: modelExecutionStreamErrorFromMessage(errMsg)})
					return
				}
			}
		}
	}()
	return chunks
}

func modelExecutionStreamErrorFromMessage(errMsg *interfaces.ErrorMessage) *ModelExecutionStreamError {
	if errMsg == nil {
		return nil
	}
	message := ""
	if errMsg.Error != nil {
		message = errMsg.Error.Error()
	}
	return &ModelExecutionStreamError{
		StatusCode: errMsg.StatusCode,
		Message:    message,
		Headers:    cloneHeader(errMsg.Addon),
	}
}

func sendModelExecutionChunk(ctx context.Context, chunks chan<- ModelExecutionChunk, chunk ModelExecutionChunk) bool {
	if ctx == nil {
		chunks <- chunk
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case chunks <- chunk:
		return true
	}
}
