package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func codexCompressionConfig() *config.Config {
	return &config.Config{Codex: config.CodexConfig{UpstreamRequestCompression: true}}
}

func codexCompressionAuth() *cliproxyauth.Auth {
	return &cliproxyauth.Auth{Metadata: map[string]any{"access_token": "test-oauth"}}
}

func codexCompressionPayload() []byte {
	return []byte(`{"model":"gpt-5.4","input":[{"role":"user","content":"` + strings.Repeat("compressible input ", 400) + `"}]}`)
}

func readCodexCompressionBody(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	defer func() {
		if errClose := body.Close(); errClose != nil {
			log.Errorf("compression test: close body error: %v", errClose)
		}
	}()
	data, errRead := io.ReadAll(body)
	if errRead != nil {
		t.Fatalf("read body: %v", errRead)
	}
	return data
}

func TestCodexUpstreamCompression_EligibleCompressed(t *testing.T) {
	for _, target := range []string{"https://chatgpt.com/backend-api/codex/responses", "https://CHATGPT.COM/backend-api/codex/responses/compact"} {
		t.Run(target, func(t *testing.T) {
			executor := NewCodexExecutor(codexCompressionConfig())
			payload := codexCompressionPayload()
			req, upstreamBody, errCache := executor.cacheHelper(t.Context(), sdktranslator.FormatOpenAIResponse, target, cliproxyexecutor.Request{Payload: payload}, payload, codexCompressionAuth())
			if errCache != nil {
				t.Fatal(errCache)
			}
			wire := readCodexCompressionBody(t, req.Body)
			if bytes.Equal(wire, upstreamBody) || req.Header.Get("Content-Encoding") != "zstd" {
				t.Fatalf("expected zstd wire body, got Content-Encoding=%q, wire equals plaintext=%v", req.Header.Get("Content-Encoding"), bytes.Equal(wire, upstreamBody))
			}
			if req.ContentLength != int64(len(wire)) || len(wire) >= len(upstreamBody) {
				t.Fatalf("invalid compressed length: ContentLength=%d, wire=%d, plaintext=%d", req.ContentLength, len(wire), len(upstreamBody))
			}
			decoder, errDecoder := zstd.NewReader(nil)
			if errDecoder != nil {
				t.Fatal(errDecoder)
			}
			defer decoder.Close()
			decoded, errDecode := decoder.DecodeAll(wire, nil)
			if errDecode != nil || !bytes.Equal(decoded, upstreamBody) {
				t.Fatalf("zstd round trip mismatch: %v", errDecode)
			}
			if got := req.Header.Get("Accept-Encoding"); got != "gzip, deflate, br, zstd" {
				t.Fatalf("Accept-Encoding=%q", got)
			}
			if !bytes.Equal(upstreamBody, payload) {
				t.Fatal("returned upstreamBody must stay uncompressed")
			}
		})
	}
}

func TestCodexUpstreamCompression_Gates(t *testing.T) {
	for _, name := range []string{"disabled", "nil-config", "api-key", "siwc", "custom-url", "wrong-path", "below-min", "nil-auth", "no-size-benefit"} {
		t.Run(name, func(t *testing.T) {
			cfg, auth := codexCompressionConfig(), codexCompressionAuth()
			target, payload := "https://chatgpt.com/backend-api/codex/responses", codexCompressionPayload()
			switch name {
			case "disabled":
				cfg.Codex.UpstreamRequestCompression = false
			case "nil-config":
				cfg = nil
			case "api-key":
				auth.Attributes = map[string]string{"api_key": "test"}
			case "siwc":
				auth.Metadata["use_siwc"] = true
			case "custom-url":
				target = "https://example.com/responses"
			case "wrong-path":
				target = "https://chatgpt.com/backend-api/codex-other/responses"
			case "below-min":
				cfg.Codex.UpstreamRequestCompressionMinBytes = len(payload) + 1
			case "nil-auth":
				auth = nil
			case "no-size-benefit":
				cfg.Codex.UpstreamRequestCompressionMinBytes = 1
				payload = []byte(`{}`)
			}
			executor := NewCodexExecutor(cfg)
			req, plain, errCache := executor.cacheHelper(t.Context(), sdktranslator.FormatOpenAIResponse, target, cliproxyexecutor.Request{Payload: payload}, payload, auth)
			if errCache != nil {
				t.Fatal(errCache)
			}
			if wire := readCodexCompressionBody(t, req.Body); !bytes.Equal(wire, plain) || req.Header.Get("Content-Encoding") != "" {
				t.Fatalf("gate must keep plaintext, Content-Encoding=%q", req.Header.Get("Content-Encoding"))
			}
		})
	}
}

