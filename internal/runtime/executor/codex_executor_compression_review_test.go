package executor

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/tidwall/gjson"
)

func TestCodexImageCompressionRejectedFallback(t *testing.T) {
	for _, direct := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			for _, status := range []int{http.StatusBadRequest, http.StatusUnsupportedMediaType} {
				name := "direct=" + strconv.FormatBool(direct) + "/stream=" + strconv.FormatBool(stream) + "/" + strconv.Itoa(status)
				t.Run(name, func(t *testing.T) {
					var encodings []string
					var firstPlain []byte
					transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
						encodings = append(encodings, req.Header.Get("Content-Encoding"))
						wire := readCodexCompressionBody(t, req.Body)
						if req.Header.Get("Content-Encoding") == "zstd" {
							decoder, errNewReader := zstd.NewReader(nil)
							if errNewReader != nil {
								t.Fatal(errNewReader)
							}
							defer decoder.Close()
							var errDecode error
							firstPlain, errDecode = decoder.DecodeAll(wire, nil)
							if errDecode != nil {
								t.Fatal(errDecode)
							}
							return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("compression rejected"))}, nil
						}
						if !direct && !bytes.Equal(wire, firstPlain) {
							t.Error("retry changed the final business payload")
						}
						if got := gjson.GetBytes(wire, "audit").Raw; got != `["once"]` {
							t.Errorf("payload rules must run exactly once: audit=%s", got)
						}
						if got := req.Header.Get("Authorization"); got != "Bearer test-oauth" {
							t.Errorf("Authorization=%q", got)
						}
						output := "data: {\"type\":\"response.completed\",\"response\":{\"output\":[{\"type\":\"image_generation_call\",\"result\":\"AA==\",\"output_format\":\"png\"}]}}\n\n"
						if direct && stream {
							output = "event: image_generation.completed\ndata: {\"type\":\"image_generation.completed\",\"b64_json\":\"AA==\"}\n\n"
						} else if direct {
							output = `{"data":[{"b64_json":"AA=="}]}`
						}
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(output))}, nil
					})
					ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", transport)
					cfg := codexCompressionConfig()
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: map[string]any{"audit.-1": "once"}}}
					executor := NewCodexExecutor(cfg)
					model := "custom-image"
					if direct {
						model = "gpt-image-2"
					}
					req := cliproxyexecutor.Request{Model: model, Payload: []byte(`{"model":"` + model + `","prompt":"` + strings.Repeat("a compressible cat ", 400) + `"}`)}
					opts := codexOpenAIImageTestOptions(codexImagesGenerationsPath, stream)
					auth := codexCompressionAuth()
					// Custom headers must not relabel the plaintext fallback or direct body.
					auth.Attributes = map[string]string{"header:Content-Encoding": "zstd"}
					var output []byte
					if stream {
						result, errStream := executor.ExecuteStream(ctx, auth, req, opts)
						if errStream != nil {
							t.Fatal(errStream)
						}
						for chunk := range result.Chunks {
							if chunk.Err != nil {
								t.Fatal(chunk.Err)
							}
							output = append(output, chunk.Payload...)
						}
					} else {
						result, errExecute := executor.Execute(ctx, auth, req, opts)
						if errExecute != nil {
							t.Fatal(errExecute)
						}
						output = result.Payload
					}
					if !bytes.Contains(output, []byte("AA==")) {
						t.Fatalf("missing image result: %s", output)
					}
					if direct {
						if len(encodings) != 1 || encodings[0] != "" {
							t.Fatalf("direct image encodings=%v, want one plaintext request", encodings)
						}
					} else if len(encodings) != 2 || encodings[0] != "zstd" || encodings[1] != "" {
						t.Fatalf("image encodings=%v, want [zstd plaintext]", encodings)
					}
				})
			}
		}
	}
}

