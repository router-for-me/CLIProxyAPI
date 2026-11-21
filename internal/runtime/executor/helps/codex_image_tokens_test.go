package helps

import (
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/tiktoken-go/tokenizer"
)

func codexTokenTestPNG(width, height uint32) string {
	header := make([]byte, 25)
	binary.BigEndian.PutUint32(header[0:4], 13)
	copy(header[4:8], "IHDR")
	binary.BigEndian.PutUint32(header[8:12], width)
	binary.BigEndian.PutUint32(header[12:16], height)
	header[16] = 8
	header[17] = 2
	binary.BigEndian.PutUint32(header[21:25], crc32.ChecksumIEEE(header[4:21]))
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(append([]byte("\x89PNG\r\n\x1a\n"), header...))
}

func TestEstimateCodexImageTokensDocumentedExamples(t *testing.T) {
	for _, tc := range []struct {
		model, detail string
		width, height uint32
		want          int64
	}{
		{"gpt-6-astra", "high", 1024, 1024, 1229},
		{"gpt-6-astra", "high", 2048, 2048, 3000},
		{"gpt-6-astra", "high", 4096, 512, 2458},
		{"gpt-6-astra", "low", 1024, 1024, 308},
		{"gpt-6-astra", "auto", 2048, 2048, 4916},
		{"gpt-6-astra", "original", 32, 32, 2},
		{"gpt-5.4", "high", 2048, 2048, 3000},
		{"gpt-5.4", "original", 2048, 2048, 4916},
		{"gpt-5", "low", 1024, 1024, 70},
		{"gpt-5", "high", 1024, 1024, 630},
		{"gpt-5-2025-08-07", "low", 1024, 1024, 70},
		{"gpt-5-2025-08-07", "high", 1024, 1024, 630},
		{"gpt-5-2025-08-07", "auto", 1024, 1024, 630},
		{"gpt-5-mini", "low", 1024, 1024, 1844},
		{"gpt-5-2025-08-07-custom", "low", 1024, 1024, 1844},
		{"gpt-5-2025-99-99", "low", 1024, 1024, 1844},
		{"gpt-4o", "low", 1024, 1024, 85},
		{"gpt-4o", "high", 1024, 1024, 765},
		{"gpt-4o", "high", 1, 65535, 765},
		{"gpt-4o", "high", 65535, 1, 765},
		{"gpt-4o-2026-11-14", "high", 1024, 1024, 765},
		{"gpt-4o-20custom", "high", 1024, 1024, 1844},
		{"gpt-4o-2026-99-99", "high", 1024, 1024, 1844},
		{"gpt-4o-mini", "high", 1024, 1024, 25501},
		{"gpt-4.1-mini", "high", 1024, 1024, 1659},
		{"gpt-4.1-unknown", "high", 1024, 1024, 1844},
		{"gpt-6.1-sol", "auto", 32, 32, 1844},
		{"gpt-6.1-terra", "original", 1024, 1024, 1024},
		{"gpt-6-luna", "original", 65535, 65535, 10000},
	} {
		t.Run(fmt.Sprintf("%s/%s/%dx%d", tc.model, tc.detail, tc.width, tc.height), func(t *testing.T) {
			doc := ojson.NewDocString(`{"type":"input_image"}`)
			if err := doc.Set("image_url", codexTokenTestPNG(tc.width, tc.height)); err != nil {
				t.Fatal(err)
			}
			if err := doc.Set("detail", tc.detail); err != nil {
				t.Fatal(err)
			}
			got := estimateCodexImageTokens(tc.model, doc.Get("@this"))
			if got != tc.want {
				t.Fatalf("%dx%d: got %d, want %d", tc.width, tc.height, got, tc.want)
			}
		})
	}
}

