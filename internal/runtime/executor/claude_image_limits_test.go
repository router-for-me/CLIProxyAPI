package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

// testPNG builds a deterministic PNG. noise=true fills it with pseudo-random
// opaque pixels so it does not compress; alpha<255 makes it transparent.
func testPNG(t *testing.T, w, h int, noise bool, alpha uint8) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	seed := uint32(2463534242)
	for i := 0; i < len(img.Pix); i += 4 {
		if noise {
			seed ^= seed << 13
			seed ^= seed >> 17
			seed ^= seed << 5
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = uint8(seed), uint8(seed>>8), uint8(seed>>16)
		} else {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 200, 30, 30
		}
		img.Pix[i+3] = alpha
	}
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.NoCompression}).Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func imageBlock(mediaType, data string) string {
	return `{"type":"image","source":{"type":"base64","media_type":"` + mediaType + `","data":"` + data + `"}}`
}

func urlImageBlock(url string) string {
	return `{"type":"image","source":{"type":"url","url":"` + url + `"}}`
}

func claudeImagePayload(blocks ...string) []byte {
	content := append([]string{`{"type":"text","text":"describe"}`}, blocks...)
	return []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":[` + strings.Join(content, ",") + `]}]}`)
}

func imageDims(t *testing.T, block gjson.Result) (string, int, int) {
	t.Helper()
	cfg, format, err := image.DecodeConfig(base64.NewDecoder(base64.StdEncoding, strings.NewReader(block.Get("source.data").String())))
	if err != nil {
		t.Fatalf("decode %s: %v", block.Raw[:min(80, len(block.Raw))], err)
	}
	return format, cfg.Width, cfg.Height
}

func imagesOf(body []byte) []gjson.Result {
	var out []gjson.Result
	for _, message := range gjson.GetBytes(body, "messages").Array() {
		for _, block := range message.Get("content").Array() {
			switch block.Get("type").String() {
			case "image":
				out = append(out, block)
			case "tool_result":
				for _, inner := range block.Get("content").Array() {
					if inner.Get("type").String() == "image" {
						out = append(out, inner)
					}
				}
			}
		}
	}
	return out
}

// --- executor paths ---

func imageLimitSenders() map[string]func(*ClaudeExecutor, context.Context, []byte, sdktranslator.Format) error {
	return map[string]func(*ClaudeExecutor, context.Context, []byte, sdktranslator.Format) error{
		"execute": func(ex *ClaudeExecutor, ctx context.Context, payload []byte, from sdktranslator.Format) error {
			_, err := ex.Execute(ctx, midSystemAuth(), cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: from})
			return err
		},
		"stream": func(ex *ClaudeExecutor, ctx context.Context, payload []byte, from sdktranslator.Format) error {
			result, err := ex.ExecuteStream(ctx, midSystemAuth(), cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: from})
			if err != nil {
				return err
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					return chunk.Err
				}
			}
			return nil
		},
		"count tokens": func(ex *ClaudeExecutor, ctx context.Context, payload []byte, from sdktranslator.Format) error {
			_, err := ex.CountTokens(ctx, midSystemAuth(), cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: from})
			return err
		},
		// A gateway without Anthropic's count_tokens uses the local estimator.
		"count tokens local": func(ex *ClaudeExecutor, ctx context.Context, payload []byte, from sdktranslator.Format) error {
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "base_url": "https://gateway.example.invalid"}}
			_, err := ex.CountTokens(ctx, auth, cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{SourceFormat: from})
			return err
		},
	}
}

func openAIImagePayload(n int, data string) []byte {
	parts := []string{`{"type":"text","text":"describe"}`}
	for i := 0; i < n; i++ {
		parts = append(parts, `{"type":"image_url","image_url":{"url":"data:image/png;base64,`+data+`"}}`)
	}
	return []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":[` + strings.Join(parts, ",") + `]}]}`)
}

func TestClaudeExecutor_ImageLimitsOnEveryPathNativeAndTranslated(t *testing.T) {
	big := testPNG(t, 3100, 16, false, 255)
	var native []string
	for i := 0; i < 21; i++ {
		native = append(native, imageBlock("image/png", big))
	}
	inputs := map[string]struct {
		from    sdktranslator.Format
		payload []byte
	}{
		"native":     {sdktranslator.FormatClaude, claudeImagePayload(native...)},
		"translated": {sdktranslator.FormatOpenAI, openAIImagePayload(21, big)},
	}
	for inputName, input := range inputs {
		for pathName, send := range imageLimitSenders() {
			if pathName == "count tokens local" {
				continue // no upstream body to inspect
			}
			t.Run(inputName+"/"+pathName, func(t *testing.T) {
				upstream := &midSystemUpstream{}
				if err := send(NewClaudeExecutor(midSystemConfig()), upstream.context(t, nil), input.payload, input.from); err != nil {
					t.Fatal(err)
				}
				images := imagesOf(upstream.body)
				if len(images) != 21 {
					t.Fatalf("upstream images = %d", len(images))
				}
				for i, img := range images {
					if _, w, _ := imageDims(t, img); w != 3000 {
						t.Fatalf("image %d width %d, want 3000", i, w)
					}
				}
			})
		}
	}
}