func TestCodexUpstreamCompression_ContentEncodingGuard(t *testing.T) {
	executor, auth := NewCodexExecutor(codexCompressionConfig()), codexCompressionAuth()
	auth.Attributes = map[string]string{"header:Content-Encoding": "identity"}
	payload := codexCompressionPayload()
	req, _, errCache := executor.cacheHelper(t.Context(), sdktranslator.FormatOpenAIResponse, "https://chatgpt.com/backend-api/codex/responses", cliproxyexecutor.Request{}, payload, auth)
	if errCache != nil {
		t.Fatal(errCache)
	}
	applyCodexHeaders(req, auth, "test", true, executor.cfg)
	if got := req.Header.Get("Content-Encoding"); got != "identity" {
		t.Fatalf("custom header was not applied: %q", got)
	}
	if errPrepare := executor.PrepareRequest(req, auth); errPrepare != nil {
		t.Fatal(errPrepare)
	}
	if got := req.Header.Get("Content-Encoding"); got != "zstd" {
		t.Fatalf("Content-Encoding=%q, want zstd for actual compressed bytes", got)
	}
	if wire := readCodexCompressionBody(t, req.Body); !bytes.HasPrefix(wire, []byte{0x28, 0xb5, 0x2f, 0xfd}) {
		t.Fatal("guard changed the wire body")
	}
}

func codexCompressedResponse(t *testing.T, encoding string, plain []byte) []byte {
	t.Helper()
	var output bytes.Buffer
	var writer io.WriteCloser
	switch encoding {
	case "gzip":
		writer = gzip.NewWriter(&output)
	case "br":
		writer = brotli.NewWriter(&output)
	case "zstd":
		var errWriter error
		writer, errWriter = zstd.NewWriter(&output)
		if errWriter != nil {
			t.Fatal(errWriter)
		}
	}
	if _, errWrite := writer.Write(plain); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatal(errClose)
	}
	return output.Bytes()
}

func TestCodexExecutor_CompressedUpstreamResponseDecoded(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "compact"} {
		for _, encoding := range []string{"gzip", "zstd", "br"} {
			t.Run(mode+"/"+encoding, func(t *testing.T) {
				plain := []byte("data: " + codexCompletedEventBody + "\n\n")
				if mode == "compact" {
					plain = []byte(`{"id":"resp_1","object":"response","status":"completed","output":[]}`)
				}
				wire := codexCompressedResponse(t, encoding, plain)
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if got := r.Header.Get("Accept-Encoding"); got != "gzip, deflate, br, zstd" {
						t.Errorf("Accept-Encoding=%q", got)
					}
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set("Content-Encoding", encoding)
					w.Header().Set("Content-Length", strconv.Itoa(len(wire)))
					if _, errWrite := w.Write(wire); errWrite != nil {
						t.Errorf("write compressed response: %v", errWrite)
					}
				}))
				defer server.Close()
				executor := NewCodexExecutor(&config.Config{})
				auth := codexTestAuth(server.URL)
				// Force negotiation even before cacheHelper supports it, keeping RED deterministic.
				auth.Attributes["header:Accept-Encoding"] = "gzip, deflate, br, zstd"
				req, opts := codexTestRequest()
				var headers http.Header
				if mode == "stream" {
					result, errStream := executor.ExecuteStream(t.Context(), auth, req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					headers = result.Headers
					var output []byte
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatalf("compressed stream failed: %v", chunk.Err)
						}
						output = append(output, chunk.Payload...)
					}
					if !bytes.Contains(output, []byte("hello")) {
						t.Fatalf("decoded completion not found: %s", output)
					}
				} else {
					if mode == "compact" {
						opts.Alt = "responses/compact"
					}
					result, errExecute := executor.Execute(t.Context(), auth, req, opts)
					if errExecute != nil {
						t.Fatal(errExecute)
					}
					headers = result.Headers
					if !gjson.ValidBytes(result.Payload) || gjson.GetBytes(result.Payload, "id").String() != "resp_1" {
						t.Fatalf("invalid decoded payload: %s", result.Payload)
					}
				}
				if headers.Get("Content-Encoding") != "" || headers.Get("Content-Length") != "" {
					t.Fatalf("decoded response retained encoding/length: %v", headers)
				}
			})
		}
	}
}

