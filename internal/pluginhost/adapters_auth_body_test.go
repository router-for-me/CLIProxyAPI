package pluginhost

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginabi"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// authenticateWithBody runs a plugin access provider on a request and returns what the plugin saw.
// maxBody is the configured server.max-request-body-bytes (0 = unset).
func authenticateWithBody(ctx context.Context, t *testing.T, method, path string, body []byte, encoding string, maxBody int64) (pluginapi.FrontendAuthRequest, int, *http.Request, *sdkaccess.AuthError) {
	t.Helper()
	var seen pluginapi.FrontendAuthRequest
	calls := 0
	host := New()
	host.runtimeConfig = &config.Config{}
	host.runtimeConfig.MaxRequestBodyBytes = maxBody
	adapter := newAccessAdapterForTest(host, "auth-plugin", frontendAuthProviderFunc{
		identifier: "quota",
		authenticate: func(_ context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
			seen = req
			calls++
			return pluginapi.FrontendAuthResponse{Authenticated: true, Principal: "k"}, nil
		},
	})
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", encoding)
	req.Header.Set("Content-Length", strconv.Itoa(len(body)))
	_, authErr := adapter.Authenticate(ctx, req)
	return seen, calls, req, authErr
}

func zstdCompress(t *testing.T, raw []byte) []byte {
	t.Helper()
	encoder, errNewWriter := zstd.NewWriter(nil)
	if errNewWriter != nil {
		t.Fatal(errNewWriter)
	}
	defer func() {
		if errClose := encoder.Close(); errClose != nil {
			t.Errorf("encoder close error: %v", errClose)
		}
	}()
	return encoder.EncodeAll(raw, nil)
}

// pi and the Codex CLI send zstd bodies on POST /v1/responses; the plugin gets the decoded JSON in
// DecodedBody, Body and Headers stay as sent, and the handler still gets the raw bytes.
func TestAccessAdapterDecodesZstdBodyForPlugin(t *testing.T) {
	plain := []byte(`{"model":"gpt-5","input":"hi"}`)
	compressed := zstdCompress(t, plain)

	seen, _, req, authErr := authenticateWithBody(context.Background(), t, http.MethodPost, "/v1/responses", compressed, "zstd", 0)
	if authErr != nil {
		t.Fatalf("auth error: %v", authErr)
	}
	if !bytes.Equal(seen.DecodedBody, plain) {
		t.Fatalf("DecodedBody = %q, want %q", seen.DecodedBody, plain)
	}
	if !bytes.Equal(seen.Body, compressed) || seen.Headers.Get("Content-Encoding") != "zstd" || seen.Headers.Get("Content-Length") != strconv.Itoa(len(compressed)) {
		t.Fatalf("Body or Headers are not raw: headers %v", seen.Headers)
	}
	rest, errReadAll := io.ReadAll(req.Body)
	if errReadAll != nil || !bytes.Equal(rest, compressed) {
		t.Fatal("the handler no longer gets the raw compressed body")
	}
}

// Nothing is decoded where the handler does not decode: GET (websocket upgrade) on a decoding path,
// routes whose handler reads raw bytes, and plain JSON labeled zstd.
func TestAccessAdapterLeavesDecodedBodyNilWhereHandlerDoesNotDecode(t *testing.T) {
	compressed := zstdCompress(t, []byte(`{"model":"gpt-5"}`))
	plainJSON := []byte(`{"model":"gpt-5"}`)
	for _, tc := range []struct {
		name, method, path string
		body               []byte
	}{
		{"GET responses upgrade", http.MethodGet, "/v1/responses", compressed},
		{"GET codex responses upgrade", http.MethodGet, "/backend-api/codex/responses", compressed},
		{"claude handler reads raw", http.MethodPost, "/v1/messages", compressed},
		{"gemini handler reads raw", http.MethodPost, "/v1beta/models/m:generateContent", compressed},
		{"plain JSON labeled zstd", http.MethodPost, "/v1/responses", plainJSON},
	} {
		seen, calls, _, authErr := authenticateWithBody(context.Background(), t, tc.method, tc.path, tc.body, "zstd", 0)
		if authErr != nil || calls != 1 || seen.DecodedBody != nil || !bytes.Equal(seen.Body, tc.body) || seen.Headers.Get("Content-Encoding") != "zstd" {
			t.Fatalf("%s: body %q decoded %q header %q calls %d err %v", tc.name, seen.Body, seen.DecodedBody, seen.Headers.Get("Content-Encoding"), calls, authErr)
		}
	}
}

