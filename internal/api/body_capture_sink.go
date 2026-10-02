package api

import (
	"context"
	"encoding/json"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/api/middleware"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	log "github.com/sirupsen/logrus"
)

// usageBodyCaptureSink persists captured request/response pairs into the
// request_bodies table. Errors are logged and swallowed: capture must never
// affect the client response.
type usageBodyCaptureSink struct {
	usage *store.UsageStore
}

// newUsageBodyCaptureSink returns the BodyCaptureSink interface so a nil usage
// store yields an untyped nil (not a typed-nil pointer), preserving the
// middleware's sink == nil fast path.
func newUsageBodyCaptureSink(usage *store.UsageStore) middleware.BodyCaptureSink {
	if usage == nil {
		return nil
	}
	return &usageBodyCaptureSink{usage: usage}
}

func marshalHeaders(h map[string][]string) string {
	if len(h) == 0 {
		return ""
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return ""
	}
	return string(raw)
}

func (s *usageBodyCaptureSink) Capture(ctx context.Context, req middleware.BodyCaptureRequest) {
	if s == nil || s.usage == nil || req.RequestID == "" {
		return
	}
	err := s.usage.InsertRequestBody(ctx, store.RequestBody{
		RequestID:             req.RequestID,
		Provider:              req.Provider,
		UpstreamProviderID:    req.UpstreamProviderID,
		ClientRequestHeaders:  marshalHeaders(req.ClientRequestHeaders),
		ClientRequestBody:     string(req.ClientRequestBody),
		ClientResponseHeaders: marshalHeaders(req.ClientResponseHeaders),
		ClientResponseBody:    string(req.ClientResponseBody),
		UpstreamRequest:       string(req.UpstreamRequest),
		UpstreamResponse:      string(req.UpstreamResponse),
		Truncated:             req.Truncated,
	})
	if err != nil {
		log.WithError(err).Warn("body capture: persist request/response failed")
	}
}
