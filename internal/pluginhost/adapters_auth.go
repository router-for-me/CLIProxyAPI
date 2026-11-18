package pluginhost

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Decoding runs before authentication, so what it allocates, produces and runs at once is bounded.
// 8 MiB of window covers every zstd level up to 19; a larger declared window is allocated up front.
const (
	maxAuthZstdWindowBytes  = 8 << 20
	maxAuthDecodedBodyBytes = 64 << 20
	// maxAuthSpeechBodyBytes is the speech handlers' own decoded cap (maxXAISpeechBody).
	maxAuthSpeechBodyBytes = 1 << 20
	// maxAuthContentCodings bounds the decoder layers run before authentication; a longer
	// Content-Encoding list is left to the handler and the plugin gets no DecodedBody.
	maxAuthContentCodings = 2
)

var (
	authDecodeSlots     = make(chan struct{}, 2)
	errAuthBodyTooLarge = errors.New("request body too large")
)

// decodeAuthBody decodes a Content-Encoding list the way handlers.ReadRequestBody does (zstd,
// last to first), but with the window bound the handler's decoder lacks and a decoded size of at
// most limit bytes.
func decodeAuthBody(body []byte, encoding string, limit int64) ([]byte, error) {
	parts := strings.Split(encoding, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		switch enc := strings.ToLower(strings.TrimSpace(parts[i])); enc {
		case "", "identity":
		case "zstd":
			decoder, errNewReader := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxAuthZstdWindowBytes))
			if errNewReader != nil {
				return nil, errNewReader
			}
			out, errReadAll := io.ReadAll(io.LimitReader(decoder, limit+1))
			decoder.Close()
			if errors.Is(errReadAll, zstd.ErrWindowSizeExceeded) || errors.Is(errReadAll, zstd.ErrDecoderSizeExceeded) || int64(len(out)) > limit {
				return nil, errAuthBodyTooLarge
			}
			if errReadAll != nil {
				return nil, errReadAll
			}
			body = out
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

// handlerDecodesBody reports whether the route's handler reads the body through
// handlers.ReadRequestBody. Other handlers read the raw bytes, and a plugin judging a
// decoded body there could approve a model the handler never runs. Only POST is decoded:
// GET on the responses routes is a websocket upgrade whose body the handler never reads.
// NOTE: keep in step with the ReadRequestBody callers.
func handlerDecodesBody(r *http.Request) bool {
	if r.Method != http.MethodPost {
		return false
	}
	switch r.URL.Path {
	case "/v1/chat/completions", "/v1/completions", "/v1/responses", "/v1/responses/compact",
		"/backend-api/codex/responses", "/backend-api/codex/responses/compact",
		"/v1/images/generations", "/v1/audio/speech", "/v1/tts",
		"/v1/videos", "/v1/videos/generations", "/v1/videos/edits", "/v1/videos/extensions":
		return true
	case "/v1/images/edits": // only the JSON branch; multipart is parsed raw
		return strings.HasPrefix(strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Type"))), "application/json")
	case "/openai/v1/videos": // forms are read raw
		contentType := r.Header.Get("Content-Type")
		if i := strings.IndexAny(contentType, " ;"); i >= 0 {
			contentType = contentType[:i]
		}
		contentType = strings.ToLower(strings.TrimSpace(contentType))
		return contentType != "multipart/form-data" && contentType != "application/x-www-form-urlencoded"
	}
	return false
}

func (h *Host) RegisterFrontendAuthProviders() {
	if h == nil {
		return
	}

	type exclusiveFrontendAuthCandidate struct {
		key      string
		pluginID string
		priority int
	}

	nextKeys := make(map[string]struct{})
	var bestExclusive exclusiveFrontendAuthCandidate
	for _, record := range h.activeRecords() {
		provider := record.plugin.Capabilities.FrontendAuthProvider
		if provider == nil || h.isPluginFused(record.id) {
			continue
		}
		adapter := &accessAdapter{
			host:     h,
			pluginID: record.id,
			path:     record.path,
			version:  record.version,
			provider: provider,
			omitBody: frontendAuthOmitsBody(record),
		}
		key := strings.TrimSpace(adapter.Identifier())
		if key == "" {
			continue
		}
		sdkaccess.RegisterProvider(key, adapter)
		nextKeys[key] = struct{}{}
		if record.plugin.Capabilities.FrontendAuthProviderExclusive {
			candidate := exclusiveFrontendAuthCandidate{
				key:      key,
				pluginID: record.id,
				priority: record.priority,
			}
			if bestExclusive.key == "" ||
				candidate.priority > bestExclusive.priority ||
				(candidate.priority == bestExclusive.priority && candidate.pluginID < bestExclusive.pluginID) {
				bestExclusive = candidate
			}
		}
	}

	if bestExclusive.key != "" {
		sdkaccess.SetExclusiveProvider(bestExclusive.key)
	} else {
		sdkaccess.ClearExclusiveProvider()
	}
	h.pruneStaleAccessProviders(nextKeys)
}

func (h *Host) pruneStaleAccessProviders(nextKeys map[string]struct{}) {
	if h == nil {
		return
	}

	staleKeys := make([]string, 0)
	h.mu.Lock()
	for key := range h.accessProviderKeys {
		if _, okKey := nextKeys[key]; !okKey {
			staleKeys = append(staleKeys, key)
		}
	}
	h.accessProviderKeys = nextKeys
	h.mu.Unlock()

	for _, key := range staleKeys {
		sdkaccess.UnregisterProvider(key)
	}
}

type accessAdapter struct {
	host     *Host
	pluginID string
	path     string
	version  string
	provider pluginapi.FrontendAuthProvider
	omitBody bool
}

func frontendAuthOmitsBody(record capabilityRecord) bool {
	if record.plugin.SchemaVersion < pluginabi.SchemaVersionFrontendAuthOmitBody {
		return false
	}
	caps := record.plugin.Capabilities
	if caps.FrontendAuthProvider == nil {
		return false
	}
	return frontendAuthProviderWantsOmitBody(caps)
}

func frontendAuthRequestBody(provider pluginapi.FrontendAuthProvider, body []byte) []byte {
	if len(body) == 0 {
		return nil
	}
	if _, isRPC := provider.(rpcFrontendAuthProvider); isRPC {
		return body
	}
	return bytes.Clone(body)
}

func (a *accessAdapter) Identifier() (identifier string) {
	if a == nil || a.provider == nil {
		return ""
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if a.host != nil {
				a.host.fusePlugin(a.pluginID, "FrontendAuthProvider.Identifier", recovered)
			}
			identifier = ""
		}
	}()
	pluginID := strings.TrimSpace(a.pluginID)
	providerID := strings.TrimSpace(a.provider.Identifier())
	if pluginID == "" || providerID == "" {
		return ""
	}
	return "plugin:" + pluginID + ":" + providerID
}

func (a *accessAdapter) Authenticate(ctx context.Context, r *http.Request) (result *sdkaccess.Result, authErr *sdkaccess.AuthError) {
	if a == nil || a.provider == nil || a.host.isPluginFused(a.pluginID) || !a.host.pluginIdentityCurrent(a.pluginID, a.path, a.version) {
		return nil, sdkaccess.NewNotHandledError()
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			a.host.fusePlugin(a.pluginID, "FrontendAuthProvider.Authenticate", recovered)
			result = nil
			authErr = sdkaccess.NewNotHandledError()
		}
	}()

	headers := cloneHeader(r.Header)
	var authBody []byte
	var decodedBody []byte
	if !a.omitBody {
		body, errReadAll := readAndRestoreRequestBody(r)
		if errReadAll != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(errReadAll, &maxBytesErr) {
				return nil, sdkaccess.NewRequestEntityTooLargeError(errReadAll)
			}
			return nil, sdkaccess.NewInternalAuthError("failed to read plugin auth request body", errReadAll)
		}
		cfg := a.host.currentRuntimeConfig()
		// With image generation disabled the image handlers answer 404 before reading the body.
		imagesOff := cfg != nil && cfg.DisableImageGeneration == config.DisableImageGenerationAll && strings.HasPrefix(r.URL.Path, "/v1/images/")
		if encoding := strings.TrimSpace(headers.Get("Content-Encoding")); encoding != "" && !strings.EqualFold(encoding, "identity") && strings.Count(encoding, ",") < maxAuthContentCodings && !imagesOff && handlerDecodesBody(r) {
			// Decoding runs before authentication, so it is bounded; a slot is held until the plugin call finishes.
			var releaseSlot func()
			select {
			case authDecodeSlots <- struct{}{}:
				ctx, releaseSlot = withPluginCallFinish(ctx, func() { <-authDecodeSlots })
				defer releaseSlot()
			case <-ctx.Done():
				return nil, sdkaccess.NewInternalAuthError("plugin auth request canceled", ctx.Err())
			}
			// The server middleware limits only compressed bytes; ReadRequestBody applies the configured
			// limit to decoded bytes after authentication, so apply it here too.
			limit := int64(maxAuthDecodedBodyBytes)
			if cfg != nil && cfg.MaxRequestBodyBytes > 0 && cfg.MaxRequestBodyBytes < limit {
				limit = cfg.MaxRequestBodyBytes
			}
			if (r.URL.Path == "/v1/audio/speech" || r.URL.Path == "/v1/tts") && limit > maxAuthSpeechBodyBytes {
				limit = maxAuthSpeechBodyBytes
			}
			decoded, errDecode := decodeAuthBody(body, encoding, limit)
			if errors.Is(errDecode, errAuthBodyTooLarge) {
				return nil, sdkaccess.NewRequestEntityTooLargeError(errDecode)
			}
			// Any other failure leaves DecodedBody unset; the handler falls back to the raw body or rejects it.
			if errDecode == nil {
				decodedBody = decoded
			} else {
				releaseSlot()
			}
		}
		authBody = frontendAuthRequestBody(a.provider, body)
	}
	resp, errAuthenticate := a.provider.Authenticate(ctx, pluginapi.FrontendAuthRequest{
		Method:      r.Method,
		Path:        r.URL.Path,
		Headers:     headers,
		Query:       cloneValues(r.URL.Query()),
		Body:        authBody,
		DecodedBody: decodedBody,
	})
	if errAuthenticate != nil {
		return nil, sdkaccess.NewNotHandledError()
	}
	if !resp.Authenticated {
		if resp.DenyStatusCode > 0 || strings.TrimSpace(resp.DenyMessage) != "" {
			return nil, sdkaccess.NewDeniedError(resp.DenyMessage, resp.DenyStatusCode)
		}
		return nil, sdkaccess.NewNotHandledError()
	}
	providerID := a.Identifier()
	if providerID == "" {
		return nil, sdkaccess.NewNotHandledError()
	}
	return &sdkaccess.Result{
		Provider:  providerID,
		Principal: resp.Principal,
		Metadata:  cloneStringMap(resp.Metadata),
	}, nil
}
