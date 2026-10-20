package helps

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// testPNG builds a deterministic PNG. noise=true fills it with pseudo-random
// opaque pixels so it does not compress; alpha<255 makes it transparent.
func testPNG(t testing.TB, w, h int, noise bool, alpha uint8) string {
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
	for _, ref := range collectClaudeImageBlocks(body) {
		out = append(out, ref.block)
	}
	return out
}

func TestLimitClaudeImages_OverDimensionPNGIsScaledAndStaysPNG(t *testing.T) {
	body := LimitClaudeRequestImages(claudeImagePayload(imageBlock("image/png", testPNG(t, 8400, 20, false, 255))))
	format, w, h := imageDims(t, imagesOf(body)[0])
	if format != "png" || w != 8000 || h != 19 {
		t.Fatalf("got %s %dx%d, want png 8000x19", format, w, h)
	}
	if gjson.GetBytes(body, "messages.0.content.1.source.media_type").String() != "image/png" {
		t.Fatalf("media_type changed: %s", gjson.GetBytes(body, "messages.0.content.1.source").Raw[:120])
	}
}

func TestLimitClaudeImages_OverTenMegabytesIsReencodedUnderLimit(t *testing.T) {
	data := testPNG(t, 1900, 1900, true, 255) // ~14 MB base64
	if len(data) <= claudeImageBase64Limit {
		t.Fatalf("fixture too small: %d", len(data))
	}
	body := LimitClaudeRequestImages(claudeImagePayload(imageBlock("image/png", data)))
	got := imagesOf(body)[0].Get("source.data").String()
	if len(got) > claudeImageBase64Limit {
		t.Fatalf("base64 = %d, want <= %d", len(got), claudeImageBase64Limit)
	}
	if err := ValidateClaudeImageLimits(body); err != nil {
		t.Fatal(err)
	}
}