// Decoding runs before authentication: a declared window past 8 MiB, or a decoded body past
// min(configured limit, 64 MiB), is refused with 413 without asking the plugin.
func TestAccessAdapterRefusesOversizedDecodes(t *testing.T) {
	// `printf '{"model":"gpt-5"}' | zstd --long=24` style frame declaring a 16 MiB window.
	window16MiB, errDecodeHex := hex.DecodeString("28b52ffd0470b900007b226d6f64656c223a226770742d362e312d736f6c227d2a82afeb")
	if errDecodeHex != nil {
		t.Fatal(errDecodeHex)
	}
	for _, tc := range []struct {
		name, path string
		body       []byte
		maxBody    int64
		wantLen    int // 0: want 413
	}{
		{"window past 8 MiB", "/v1/responses", window16MiB, 0, 0},
		{"body past 64 MiB, no limit configured", "/v1/responses", zstdCompress(t, make([]byte, 64<<20+1)), 0, 0},
		{"body past 64 MiB, limit above 64 MiB", "/v1/responses", zstdCompress(t, make([]byte, 64<<20+1)), 128 << 20, 0},
		{"body past configured limit", "/v1/responses", zstdCompress(t, make([]byte, 1025)), 1024, 0},
		{"body at configured limit", "/v1/responses", zstdCompress(t, make([]byte, 1024)), 1024, 1024},
		{"speech past its 1 MiB cap", "/v1/audio/speech", zstdCompress(t, make([]byte, 1<<20+1)), 0, 0},
		{"tts at its 1 MiB cap", "/v1/tts", zstdCompress(t, make([]byte, 1<<20)), 0, 1 << 20},
	} {
		seen, calls, _, authErr := authenticateWithBody(context.Background(), t, http.MethodPost, tc.path, tc.body, "zstd", tc.maxBody)
		if tc.wantLen == 0 {
			if authErr == nil || authErr.HTTPStatusCode() != http.StatusRequestEntityTooLarge || calls != 0 {
				t.Fatalf("%s: err %v, plugin calls %d", tc.name, authErr, calls)
			}
		} else if authErr != nil || calls != 1 || len(seen.DecodedBody) != tc.wantLen {
			t.Fatalf("%s: err %v, plugin calls %d, decoded %d", tc.name, authErr, calls, len(seen.DecodedBody))
		}
	}
}

// With image generation disabled the image handlers answer 404 before reading the body, so the
// adapter does not decode it either.
func TestAccessAdapterSkipsDisabledImageRoutes(t *testing.T) {
	compressed := zstdCompress(t, []byte(`{"model":"gpt-image-1","prompt":"x"}`))
	host := New()
	host.runtimeConfig = &config.Config{}
	host.runtimeConfig.DisableImageGeneration = config.DisableImageGenerationAll
	var seen pluginapi.FrontendAuthRequest
	adapter := newAccessAdapterForTest(host, "auth-plugin", frontendAuthProviderFunc{
		identifier: "quota",
		authenticate: func(_ context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
			seen = req
			return pluginapi.FrontendAuthResponse{Authenticated: true, Principal: "k"}, nil
		},
	})
	for _, path := range []string{"/v1/images/generations", "/v1/images/edits"} {
		req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(compressed))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Content-Encoding", "zstd")
		if _, authErr := adapter.Authenticate(context.Background(), req); authErr != nil || seen.DecodedBody != nil {
			t.Fatalf("%s: decoded %q err %v", path, seen.DecodedBody, authErr)
		}
	}
}