// R6 at the R3 threshold: a sweep of request sizes around 32 MiB crosses the
// point where the oldest image is omitted. Execute and CountTokens must reach
// the same image blocks on every point, so the auth adds nothing to the body
// after the reduction that could push Execute over the limit.
func TestClaudeExecutor_CountTokensReachesSameImagesAsExecuteAtBudgetBoundary(t *testing.T) {
	auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "key-123", "cloak_mode": "never"}}
	newest := imageBlock("image/gif", "R0lGOD"+strings.Repeat("A", 1<<20))
	build := func(oldLen int) []byte {
		return claudeImagePayload(imageBlock("image/gif", "R0lGOD"+strings.Repeat("A", oldLen)), newest)
	}
	baseLen := (32 << 20) - len(build(0))
	omitted := map[bool]bool{}
	for _, delta := range []int{-4096, -1024, -256, -64, -16, -1, 0, 1, 16, 64, 256, 1024, 4096} {
		payload := build(baseLen + delta)
		var bodies [][]byte
		for _, count := range []bool{false, true} {
			upstream := &midSystemUpstream{}
			ex := NewClaudeExecutor(midSystemConfig())
			req := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			var err error
			if count {
				_, err = ex.CountTokens(upstream.context(t, nil), auth, req, opts)
			} else {
				_, err = ex.Execute(upstream.context(t, nil), auth, req, opts)
			}
			if err != nil {
				// Aneks (a): Execute fitted before the reduction and was pushed
				// over by fields added later. Omitting the 32 MiB-sized oldest
				// image would have left it far under the limit, so a size error
				// means Execute decided to keep every image.
				if !count && strings.Contains(err.Error(), "request body is") {
					bodies = append(bodies, nil)
					continue
				}
				t.Fatalf("delta %d count=%v: %v", delta, count, err)
			}
			bodies = append(bodies, upstream.body)
		}
		if bodies[0] == nil {
			if got := len(imagesOf(bodies[1])); got != 2 {
				t.Fatalf("delta %d: Execute kept every image but CountTokens has %d", delta, got)
			}
			omitted[false] = true
			continue
		}
		a, b := imagesOf(bodies[0]), imagesOf(bodies[1])
		if len(a) != len(b) {
			t.Fatalf("delta %d: image counts %d vs %d", delta, len(a), len(b))
		}
		// cache_control placement differs between the two endpoints by design;
		// the image decision is the block type and its source.
		for i := range a {
			if a[i].Get("source").Raw != b[i].Get("source").Raw {
				t.Fatalf("delta %d: image %d differs between Execute and CountTokens", delta, i)
			}
		}
		omitted[len(a) == 1] = true
	}
	if !omitted[true] || !omitted[false] {
		t.Fatalf("sweep did not cross the threshold: %v", omitted)
	}
}

func TestClaudeExecutor_ImageLimitErrorsLocallyAfterPayloadRules(t *testing.T) {
	small := testPNG(t, 10, 10, false, 255)
	huge := testPNG(t, 9000, 2, false, 255)
	mid := testPNG(t, 3100, 16, false, 255)
	var twenty []string
	for i := 0; i < 20; i++ {
		twenty = append(twenty, imageBlock("image/png", mid))
	}
	appendImage := func(data string) map[string]any {
		return map[string]any{"messages.0.content.-1": map[string]any{
			"type": "image", "source": map[string]any{"type": "base64", "media_type": "image/png", "data": data},
		}}
	}
	for _, test := range []struct {
		name    string
		rule    map[string]any
		payload []byte
		want    string
	}{
		{"rule adds 9000 px PNG", appendImage(huge), claudeImagePayload(imageBlock("image/png", small)), "per side"},
		{"rule adds 21st image", appendImage(small), claudeImagePayload(twenty...), "more than 20 images"},
		{"body over 32 MB", nil, claudeImagePayload(`{"type":"text","text":"` + strings.Repeat("a", (32<<20)) + `"}`), "request body is"},
	} {
		for pathName, send := range imageLimitSenders() {
			t.Run(test.name+"/"+pathName, func(t *testing.T) {
				cfg := midSystemConfig()
				if test.rule != nil {
					cfg.Payload.Override = []config.PayloadRule{{Models: []config.PayloadModelRule{{Name: "*"}}, Params: test.rule}}
				}
				upstream := &midSystemUpstream{}
				err := send(NewClaudeExecutor(cfg), upstream.context(t, nil), test.payload, sdktranslator.FormatClaude)
				if err == nil || !strings.Contains(err.Error(), test.want) {
					t.Fatalf("err = %v, want %q", err, test.want)
				}
				if upstream.called {
					t.Fatal("upstream called for a request that breaks Anthropic's limits")
				}
			})
		}
	}
}

func TestClaudeExecutor_ImagesInLimitsAreSentByteForByte(t *testing.T) {
	data := testPNG(t, 300, 200, true, 255)
	upstream := &midSystemUpstream{}
	if err := imageLimitSenders()["execute"](NewClaudeExecutor(midSystemConfig()), upstream.context(t, nil), claudeImagePayload(imageBlock("image/png", data)), sdktranslator.FormatClaude); err != nil {
		t.Fatal(err)
	}
	if got := imagesOf(upstream.body)[0].Get("source.data").String(); got != data {
		t.Fatal("image in limits was modified")
	}
}