func TestLimitClaudeImages_ManyImagesCapLongSideAt3000IncludingToolResults(t *testing.T) {
	big := testPNG(t, 3100, 16, false, 255)
	var blocks []string
	for i := 0; i < 20; i++ {
		blocks = append(blocks, imageBlock("image/png", big))
	}
	payload := []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[` +
		`{"role":"user","content":[` + strings.Join(blocks, ",") + `]},` +
		`{"role":"assistant","content":[{"type":"tool_use","id":"tu1","name":"view","input":{}}]},` +
		`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":[` + imageBlock("image/png", big) + `]}]}]}`)
	body := LimitClaudeRequestImages(payload)
	images := imagesOf(body)
	if len(images) != 21 {
		t.Fatalf("images = %d", len(images))
	}
	for i, img := range images {
		if _, w, _ := imageDims(t, img); w != 3000 {
			t.Fatalf("image %d width = %d, want 3000", i, w)
		}
	}
	if err := ValidateClaudeImageLimits(body); err != nil {
		t.Fatal(err)
	}
}

func TestLimitClaudeImages_TwentyImagesAndOtherTypesUntouched(t *testing.T) {
	t.Run("21 images at exactly 3000 px stay byte for byte", func(t *testing.T) {
		edge := imageBlock("image/png", testPNG(t, 3000, 16, false, 255))
		var blocks []string
		for i := 0; i < 21; i++ {
			blocks = append(blocks, edge)
		}
		payload := claudeImagePayload(blocks...)
		if got := LimitClaudeRequestImages(payload); !bytes.Equal(got, payload) {
			t.Fatal("images at the enforced many-image limit were modified")
		}
		if err := ValidateClaudeImageLimits(payload); err != nil {
			t.Fatal(err)
		}
	})
	big := testPNG(t, 3100, 16, false, 255)
	gif := imageBlock("image/gif", "R0lGODlhAQABAAAAACw=")
	url := urlImageBlock("https://example.com/a.png")
	var blocks []string
	for i := 0; i < 18; i++ {
		blocks = append(blocks, imageBlock("image/png", big))
	}
	// 20 image blocks: no many-image rule, every block stays byte for byte.
	payload := claudeImagePayload(append(blocks, gif, url)...)
	if got := LimitClaudeRequestImages(payload); !bytes.Equal(got, payload) {
		t.Fatal("request with 20 images in limits was modified")
	}
	// The 21st block (a GIF) triggers the rule for PNG/JPEG; GIF and URL stay.
	payload = claudeImagePayload(append(blocks, gif, url, gif)...)
	body := LimitClaudeRequestImages(payload)
	images := imagesOf(body)
	if images[18].Raw != gif || images[19].Raw != url || images[20].Raw != gif {
		t.Fatalf("GIF or URL block changed: %s %s", images[18].Raw, images[19].Raw)
	}
	if _, w, _ := imageDims(t, images[0]); w != 3000 {
		t.Fatalf("PNG width = %d, want 3000", w)
	}
}

func TestLimitClaudeImages_IsDeterministic(t *testing.T) {
	payload := claudeImagePayload(imageBlock("image/png", testPNG(t, 8100, 300, true, 128)))
	a := LimitClaudeRequestImages(payload)
	b := LimitClaudeRequestImages(append([]byte(nil), payload...))
	if !bytes.Equal(a, b) {
		t.Fatal("same input produced different bytes")
	}
}

func TestReencodeClaudeImage_TransparentPNGToJPEGGetsWhiteBackground(t *testing.T) {
	body := claudeImagePayload(imageBlock("image/png", testPNG(t, 64, 64, false, 0)))
	ref := collectClaudeImageBlocks(body)[0]
	raw, ok := reencodeClaudeImageBlock(ref, claudeOversizeBodyLongSide, true)
	if !ok {
		t.Fatal("re-encode failed")
	}
	block := gjson.ParseBytes(raw)
	if block.Get("source.media_type").String() != "image/jpeg" {
		t.Fatalf("media_type = %s", block.Get("source.media_type").String())
	}
	data, _ := base64.StdEncoding.DecodeString(block.Get("source.data").String())
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := img.At(10, 10).RGBA()
	if r>>8 < 250 || g>>8 < 250 || b>>8 < 250 {
		t.Fatalf("pixel = %d,%d,%d, want white", r>>8, g>>8, b>>8)
	}
	_ = color.White
}

// fakeGIF is undecodable base64 labelled GIF: the proxy never measures GIFs,
// so it can only be replaced, never shrunk.
func fakeGIF(size int) string {
	return "R0lGOD" + strings.Repeat("A", size-6)
}

func TestLimitClaudeImages_OversizeBodyShrinksOldestFirst(t *testing.T) {
	noisy := testPNG(t, 1600, 1600, true, 255) // ~10 MB base64 each
	if len(noisy) > claudeImageBase64Limit {
		t.Fatalf("fixture over per-image limit: %d", len(noisy))
	}
	payload := claudeImagePayload(imageBlock("image/png", noisy), imageBlock("image/png", noisy), imageBlock("image/png", noisy), imageBlock("image/png", noisy))
	body := LimitClaudeRequestImages(payload)
	if claudeImageRequestSize(body) > claudeRequestBodyLimit {
		t.Fatalf("shared size %d still over budget", claudeImageRequestSize(body))
	}
	images := imagesOf(body)
	format, w, _ := imageDims(t, images[0])
	if format != "jpeg" || w != 1024 {
		t.Fatalf("oldest image = %s %dpx, want jpeg 1024", format, w)
	}
	if images[3].Get("source.data").String() != noisy {
		t.Fatal("newest image changed although older ones were enough")
	}
	if strings.Contains(string(body), claudeOmittedImagePlaceholder) {
		t.Fatal("placeholder used although shrinking was enough")
	}
}

func TestLimitClaudeImages_OversizeBodyOmitsOldestButNeverNewest(t *testing.T) {
	gif := fakeGIF(9 << 20)
	payload := claudeImagePayload(
		`{"type":"image","source":{"type":"base64","media_type":"image/gif","data":"`+gif+`"},"cache_control":{"type":"ephemeral"}}`,
		imageBlock("image/gif", gif), imageBlock("image/gif", gif), imageBlock("image/gif", gif))
	body := LimitClaudeRequestImages(payload)
	content := gjson.GetBytes(body, "messages.0.content").Array()
	if content[1].Get("text").String() != claudeOmittedImagePlaceholder || content[1].Get("cache_control.type").String() != "ephemeral" {
		t.Fatalf("oldest = %s", content[1].Raw)
	}
	if content[4].Get("type").String() != "image" {
		t.Fatal("newest image was omitted")
	}
	if claudeImageRequestSize(body) > claudeRequestBodyLimit {
		t.Fatalf("still over budget: %d", claudeImageRequestSize(body))
	}
	// A later turn with more history keeps degrading from the oldest.
	if again := LimitClaudeRequestImages(body); !bytes.Equal(again, body) {
		t.Fatal("result is not stable when applied again")
	}
}

func TestValidateClaudeImageLimits_Boundaries(t *testing.T) {
	if err := ValidateClaudeImageLimits(make([]byte, claudeRequestBodyLimit)); err != nil {
		t.Fatalf("body at the limit rejected: %v", err)
	}
	err := ValidateClaudeImageLimits(make([]byte, claudeRequestBodyLimit+1))
	var scoped interface{ IsRequestScoped() bool }
	var status interface{ StatusCode() int }
	if err == nil || !errors.As(err, &scoped) || !errors.As(err, &status) || status.StatusCode() != http.StatusBadRequest {
		t.Fatalf("err = %v, want request-scoped 400", err)
	}
	if !strings.Contains(err.Error(), fmt.Sprint(claudeRequestBodyLimit+1)) || !strings.Contains(err.Error(), fmt.Sprint(claudeRequestBodyLimit)) {
		t.Fatalf("message lacks size and limit: %v", err)
	}
}

func testJPEG(t testing.TB, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 20, 120, 220, 255
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestLimitClaudeImages_JPEGInputStaysJPEG(t *testing.T) {
	body := LimitClaudeRequestImages(claudeImagePayload(imageBlock("image/jpeg", testJPEG(t, 8200, 40))))
	format, w, _ := imageDims(t, imagesOf(body)[0])
	if format != "jpeg" || w != 8000 {
		t.Fatalf("got %s %d px, want jpeg 8000", format, w)
	}
	if got := gjson.GetBytes(body, "messages.0.content.1.source.media_type").String(); got != "image/jpeg" {
		t.Fatalf("media_type = %s", got)
	}
	small := claudeImagePayload(imageBlock("image/jpeg", testJPEG(t, 300, 200)))
	if got := LimitClaudeRequestImages(small); !bytes.Equal(got, small) {
		t.Fatal("JPEG in limits was modified")
	}
}

// Later turns: the history grows and a new newest image arrives. Degradation
// continues from the oldest image, the new newest one is kept, and an image
// omitted inside a tool_result becomes a text block there.
func TestLimitClaudeImages_OversizeHistoryGrowsAcrossTurns(t *testing.T) {
	gif := fakeGIF(9 << 20)
	turn := func(images int) []byte {
		msgs := []string{}
		for i := 0; i < images; i++ {
			msgs = append(msgs,
				`{"role":"assistant","content":[{"type":"tool_use","id":"tu`+fmt.Sprint(i)+`","name":"screenshot","input":{}}]}`,
				`{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu`+fmt.Sprint(i)+`","content":[`+imageBlock("image/gif", gif)+`]}]}`)
		}
		return []byte(`{"model":"claude-opus-5-5","max_tokens":64,"messages":[{"role":"user","content":"start"},` + strings.Join(msgs, ",") + `]}`)
	}
	placeholders := func(body []byte) []int {
		var at []int
		for i, ref := range gjson.GetBytes(body, "messages").Array() {
			for _, block := range ref.Get("content").Array() {
				for _, inner := range block.Get("content").Array() {
					if inner.Get("type").String() == "text" && inner.Get("text").String() == claudeOmittedImagePlaceholder {
						at = append(at, i)
					}
				}
			}
		}
		return at
	}
	three := LimitClaudeRequestImages(turn(3))
	four := LimitClaudeRequestImages(turn(4))
	if got := placeholders(three); len(got) != 0 {
		t.Fatalf("3 images (27 MB) need no omission, got placeholders at %v", got)
	}
	got := placeholders(four)
	if len(got) != 1 || got[0] != 2 {
		t.Fatalf("4 images: placeholders at %v, want only the oldest tool_result (message 2)", got)
	}
	images := imagesOf(four)
	if len(images) != 3 || images[len(images)-1].Get("source.data").String() != gif {
		t.Fatal("the new newest image must stay")
	}
	if ValidateClaudeImageLimits(four) != nil {
		t.Fatal("result does not fit")
	}
}

// pngHeaderOnly returns a PNG whose header claims w x h. Only DecodeConfig can
// read it, which is all the guard must ever do with such an image.
func pngHeaderOnly(t *testing.T, w, h int) string {
	t.Helper()
	raw, _ := base64.StdEncoding.DecodeString(testPNG(t, 1, 1, false, 255))
	binary.BigEndian.PutUint32(raw[16:20], uint32(w))
	binary.BigEndian.PutUint32(raw[20:24], uint32(h))
	binary.BigEndian.PutUint32(raw[29:33], crc32.ChecksumIEEE(raw[12:29]))
	return base64.StdEncoding.EncodeToString(raw)
}

func TestLimitClaudeImages_RasterTooLargeToDecodeIsLeftForValidation(t *testing.T) {
	payload := claudeImagePayload(imageBlock("image/png", pngHeaderOnly(t, 30000, 30000)))
	if got := LimitClaudeRequestImages(payload); !bytes.Equal(got, payload) {
		t.Fatal("an image above the decode bound must not be decoded or changed")
	}
	if err := ValidateClaudeImageLimits(payload); err == nil || !strings.Contains(err.Error(), "30000x30000") {
		t.Fatalf("err = %v, want local rejection naming the size", err)
	}
}

// Large raster: 8100 x 8100 PNG (above 8000 px) decoded and scaled once.
func BenchmarkLimitClaudeImages_LargeRaster(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 8100, 8100))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(i>>10), uint8(i>>4), 90, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatal(err)
	}
	payload := claudeImagePayload(imageBlock("image/png", base64.StdEncoding.EncodeToString(buf.Bytes())))
	b.ReportMetric(float64(len(payload)), "body_bytes")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out := LimitClaudeRequestImages(payload)
		if bytes.Equal(out, payload) {
			b.Fatal("image was not scaled")
		}
	}
}