// At most two content codings are decoded before authentication; a longer list costs no decoding
// and leaves DecodedBody nil.
func TestAccessAdapterDecodesAtMostTwoCodings(t *testing.T) {
	plain := []byte(`{"model":"gpt-5"}`)
	twice := zstdCompress(t, zstdCompress(t, plain))
	seen, calls, _, authErr := authenticateWithBody(context.Background(), t, http.MethodPost, "/v1/responses", twice, "zstd, zstd", 0)
	if authErr != nil || calls != 1 || !bytes.Equal(seen.DecodedBody, plain) {
		t.Fatalf("two codings: decoded %q calls %d err %v", seen.DecodedBody, calls, authErr)
	}
	thrice := zstdCompress(t, twice)
	seen, calls, _, authErr = authenticateWithBody(context.Background(), t, http.MethodPost, "/v1/responses", thrice, "zstd, zstd, zstd", 0)
	if authErr != nil || calls != 1 || seen.DecodedBody != nil || !bytes.Equal(seen.Body, thrice) {
		t.Fatalf("three codings: decoded %q calls %d err %v", seen.DecodedBody, calls, authErr)
	}
}

// DecodedBody crosses the RPC boundary as JSON; a plugin built against the old request type ignores it.
func TestFrontendAuthRequestDecodedBodyOverRPC(t *testing.T) {
	client := &capturePluginClient{}
	adapter := rpcFrontendAuthProvider{rpcPluginAdapter: &rpcPluginAdapter{client: client}}
	_, _ = adapter.Authenticate(context.Background(), pluginapi.FrontendAuthRequest{Body: []byte("raw"), DecodedBody: []byte("decoded")})
	raw := client.requests[pluginabi.MethodFrontendAuthAuthenticate]
	var got pluginapi.FrontendAuthRequest
	if errUnmarshal := json.Unmarshal(raw, &got); errUnmarshal != nil || string(got.Body) != "raw" || string(got.DecodedBody) != "decoded" {
		t.Fatalf("request %s decoded to %+v, err %v", raw, got, errUnmarshal)
	}
	var old struct {
		Method string
		Body   []byte
	}
	if errUnmarshal := json.Unmarshal(raw, &old); errUnmarshal != nil || string(old.Body) != "raw" {
		t.Fatalf("old plugin type failed on %s: %v", raw, errUnmarshal)
	}
}

type countingAuthBodyReader struct {
	reader    io.Reader
	readBytes int
}

func (r *countingAuthBodyReader) Read(p []byte) (int, error) {
	n, errRead := r.reader.Read(p)
	r.readBytes += n
	return n, errRead
}

type omitBodyFrontendAuthProvider struct {
	frontendAuthProviderFunc
}

func (omitBodyFrontendAuthProvider) FrontendAuthProviderWantsOmitBody() bool {
	return true
}