func TestCodexExecutor_CompressionRejectedFallback(t *testing.T) {
	for _, mode := range []string{"execute", "stream", "compact"} {
		for _, status := range []int{http.StatusBadRequest, http.StatusUnsupportedMediaType} {
			t.Run(mode+"/"+strconv.Itoa(status), func(t *testing.T) {
				var encodings []string
				var firstPlain []byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					encoding := r.Header.Get("Content-Encoding")
					encodings = append(encodings, encoding)
					wire, errRead := io.ReadAll(r.Body)
					if errRead != nil {
						t.Errorf("read request: %v", errRead)
					}
					if encoding == "zstd" {
						decoder, errDecoder := zstd.NewReader(nil)
						if errDecoder != nil {
							t.Errorf("create decoder: %v", errDecoder)
							return
						}
						defer decoder.Close()
						firstPlain, errRead = decoder.DecodeAll(wire, nil)
						if errRead != nil {
							t.Errorf("decode request: %v", errRead)
						}
						w.WriteHeader(status)
						return
					}
					if len(encodings) > 1 && !bytes.Equal(wire, firstPlain) {
						t.Error("retry changed the final business payload")
					}
					if got := gjson.GetBytes(wire, "audit").Raw; got != `["once"]` {
						t.Errorf("payload rules must run exactly once: audit=%s", got)
					}
					if r.Header.Get(codexRoutingHintHeader) == "" || r.Header.Get("Authorization") != "Bearer test-oauth" {
						t.Error("retry lost routing or authentication headers")
					}
					w.Header().Set("Content-Type", "text/event-stream")
					output := "data: " + codexCompletedEventBody + "\n\n"
					if mode == "compact" {
						output = `{"id":"resp_1","object":"response","status":"completed","output":[]}`
					}
					if _, errWrite := io.WriteString(w, output); errWrite != nil {
						t.Errorf("write completion: %v", errWrite)
					}
				}))
				defer server.Close()
				localURL, errURL := url.Parse(server.URL)
				if errURL != nil {
					t.Fatal(errURL)
				}
				// Keep the real ChatGPT URL through the gate, redirecting only at transport time.
				transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
					mapped := req.Clone(req.Context())
					mapped.URL.Scheme, mapped.URL.Host = localURL.Scheme, localURL.Host
					mapped.Host = localURL.Host
					return http.DefaultTransport.RoundTrip(mapped)
				})
				ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", transport)
				cfg := codexCompressionConfig()
				cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"audit.-1": "once"}}}
				executor := NewCodexExecutor(cfg)
				req := cliproxyexecutor.Request{Model: "gpt-5.4", Payload: codexCompressionPayload()}
				opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse}
				if mode == "stream" {
					result, errStream := executor.ExecuteStream(ctx, codexCompressionAuth(), req, opts)
					if errStream != nil {
						t.Fatal(errStream)
					}
					for chunk := range result.Chunks {
						if chunk.Err != nil {
							t.Fatal(chunk.Err)
						}
					}
				} else {
					if mode == "compact" {
						opts.Alt = "responses/compact"
					}
					if _, errExecute := executor.Execute(ctx, codexCompressionAuth(), req, opts); errExecute != nil {
						t.Fatal(errExecute)
					}
				}
				if len(encodings) != 2 || encodings[0] != "zstd" || encodings[1] != "" {
					t.Fatalf("request encodings=%v, want [zstd plaintext]", encodings)
				}
			})
		}
	}
}