// fitGIFPayload returns a request with an old and a newest GIF whose byte
// length is exactly claudeRequestBodyLimit+delta. stream is the raw value of
// the stream field or "" for none.
func fitGIFPayload(t testing.TB, stream string, delta int) []byte {
	t.Helper()
	newest := imageBlock("image/gif", fakeGIF(1<<20))
	streamField := ""
	if stream != "" {
		streamField = `"stream":` + stream + `,`
	}
	build := func(oldLen int) []byte {
		return []byte(`{"model":"claude-opus-5-5","max_tokens":64,` + streamField + `"messages":[{"role":"user","content":[{"type":"text","text":"describe"},` +
			imageBlock("image/gif", fakeGIF(oldLen)) + `,` + newest + `]}]}`)
	}
	base := build(1 << 20)
	payload := build((1 << 20) + claudeRequestBodyLimit + delta - len(base))
	if len(payload) != claudeRequestBodyLimit+delta {
		t.Fatalf("fixture len %d, want %d", len(payload), claudeRequestBodyLimit+delta)
	}
	return payload
}

func TestLimitClaudeImages_R3BoundaryStartsOnlyAboveLimit(t *testing.T) {
	omittedOldest := func(body []byte) bool {
		content := gjson.GetBytes(body, "messages.0.content").Array()
		if content[2].Get("type").String() != "image" {
			t.Fatal("newest image lost")
		}
		return content[1].Get("text").String() == claudeOmittedImagePlaceholder
	}
	for _, stream := range []string{"", "true", "false"} {
		streamLen := 0
		if stream != "" {
			streamLen = len(`,"stream":` + stream)
		}
		t.Run("stream="+stream, func(t *testing.T) {
			// A body that fits is never reduced, whatever its stream field.
			for _, delta := range []int{-1, 0} {
				payload := fitGIFPayload(t, stream, delta)
				if got := LimitClaudeRequestImages(payload); !bytes.Equal(got, payload) {
					t.Fatalf("delta %d: body within 32 MiB was modified", delta)
				}
			}
			// Over the limit by at most the stream field: left for U1.
			if stream != "" {
				payload := fitGIFPayload(t, stream, streamLen)
				if got := LimitClaudeRequestImages(payload); !bytes.Equal(got, payload) {
					t.Fatal("body over the limit only by its stream field was reduced")
				}
				if ValidateClaudeImageLimits(payload) == nil {
					t.Fatal("U1 did not reject the oversize body")
				}
			}
			// One byte more than that: the oldest image is replaced.
			if !omittedOldest(LimitClaudeRequestImages(fitGIFPayload(t, stream, streamLen+1))) {
				t.Fatal("oldest image not replaced above the limit")
			}
		})
	}
}

// Large raster inside a request of about 30 MiB: the image is scaled and the
// rest of the body is copied once.
func BenchmarkLimitClaudeImages_LargeRasterBody30MiB(b *testing.B) {
	img := image.NewNRGBA(image.Rect(0, 0, 8100, 8100))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(i>>10), uint8(i>>4), 90, 255
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		b.Fatal(err)
	}
	raster := imageBlock("image/png", base64.StdEncoding.EncodeToString(buf.Bytes()))
	filler := `{"type":"text","text":"` + strings.Repeat("a", (30<<20)-len(raster)) + `"}`
	payload := claudeImagePayload(raster, filler)
	b.ReportMetric(float64(len(payload)), "body_bytes")
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if out := LimitClaudeRequestImages(payload); bytes.Equal(out, payload) {
			b.Fatal("image was not scaled")
		}
	}
}