func TestEstimateCodexImageTokensGPT54DetailLevels(t *testing.T) {
	// The documented 6,144-patch low budget exceeds high/auto's 2,500.
	// https://developers.openai.com/api/docs/guides/images-vision#model-sizing-behavior
	for _, model := range []string{"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano"} {
		for _, tc := range []struct {
			detail string
			want   int64
		}{{"low", 4916}, {"high", 3000}, {"auto", 3000}, {"", 3000}} {
			t.Run(model+"/"+tc.detail, func(t *testing.T) {
				doc := ojson.NewDocString(`{"type":"input_image"}`)
				if err := doc.Set("image_url", codexTokenTestPNG(2048, 2048)); err != nil {
					t.Fatal(err)
				}
				if tc.detail != "" {
					if err := doc.Set("detail", tc.detail); err != nil {
						t.Fatal(err)
					}
				}
				if got := estimateCodexImageTokens(model, doc.Get("@this")); got != tc.want {
					t.Fatalf("got %d, want %d", got, tc.want)
				}
			})
		}
	}
}

func TestEstimateCodexImageTokensUnavailableDimensions(t *testing.T) {
	for _, url := range []string{"http://127.0.0.1:1/never-fetch", "file:///etc/passwd", "data:text/plain;base64,AA==", "data:image/png,not-base64", "data:image/png;base64,not-base64", "data:image/png;base64," + strings.Repeat("A", 2<<20)} {
		doc := ojson.NewDocString(`{"type":"input_image"}`)
		if err := doc.Set("image_url", url); err != nil {
			t.Fatal(err)
		}
		if got := estimateCodexImageTokens("gpt-6-astra", doc.Get("@this")); got != 1844 {
			t.Fatalf("unknown dimensions: got %d", got)
		}
	}
}

func TestCountResponsesInputTokensIncludesMessageAndToolImages(t *testing.T) {
	enc, err := tokenizer.Get(tokenizer.O200kBase)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"input.0.content", "input.0.output"} {
		doc := ojson.NewDocString(`{"model":"gpt-6-astra","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"What color?"}]}]}`)
		if path == "input.0.output" {
			if err := doc.Set("input.0.type", "function_call_output"); err != nil {
				t.Fatal(err)
			}
			if err := doc.Set("input.0.output", []any{map[string]any{"type": "input_text", "text": "What color?"}}); err != nil {
				t.Fatal(err)
			}
			if err := doc.Delete("input.0.content"); err != nil {
				t.Fatal(err)
			}
		}
		before, err := CountResponsesInputTokens(enc, doc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if err := doc.Set(path+".1", map[string]any{"type": "input_image", "image_url": codexTokenTestPNG(32, 32), "detail": "high"}); err != nil {
			t.Fatal(err)
		}
		after, err := CountResponsesInputTokens(enc, doc.Bytes())
		if err != nil {
			t.Fatal(err)
		}
		if after-before != 2 {
			t.Fatalf("image at %s: delta %d, want 2", path, after-before)
		}
	}
	imageOnly := ojson.NewDocString(`{"model":"gpt-6-astra","input":[{"type":"message","content":[]}]}`)
	if err := imageOnly.Set("input.0.content.0", map[string]any{"type": "input_image", "image_url": codexTokenTestPNG(32, 32)}); err != nil {
		t.Fatal(err)
	}
	got, err := CountResponsesInputTokens(enc, imageOnly.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("image-only request = %d, want 2", got)
	}
}

func TestEstimateCodexImageTokensHighDetailBudget(t *testing.T) {
	for _, w := range []uint32{1, 2, 31, 32, 33, 511, 1024, 2048, 60000, 65535} {
		for _, h := range []uint32{1, 2, 31, 32, 33, 511, 1024, 2048, 60000, 65535} {
			doc := ojson.NewDocString(`{"type":"input_image","detail":"high"}`)
			if err := doc.Set("image_url", codexTokenTestPNG(w, h)); err != nil {
				t.Fatal(err)
			}
			got := estimateCodexImageTokens("gpt-6-astra", doc.Get("@this"))
			if got <= 0 || got > 3000 {
				t.Fatalf("%dx%d = %d, want 1..3000", w, h, got)
			}
		}
	}
}

func TestEstimateCodexImageTokensDoesNotFetchURL(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	doc := ojson.NewDocString(`{"type":"input_image"}`)
	if err := doc.Set("image_url", server.URL); err != nil {
		t.Fatal(err)
	}
	if got := estimateCodexImageTokens("gpt-6-astra", doc.Get("@this")); got != 1844 {
		t.Fatalf("URL fallback = %d", got)
	}
	if calls.Load() != 0 {
		t.Fatal("counting fetched the image URL")
	}
}