func TestCodexUpstreamCompression_FinalPayload(t *testing.T) {
	executor := NewCodexExecutor(codexCompressionConfig())
	payload := codexCompressionPayload()
	finalPayload := []byte(`{"model":"operator-model","input":[{"id":"operator-id","role":"user","content":"` + strings.Repeat("final payload ", 400) + `"}]}`)
	calls := 0
	ctx := helps.WithPayloadFinalizer(t.Context(), func(body []byte) []byte {
		calls++
		return finalPayload
	})
	req, upstreamBody, errCache := executor.cacheHelper(ctx, sdktranslator.FormatOpenAIResponse, "https://chatgpt.com/backend-api/codex/responses", cliproxyexecutor.Request{}, payload, codexCompressionAuth())
	if errCache != nil {
		t.Fatal(errCache)
	}
	if calls != 1 || !bytes.Equal(upstreamBody, finalPayload) {
		t.Fatalf("final payload mutated or applied %d times", calls)
	}
	decoder, errDecoder := zstd.NewReader(nil)
	if errDecoder != nil {
		t.Fatal(errDecoder)
	}
	defer decoder.Close()
	plain, errDecode := decoder.DecodeAll(readCodexCompressionBody(t, req.Body), nil)
	if errDecode != nil || !bytes.Equal(plain, finalPayload) {
		t.Fatalf("wire does not preserve the final semantic barrier: %v", errDecode)
	}
}

func TestCodexUpstreamCompression_ContentEncodingGuardMatrix(t *testing.T) {
	for _, tt := range []struct {
		name, body, encoding, want string
	}{
		{name: "plain-object", body: " \n{}", encoding: "zstd"},
		{name: "plain-array", body: "[]", encoding: "GZIP"},
		{name: "unknown", body: "opaque", encoding: "custom", want: "custom"},
		{name: "gzip", body: "\x1f\x8bbody", encoding: "identity", want: "gzip"},
		{name: "zstd", body: "\x28\xb5\x2f\xfdbody", encoding: "identity", want: "zstd"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			req, errNewRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com", strings.NewReader(tt.body))
			if errNewRequest != nil {
				t.Fatal(errNewRequest)
			}
			req.Header.Set("Content-Encoding", tt.encoding)
			alignCodexContentEncoding(req)
			if got := req.Header.Get("Content-Encoding"); got != tt.want {
				t.Fatalf("Content-Encoding=%q, want %q", got, tt.want)
			}
			if got := readCodexCompressionBody(t, req.Body); string(got) != tt.body {
				t.Fatal("guard consumed or changed the live body")
			}
		})
	}
}

type codexCompressionCloseBody struct {
	io.Reader
	closed *int
}

func (b *codexCompressionCloseBody) Close() error {
	*b.closed++
	return nil
}

func TestCodexExecutor_CompressionFallbackRetryLimit(t *testing.T) {
	for _, tt := range []struct {
		name       string
		compressed bool
		status     int
		wantCalls  int
	}{
		{name: "compressed-415", compressed: true, status: 415, wantCalls: 2},
		{name: "compressed-400", compressed: true, status: 400, wantCalls: 2},
		{name: "compressed-500", compressed: true, status: 500, wantCalls: 1},
		{name: "plain-415", status: 415, wantCalls: 1},
		{name: "plain-400", status: 400, wantCalls: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			plain := []byte(`{"input":"hello"}`)
			wire := plain
			if tt.compressed {
				wire = codexCompressedResponse(t, "zstd", plain)
			}
			req, errNewRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(wire))
			if errNewRequest != nil {
				t.Fatal(errNewRequest)
			}
			calls, closed := 0, 0
			client := &http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				if calls > 1 && request.Header.Get("Content-Encoding") != "" {
					t.Error("retry must be uncompressed")
				}
				return &http.Response{StatusCode: tt.status, Header: make(http.Header), Body: &codexCompressionCloseBody{Reader: strings.NewReader("rejected"), closed: &closed}}, nil
			})}
			resp, errDo := doCodexRequest(client, req, plain, func(request *http.Request) {}, nil)
			if errDo != nil {
				t.Fatal(errDo)
			}
			if calls != tt.wantCalls || closed != tt.wantCalls-1 {
				t.Fatalf("calls=%d closed=%d, want calls=%d closed=%d", calls, closed, tt.wantCalls, tt.wantCalls-1)
			}
			if errClose := resp.Body.Close(); errClose != nil {
				t.Fatal(errClose)
			}
		})
	}
}

