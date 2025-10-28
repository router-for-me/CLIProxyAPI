package wsrelay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
)

// HTTPRequest represents a proxied HTTP request delivered to websocket clients.
type HTTPRequest struct {
	Method  string
	URL     string
	Headers http.Header
	Body    []byte
}

// HTTPResponse captures the response relayed back from websocket clients.
type HTTPResponse struct {
	Status  int
	Headers http.Header
	Body    []byte
}

// StreamEvent represents a streaming response event from clients.
type StreamEvent struct {
	Type    string
	Payload []byte
	Status  int
	Headers http.Header
	Err     error
}

// NonStream executes a non-streaming HTTP request using the websocket provider.
func (m *Manager) NonStream(ctx context.Context, provider string, req *HTTPRequest) (*HTTPResponse, error) {
	if req == nil {
		return nil, fmt.Errorf("wsrelay: request is nil")
	}
	msg := Message{ID: uuid.NewString(), Type: MessageTypeHTTPReq, Payload: encodeRequest(req)}
	respCh, err := m.Send(ctx, provider, msg)
	if err != nil {
		return nil, err
	}
	var (
		streamMode bool
		streamResp *HTTPResponse
		streamBody bytes.Buffer
	)
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case msg, ok := <-respCh:
			if !ok {
				if streamMode {
					if streamResp == nil {
						streamResp = &HTTPResponse{Status: http.StatusOK, Headers: make(http.Header)}
					} else if streamResp.Headers == nil {
						streamResp.Headers = make(http.Header)
					}
					streamResp.Body = append(streamResp.Body[:0], streamBody.Bytes()...)
					return streamResp, nil
				}
				return nil, errors.New("wsrelay: connection closed during response")
			}
			switch msg.Type {
			case MessageTypeHTTPResp:
				resp := decodeResponse(msg.Payload)
				if streamMode && streamBody.Len() > 0 && len(resp.Body) == 0 {
					resp.Body = append(resp.Body[:0], streamBody.Bytes()...)
				}
				return resp, nil
			case MessageTypeError:
				return nil, decodeError(msg.Payload)
			case MessageTypeStreamStart, MessageTypeStreamChunk:
				if msg.Type == MessageTypeStreamStart {
					streamMode = true
					streamResp = decodeResponse(msg.Payload)
					if streamResp.Headers == nil {
						streamResp.Headers = make(http.Header)
					}
					streamBody.Reset()
					continue
				}
				if !streamMode {
					streamMode = true
					streamResp = &HTTPResponse{Status: http.StatusOK, Headers: make(http.Header)}
				}
				chunk := decodeChunk(msg.Payload)
				if len(chunk) > 0 {
					streamBody.Write(chunk)
				}
			case MessageTypeStreamEnd:
				if !streamMode {
					return &HTTPResponse{Status: http.StatusOK, Headers: make(http.Header)}, nil
				}
				if streamResp == nil {
					streamResp = &HTTPResponse{Status: http.StatusOK, Headers: make(http.Header)}
				} else if streamResp.Headers == nil {
					streamResp.Headers = make(http.Header)
				}
				streamResp.Body = append(streamResp.Body[:0], streamBody.Bytes()...)
				return streamResp, nil
			default:
			}
		}
	}
}

// Stream executes a streaming HTTP request and returns channel with stream events.
func (m *Manager) Stream(ctx context.Context, provider string, req *HTTPRequest) (<-chan StreamEvent, error) {
	if req == nil {
		return nil, fmt.Errorf("wsrelay: request is nil")
	}
	msg := Message{ID: uuid.NewString(), Type: MessageTypeHTTPReq, Payload: encodeRequest(req)}
	respCh, err := m.Send(ctx, provider, msg)
	if err != nil {
		return nil, err
	}
	out := make(chan StreamEvent)
	go func() {
		defer close(out)
		for {
			select {
			case <-ctx.Done():
				out <- StreamEvent{Err: ctx.Err()}
				return
			case msg, ok := <-respCh:
				if !ok {
					out <- StreamEvent{Err: errors.New("wsrelay: stream closed")}
					return
				}
				switch msg.Type {
				case MessageTypeStreamStart:
					resp := decodeResponse(msg.Payload)
					out <- StreamEvent{Type: MessageTypeStreamStart, Status: resp.Status, Headers: resp.Headers}
				case MessageTypeStreamChunk:
					chunk := decodeChunk(msg.Payload)
					out <- StreamEvent{Type: MessageTypeStreamChunk, Payload: chunk}
				case MessageTypeStreamEnd:
					out <- StreamEvent{Type: MessageTypeStreamEnd}
					return
				case MessageTypeError:
					out <- StreamEvent{Type: MessageTypeError, Err: decodeError(msg.Payload)}
					return
				case MessageTypeHTTPResp:
					resp := decodeResponse(msg.Payload)
					out <- StreamEvent{Type: MessageTypeHTTPResp, Status: resp.Status, Headers: resp.Headers, Payload: resp.Body}
					return
				default:
				}
			}
		}
	}()
	return out, nil
}

func encodeRequest(req *HTTPRequest) map[string]any {
	headers := make(map[string]any, len(req.Headers))
	for key, values := range req.Headers {
		copyValues := make([]string, len(values))
		copy(copyValues, values)
		headers[key] = copyValues
	}
	return map[string]any{
		"method":  req.Method,
		"url":     req.URL,
		"headers": headers,
		"body":    string(req.Body),
		"sent_at": time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func decodeResponse(payload map[string]any) *HTTPResponse {
	if payload == nil {
		return &HTTPResponse{Status: http.StatusBadGateway, Headers: make(http.Header)}
	}
	resp := &HTTPResponse{Status: http.StatusOK, Headers: make(http.Header)}
	if status, ok := payload["status"].(float64); ok {
		resp.Status = int(status)
	}
	if headers, ok := payload["headers"].(map[string]any); ok {
		for key, raw := range headers {
			switch v := raw.(type) {
			case []any:
				for _, item := range v {
					if str, ok := item.(string); ok {
						resp.Headers.Add(key, str)
					}
				}
			case []string:
				for _, str := range v {
					resp.Headers.Add(key, str)
				}
			case string:
				resp.Headers.Set(key, v)
			}
		}
	}
	if body, ok := payload["body"].(string); ok {
		resp.Body = []byte(body)
	}
	return resp
}

func decodeChunk(payload map[string]any) []byte {
	if payload == nil {
		return nil
	}
	if data, ok := payload["data"].(string); ok {
		return []byte(data)
	}
	return nil
}

func decodeError(payload map[string]any) error {
	if payload == nil {
		return errors.New("wsrelay: unknown error")
	}
	message, _ := payload["error"].(string)
	status := 0
	if v, ok := payload["status"].(float64); ok {
		status = int(v)
	}
	if message == "" {
		message = "wsrelay: upstream error"
	}
	return fmt.Errorf("%s (status=%d)", message, status)
}