func TestAccessAdapterFrontendAuthOmitBodySkipsReadingAndDecodingBody(t *testing.T) {
	const (
		omitKey   = "plugin:auth-omit:quota"
		aliasKey  = "plugin:auth-alias:quota"
		bodyKey   = "plugin:auth-body:quota"
		legacyKey = "plugin:auth-legacy:quota"
	)
	for _, key := range []string{omitKey, aliasKey, bodyKey, legacyKey} {
		sdkaccess.UnregisterProvider(key)
		defer sdkaccess.UnregisterProvider(key)
	}

	var omitSeen, aliasSeen, bodySeen, legacySeen pluginapi.FrontendAuthRequest
	var omitCalls, aliasCalls, bodyCalls, legacyCalls int
	newProvider := func(got *pluginapi.FrontendAuthRequest, calls *int) frontendAuthProviderFunc {
		return frontendAuthProviderFunc{
			identifier: "quota",
			authenticate: func(_ context.Context, req pluginapi.FrontendAuthRequest) (pluginapi.FrontendAuthResponse, error) {
				*calls++
				*got = req
				return pluginapi.FrontendAuthResponse{Authenticated: true, Principal: "user-1"}, nil
			},
		}
	}

	host := newHostWithRecords(
		capabilityRecord{
			id: "auth-omit",
			plugin: pluginapi.Plugin{
				SchemaVersion: pluginabi.SchemaVersionFrontendAuthOmitBody,
				Capabilities: pluginapi.Capabilities{
					FrontendAuthProvider:         newProvider(&omitSeen, &omitCalls),
					FrontendAuthProviderOmitBody: true,
				},
			},
		},
		capabilityRecord{
			id: "auth-alias",
			plugin: pluginapi.Plugin{
				SchemaVersion: pluginabi.SchemaVersionFrontendAuthOmitBody,
				Capabilities: pluginapi.Capabilities{
					FrontendAuthProvider: newProvider(&aliasSeen, &aliasCalls),
					FrontendAuthOmitBody: true,
				},
			},
		},
		capabilityRecord{
			id: "auth-body",
			plugin: pluginapi.Plugin{
				SchemaVersion: pluginabi.SchemaVersionFrontendAuthOmitBody,
				Capabilities: pluginapi.Capabilities{
					FrontendAuthProvider: newProvider(&bodySeen, &bodyCalls),
				},
			},
		},
		capabilityRecord{
			id: "auth-legacy",
			plugin: pluginapi.Plugin{
				SchemaVersion: 6,
				Capabilities: pluginapi.Capabilities{
					FrontendAuthProvider:         newProvider(&legacySeen, &legacyCalls),
					FrontendAuthProviderOmitBody: true,
					FrontendAuthOmitBody:         true,
				},
			},
		},
	)
	host.RegisterFrontendAuthProviders()

	findProvider := func(t *testing.T, key string) sdkaccess.Provider {
		t.Helper()
		for _, candidate := range sdkaccess.RegisteredProviders() {
			if candidate.Identifier() == key {
				return candidate
			}
		}
		t.Fatalf("provider %q was not registered", key)
		return nil
	}

	plain := []byte(`{"model":"gpt-5","input":"large payload"}`)
	compressed := zstdCompress(t, plain)

	for _, tc := range []struct {
		name string
		key  string
		seen *pluginapi.FrontendAuthRequest
		call *int
	}{
		{name: "FrontendAuthProviderOmitBody", key: omitKey, seen: &omitSeen, call: &omitCalls},
		{name: "FrontendAuthOmitBody", key: aliasKey, seen: &aliasSeen, call: &aliasCalls},
	} {
		t.Run(tc.name, func(t *testing.T) {
			savedSlots := authDecodeSlots
			authDecodeSlots = make(chan struct{}, 1)
			authDecodeSlots <- struct{}{}
			defer func() { authDecodeSlots = savedSlots }()

			provider := findProvider(t, tc.key)
			counter := &countingAuthBodyReader{reader: bytes.NewReader(compressed)}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses?stream=true", io.NopCloser(counter))
			req.Header.Set("Authorization", "Bearer sk-test")
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "zstd")

			res, authErr := provider.Authenticate(context.Background(), req)
			if authErr != nil {
				t.Fatalf("Authenticate() error = %v, want nil", authErr)
			}
			if res == nil || res.Principal != "user-1" {
				t.Fatalf("Authenticate() result = %#v, want principal user-1", res)
			}
			if *tc.call != 1 {
				t.Fatalf("Authenticate() calls = %d, want 1", *tc.call)
			}
			if counter.readBytes != 0 {
				t.Fatalf("bytes read from r.Body during Authenticate = %d, want 0", counter.readBytes)
			}
			if tc.seen.Body != nil || tc.seen.DecodedBody != nil {
				t.Fatalf("seen Body=%q DecodedBody=%q, want both nil", tc.seen.Body, tc.seen.DecodedBody)
			}
			if tc.seen.Headers.Get("Authorization") != "Bearer sk-test" || tc.seen.Query.Get("stream") != "true" {
				t.Fatalf("seen headers/query = %v / %v, want preserved metadata", tc.seen.Headers, tc.seen.Query)
			}
			rest, errReadAll := io.ReadAll(req.Body)
			if errReadAll != nil || !bytes.Equal(rest, compressed) {
				t.Fatalf("downstream body = %q (err=%v), want untouched compressed payload", rest, errReadAll)
			}
			if counter.readBytes != len(compressed) {
				t.Fatalf("bytes read after downstream ReadAll = %d, want %d", counter.readBytes, len(compressed))
			}

			// A failing body reader must not cause Authenticate to fail when omitBody is active.
			failReq, errNewReq := http.NewRequest(http.MethodPost, "http://example.test/v1/responses", failingReadCloser{})
			if errNewReq != nil {
				t.Fatalf("NewRequest() error = %v", errNewReq)
			}
			if _, authErrFail := provider.Authenticate(context.Background(), failReq); authErrFail != nil {
				t.Fatalf("Authenticate() with failing body reader error = %v, want nil", authErrFail)
			}
		})
	}

	for _, tc := range []struct {
		name string
		key  string
		seen *pluginapi.FrontendAuthRequest
		call *int
	}{
		{name: "schema 7 without opt-out", key: bodyKey, seen: &bodySeen, call: &bodyCalls},
		{name: "schema 6 legacy ignores opt-out", key: legacyKey, seen: &legacySeen, call: &legacyCalls},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := findProvider(t, tc.key)
			counter := &countingAuthBodyReader{reader: bytes.NewReader(compressed)}
			req := httptest.NewRequest(http.MethodPost, "/v1/responses", io.NopCloser(counter))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Content-Encoding", "zstd")

			if _, authErr := provider.Authenticate(context.Background(), req); authErr != nil {
				t.Fatalf("Authenticate() error = %v, want nil", authErr)
			}
			if *tc.call != 1 {
				t.Fatalf("Authenticate() calls = %d, want 1", *tc.call)
			}
			if counter.readBytes != len(compressed) {
				t.Fatalf("bytes read = %d, want %d", counter.readBytes, len(compressed))
			}
			if !bytes.Equal(tc.seen.Body, compressed) || !bytes.Equal(tc.seen.DecodedBody, plain) {
				t.Fatalf("seen Body=%q DecodedBody=%q, want preserved", tc.seen.Body, tc.seen.DecodedBody)
			}
		})
	}
}

