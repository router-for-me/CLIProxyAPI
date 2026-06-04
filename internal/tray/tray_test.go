package tray

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestOptionsURLs(t *testing.T) {
	opts := Options{Port: 8317}

	if got, want := opts.baseURL(), "http://127.0.0.1:8317"; got != want {
		t.Fatalf("baseURL() = %q, want %q", got, want)
	}
	if got, want := opts.managementURL(), "http://127.0.0.1:8317/management.html"; got != want {
		t.Fatalf("managementURL() = %q, want %q", got, want)
	}
}

func TestOptionsManagementPasswordTrimsWhitespace(t *testing.T) {
	opts := Options{ManagementPassword: "  secret  "}

	if got, want := opts.managementPassword(), "secret"; got != want {
		t.Fatalf("managementPassword() = %q, want %q", got, want)
	}
}

func TestTrayIconPNG(t *testing.T) {
	data, err := trayIconPNG("")
	if err != nil {
		t.Fatalf("trayIconPNG() error = %v", err)
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("trayIconPNG() returned invalid PNG: %v", err)
	}

	if got := img.Bounds().Dx(); got != iconSize {
		t.Fatalf("icon width = %d, want %d", got, iconSize)
	}
	if got := img.Bounds().Dy(); got != iconSize {
		t.Fatalf("icon height = %d, want %d", got, iconSize)
	}

	_, _, _, alpha := img.At(0, 0).RGBA()
	if alpha != 0 {
		t.Fatalf("top-left corner alpha = %d, want transparent rounded corner", alpha)
	}
}

func TestTrayIconPNGUsesManagementFavicon(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 80, 40))
	fillRect(src, 0, 0, 80, 40, color.RGBA{R: 16, G: 72, B: 210, A: 255})

	var jpegBuf bytes.Buffer
	if err := jpeg.Encode(&jpegBuf, src, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatalf("encode test favicon: %v", err)
	}

	encoded := base64.StdEncoding.EncodeToString(jpegBuf.Bytes())
	html := []byte("<script>const favicon = `data:image/jpeg;base64," + encoded + "`;</script>")
	assetPath := filepath.Join(t.TempDir(), "management.html")
	if err := os.WriteFile(assetPath, html, 0o644); err != nil {
		t.Fatalf("write test management asset: %v", err)
	}

	data, err := trayIconPNG(assetPath)
	if err != nil {
		t.Fatalf("trayIconPNG() error = %v", err)
	}

	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("trayIconPNG() returned invalid PNG: %v", err)
	}
	if got := img.Bounds().Dx(); got != iconSize {
		t.Fatalf("icon width = %d, want %d", got, iconSize)
	}
	if got := img.Bounds().Dy(); got != iconSize {
		t.Fatalf("icon height = %d, want %d", got, iconSize)
	}

	r, g, b, _ := img.At(iconSize/2, iconSize/2).RGBA()
	if b <= r || b <= g {
		t.Fatalf("center pixel = (%d,%d,%d), want favicon-derived blue icon", r, g, b)
	}

	_, _, _, alpha := img.At(0, 0).RGBA()
	if alpha != 0 {
		t.Fatalf("top-left corner alpha = %d, want transparent rounded corner", alpha)
	}
}
