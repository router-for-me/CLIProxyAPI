package executor

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func makeBase64Image(t *testing.T, w, h int, format string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 128, A: 255})
		}
	}
	var buf bytes.Buffer
	switch format {
	case "png":
		if err := png.Encode(&buf, img); err != nil {
			t.Fatalf("png encode: %v", err)
		}
	case "jpeg":
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
			t.Fatalf("jpeg encode: %v", err)
		}
	default:
		t.Fatalf("unknown format: %s", format)
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestMaybeCompressImage_SmallImage_NoChange(t *testing.T) {
	b64 := makeBase64Image(t, 100, 100, "png")
	_, _, changed := maybeCompressImage(b64)
	if changed {
		t.Fatal("small image should not be modified")
	}
}

func TestMaybeCompressImage_OversizedDimensions(t *testing.T) {
	// 8100px exceeds claudeMaxImageDim (7680)
	b64 := makeBase64Image(t, 8100, 100, "png")
	compressed, newType, changed := maybeCompressImage(b64)
	if !changed {
		t.Fatal("oversized image should be compressed")
	}
	if newType != "image/jpeg" {
		t.Fatalf("expected image/jpeg output, got %q", newType)
	}
	// Verify the result is decodable and within bounds
	raw, err := base64.StdEncoding.DecodeString(compressed)
	if err != nil {
		t.Fatalf("decode compressed: %v", err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	if img.Bounds().Dx() > claudeMaxImageDim {
		t.Fatalf("compressed width %d still exceeds limit %d", img.Bounds().Dx(), claudeMaxImageDim)
	}
}

func TestMaybeCompressImage_InvalidBase64(t *testing.T) {
	_, _, changed := maybeCompressImage("not-valid-base64!!!")
	if changed {
		t.Fatal("invalid base64 should return unchanged")
	}
}

func TestScaleToFit(t *testing.T) {
	cases := []struct {
		w, h, max  int
		wantW, wantH int
	}{
		{100, 100, 200, 100, 100},     // already fits
		{400, 200, 200, 200, 100},     // landscape
		{200, 400, 200, 100, 200},     // portrait
		{8000, 8000, 7680, 7680, 7680}, // square at limit
	}
	for _, tc := range cases {
		gotW, gotH := scaleToFit(tc.w, tc.h, tc.max)
		if gotW != tc.wantW || gotH != tc.wantH {
			t.Errorf("scaleToFit(%d,%d,%d) = (%d,%d), want (%d,%d)",
				tc.w, tc.h, tc.max, gotW, gotH, tc.wantW, tc.wantH)
		}
	}
}

func TestCompressImagesInPayload_NoMessages(t *testing.T) {
	payload := []byte(`{"model":"claude-sonnet-4","system":"hello"}`)
	got := compressImagesInPayload(payload)
	if string(got) != string(payload) {
		t.Fatal("payload without messages should be unchanged")
	}
}

func TestCompressImagesInPayload_TextOnly(t *testing.T) {
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}]}`)
	got := compressImagesInPayload(payload)
	if string(got) != string(payload) {
		t.Fatal("text-only payload should be unchanged")
	}
}

func TestCompressImagesInPayload_SmallImageUnchanged(t *testing.T) {
	b64 := makeBase64Image(t, 50, 50, "png")
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}`)
	got := compressImagesInPayload(payload)
	if string(got) != string(payload) {
		t.Fatal("small image payload should be unchanged")
	}
}

func TestCompressImagesInPayload_OversizedImageCompressed(t *testing.T) {
	b64 := makeBase64Image(t, 8100, 100, "png")
	payload := []byte(`{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + b64 + `"}}]}]}`)
	got := compressImagesInPayload(payload)
	if string(got) == string(payload) {
		t.Fatal("oversized image should have been modified")
	}
}