func TestFrontendAuthRequestBodySkipsCloneForRPCProvider(t *testing.T) {
	body := []byte("original-request-body")

	rpcProvider := rpcFrontendAuthProvider{rpcPluginAdapter: &rpcPluginAdapter{client: &capturePluginClient{}}}
	gotRPC := frontendAuthRequestBody(rpcProvider, body)
	if len(gotRPC) != len(body) || &gotRPC[0] != &body[0] {
		t.Fatal("frontendAuthRequestBody cloned body for rpcFrontendAuthProvider, want shared backing slice")
	}

	inProcProvider := frontendAuthProviderFunc{identifier: "in-proc"}
	gotInProc := frontendAuthRequestBody(inProcProvider, body)
	if !bytes.Equal(gotInProc, body) {
		t.Fatalf("frontendAuthRequestBody(inProc) = %q, want %q", gotInProc, body)
	}
	if &gotInProc[0] == &body[0] {
		t.Fatal("frontendAuthRequestBody did not clone body for in-process provider")
	}
	gotInProc[0] = 'X'
	if body[0] != 'o' {
		t.Fatalf("mutating in-process request body mutated original buffer: %q", body)
	}

	if gotNil := frontendAuthRequestBody(rpcProvider, nil); gotNil != nil {
		t.Fatalf("frontendAuthRequestBody(nil) = %v, want nil", gotNil)
	}
}