func TestCodexCompressionContentEncodingChains(t *testing.T) {
	for _, encodings := range [][]string{{"gzip", "gzip"}, {"zstd", "zstd"}, {"gzip", "zstd"}, {"zstd", "gzip"}} {
		encoding := strings.Join(encodings, ", ")
		wire := []byte(`{"message":"preserve every encoding layer"}`)
		for _, layer := range encodings {
			wire = codexCompressedResponse(t, layer, wire)
		}
		for _, prepare := range []bool{false, true} {
			t.Run(encoding+"/prepare="+strconv.FormatBool(prepare), func(t *testing.T) {
				req, errNewRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com", bytes.NewReader(wire))
				if errNewRequest != nil {
					t.Fatal(errNewRequest)
				}
				req.Header.Set("Content-Type", "application/json")
				if prepare {
					auth := codexCompressionAuth()
					auth.Attributes = map[string]string{"header:Content-Encoding": encoding}
					if errPrepare := NewCodexExecutor(codexCompressionConfig()).PrepareRequest(req, auth); errPrepare != nil {
						t.Fatal(errPrepare)
					}
				} else {
					for _, layer := range encodings {
						req.Header.Add("Content-Encoding", layer)
					}
					alignCodexContentEncoding(req)
				}
				if got := strings.Join(req.Header.Values("Content-Encoding"), ", "); got != encoding {
					t.Errorf("Content-Encoding=%q, want %q", got, encoding)
				}
				if got := readCodexCompressionBody(t, req.Body); !bytes.Equal(got, wire) {
					t.Fatal("alignment consumed or changed the multi-layer body")
				}
			})
		}
	}
}

func TestCodexCompressionContentEncodingAlignment(t *testing.T) {
	for _, tc := range []struct {
		name, body, contentType, encoding, want string
	}{
		{name: "zstd_identity", body: "\x28\xb5\x2f\xfdbody", encoding: "identity", want: "zstd"},
		{name: "zstd_kept", body: "\x28\xb5\x2f\xfdbody", encoding: "zstd", want: "zstd"},
		{name: "zstd_missing", body: "\x28\xb5\x2f\xfdbody", want: "zstd"},
		{name: "zstd_mislabeled_gzip", body: "\x28\xb5\x2f\xfdbody", encoding: "gzip", want: "zstd"},
		{name: "zstd_single_with_identity", body: "\x28\xb5\x2f\xfdbody", encoding: "gzip, identity", want: "zstd"},
		{name: "gzip_identity", body: "\x1f\x8bbody", encoding: "identity", want: "gzip"},
		{name: "json_br", body: " \n{}", encoding: "br"},
		{name: "json_deflate", body: "[]", encoding: "deflate"},
		{name: "json_combined", body: "{}", encoding: "gzip, zstd"},
		{name: "json_double_gzip", body: "{}", encoding: "gzip, gzip"},
		{name: "json_multiple_headers", body: "{}", encoding: "gzip, zstd, br"},
		{name: "multipart_zstd", body: "--boundary\r\nContent-Disposition: form-data; name=\"image\"\r\n\r\nimage\r\n--boundary--\r\n", contentType: "multipart/form-data; boundary=boundary", encoding: "zstd"},
		{name: "multipart_br", body: "--boundary\r\n", contentType: "multipart/form-data; boundary=boundary", encoding: "br"},
		{name: "multipart_chain", body: "--boundary\r\n", contentType: "multipart/form-data; boundary=boundary", encoding: "gzip, zstd"},
		{name: "opaque_zstd", body: "opaque", encoding: "zstd"},
		{name: "opaque_combined", body: "opaque", encoding: "br, ZSTD", want: "br, ZSTD"},
		{name: "opaque_br", body: "opaque", encoding: "br", want: "br"},
		{name: "opaque_custom", body: "opaque", encoding: "custom", want: "custom"},
	} {
		for _, prepare := range []bool{false, true} {
			t.Run(tc.name+"/prepare="+strconv.FormatBool(prepare), func(t *testing.T) {
				req, errNewRequest := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://example.com", strings.NewReader(tc.body))
				if errNewRequest != nil {
					t.Fatal(errNewRequest)
				}
				req.Header.Set("Content-Type", tc.contentType)
				if prepare {
					auth := codexCompressionAuth()
					auth.Attributes = map[string]string{"header:Content-Encoding": tc.encoding}
					if errPrepare := NewCodexExecutor(codexCompressionConfig()).PrepareRequest(req, auth); errPrepare != nil {
						t.Fatal(errPrepare)
					}
				} else {
					for _, encoding := range strings.Split(tc.encoding, ",") {
						req.Header.Add("Content-Encoding", strings.TrimSpace(encoding))
					}
					alignCodexContentEncoding(req)
				}
				if got := strings.Join(req.Header.Values("Content-Encoding"), ", "); got != tc.want {
					t.Errorf("Content-Encoding=%q, want %q", got, tc.want)
				}
				if got := readCodexCompressionBody(t, req.Body); string(got) != tc.body {
					t.Fatal("alignment consumed or changed the live body")
				}
			})
		}
	}
}
