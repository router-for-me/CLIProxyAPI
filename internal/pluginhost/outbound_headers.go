package pluginhost

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"golang.org/x/net/http/httpguts"
)

var protectedOutboundHeaders = map[string]struct{}{
	"authorization":       {},
	"connection":          {},
	"content-length":      {},
	"cookie":              {},
	"host":                {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"proxy-connection":    {},
	"set-cookie":          {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

// FinalizeOutboundHeaders applies active outbound-header plugins in configured priority order.
// Any plugin failure aborts the caller before network I/O.
func (h *Host) FinalizeOutboundHeaders(ctx context.Context, req pluginapi.OutboundHeaderInterceptRequest) (http.Header, error) {
	current := cloneHeader(req.Headers)
	if h == nil {
		return current, nil
	}
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" {
		return current, fmt.Errorf("outbound header provider is required")
	}
	transport := pluginapi.OutboundTransport(strings.ToLower(strings.TrimSpace(string(req.Transport))))
	if transport != pluginapi.OutboundTransportHTTP && transport != pluginapi.OutboundTransportWebSocket {
		return current, fmt.Errorf("unsupported outbound transport %q", req.Transport)
	}
	skipPluginID := hostCallbackPluginIDFromContext(ctx)
	for _, record := range h.activeRecords() {
		interceptor := record.plugin.Capabilities.OutboundHeaderInterceptor
		if interceptor == nil || record.id == skipPluginID {
			continue
		}
		if h.isPluginFused(record.id) {
			return nil, fmt.Errorf("outbound header interceptor %s is unavailable", record.id)
		}
		resp, errIntercept := h.callOutboundHeaderInterceptor(ctx, record, interceptor, pluginapi.OutboundHeaderInterceptRequest{
			Provider:  provider,
			Transport: transport,
			Headers:   mutableOutboundHeaders(current),
		})
		if errIntercept != nil {
			return nil, fmt.Errorf("outbound header interceptor %s failed: %w", record.id, errIntercept)
		}
		if errValidate := validateOutboundHeaderResponse(resp); errValidate != nil {
			return nil, fmt.Errorf("outbound header interceptor %s returned invalid headers: %w", record.id, errValidate)
		}
		current = mergeHeaders(current, resp.Headers, resp.ClearHeaders)
	}
	return current, nil
}

func (h *Host) callOutboundHeaderInterceptor(ctx context.Context, record capabilityRecord, interceptor pluginapi.OutboundHeaderInterceptor, req pluginapi.OutboundHeaderInterceptRequest) (resp pluginapi.OutboundHeaderInterceptResponse, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			h.fusePlugin(record.id, "OutboundHeaderInterceptor.InterceptOutboundHeaders", recovered)
			resp = pluginapi.OutboundHeaderInterceptResponse{}
			err = fmt.Errorf("plugin panic: %v", recovered)
		}
	}()
	return interceptor.InterceptOutboundHeaders(ctx, req)
}

func mutableOutboundHeaders(headers http.Header) http.Header {
	if len(headers) == 0 {
		return nil
	}
	out := make(http.Header)
	for key, values := range headers {
		if protectedOutboundHeader(key) {
			continue
		}
		out[key] = append([]string(nil), values...)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func validateOutboundHeaderResponse(resp pluginapi.OutboundHeaderInterceptResponse) error {
	for key, values := range resp.Headers {
		if !httpguts.ValidHeaderFieldName(key) {
			return fmt.Errorf("invalid header name %q", key)
		}
		if protectedOutboundHeader(key) {
			return fmt.Errorf("header %q is protected", key)
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("header %q has an invalid value", key)
			}
		}
	}
	for _, key := range resp.ClearHeaders {
		if !httpguts.ValidHeaderFieldName(key) {
			return fmt.Errorf("invalid clear header name %q", key)
		}
		if protectedOutboundHeader(key) {
			return fmt.Errorf("header %q is protected", key)
		}
	}
	return nil
}

func protectedOutboundHeader(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	if _, ok := protectedOutboundHeaders[lower]; ok {
		return true
	}
	if strings.HasPrefix(lower, "sec-websocket-") {
		return true
	}
	for _, marker := range []string{"api-key", "apikey", "token", "secret", "credential", "signature", "account-id", "account_id"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}