func TestCodexExecutor_CompressedImageResponsesDecoded(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, encoding := range []string{"gzip", "zstd"} {
				name := "direct=" + strconv.FormatBool(direct) + "/stream=" + strconv.FormatBool(stream) + "/" + encoding
				t.Run(name, func(t *testing.T) {
					plain := []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_img\",\"output\":[{\"type\":\"image_generation_call\",\"result\":\"AA==\",\"output_format\":\"png\"}]}}\n\n")
					if direct && stream {
						plain = []byte("event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"AA==\"}\n\n")
					} else if direct {
						plain = []byte(`{"created":1713833628,"data":[{"b64_json":"AA=="}]}`)
					}
					wire := codexCompressedResponse(t, encoding, plain)
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						if got := r.Header.Get("Accept-Encoding"); got != "gzip, deflate, br, zstd" {
							t.Errorf("image Accept-Encoding=%q", got)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						w.Header().Set("Content-Encoding", encoding)
						if _, errWrite := w.Write(wire); errWrite != nil {
							t.Errorf("write compressed image response: %v", errWrite)
						}
					}))
					defer server.Close()
					auth := codexTestAuth(server.URL)
					if !direct {
						auth.Metadata = map[string]any{"use_siwc": true, "siwc": map[string]any{"access_token": "siwc-test"}}
					}
					req := cliproxyexecutor.Request{Model: "gpt-image-1.5", Payload: []byte(`{"model":"gpt-image-1.5","prompt":"a cat"}`)}
					opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream)
					executor := NewCodexExecutor(&config.Config{})
					var headers http.Header
					var output []byte
					if stream {
						result, errStream := executor.ExecuteStream(t.Context(), auth, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						headers = result.Headers
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							output = append(output, chunk.Payload...)
						}
					} else {
						result, errExecute := executor.Execute(t.Context(), auth, req, opts)
						if errExecute != nil {
							t.Fatal(errExecute)
						}
						headers, output = result.Headers, result.Payload
					}
					if !bytes.Contains(output, []byte("AA==")) || headers.Get("Content-Encoding") != "" || headers.Get("Content-Length") != "" {
						t.Fatalf("image response not decoded: headers=%v output=%s", headers, output)
					}
				})
			}
		}
	}
}

func TestCodexUpstreamCompression_DirectImagePayloadRewrite(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run("stream="+strconv.FormatBool(stream), func(t *testing.T) {
			cfg := codexCompressionConfig()
			cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"prompt": "operator-prompt", "audit.-1": "once"}}}
			calls := 0
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				if got := req.Header.Get("Content-Encoding"); got != "" {
					t.Errorf("rewritten image body retained Content-Encoding=%q", got)
				}
				body := readCodexCompressionBody(t, req.Body)
				if !gjson.ValidBytes(body) || gjson.GetBytes(body, "prompt").String() != "operator-prompt" || gjson.GetBytes(body, "audit").Raw != `["once"]` {
					t.Errorf("image payload finalization was not preserved: %s", body)
				}
				output := `{"created":1713833628,"data":[{"b64_json":"AA=="}]}`
				if stream {
					output = "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"AA==\"}\n\n"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(output))}, nil
			})
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", transport)
			req := cliproxyexecutor.Request{Model: "gpt-image-1.5", Payload: []byte(`{"model":"gpt-image-1.5","prompt":"` + strings.Repeat("large prompt ", 400) + `"}`)}
			opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream)
			executor := NewCodexExecutor(cfg)
			if stream {
				result, errStream := executor.ExecuteStream(ctx, codexCompressionAuth(), req, opts)
				if errStream != nil {
					t.Fatal(errStream)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, errExecute := executor.Execute(ctx, codexCompressionAuth(), req, opts); errExecute != nil {
				t.Fatal(errExecute)
			}
			if calls != 1 {
				t.Fatalf("upstream calls=%d, want 1", calls)
			}
		})
	}
}
