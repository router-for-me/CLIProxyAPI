package util

import (
	"testing"
)

func TestValidateImageMIMEType_Supported(t *testing.T) {
	for _, mime := range []string{"image/jpeg", "image/png", "image/gif", "image/webp"} {
		if err := ValidateImageMIMEType(mime); err != nil {
			t.Errorf("expected %q to be supported, got error: %v", mime, err)
		}
	}
}

func TestValidateImageMIMEType_CaseInsensitive(t *testing.T) {
	if err := ValidateImageMIMEType("Image/JPEG"); err != nil {
		t.Errorf("expected case-insensitive match, got error: %v", err)
	}
}

func TestValidateImageMIMEType_Unsupported(t *testing.T) {
	for _, mime := range []string{"image/svg+xml", "image/bmp", "image/tiff", "application/pdf"} {
		if err := ValidateImageMIMEType(mime); err == nil {
			t.Errorf("expected %q to be rejected, got nil", mime)
		}
	}
}

func TestValidateImageMIMEType_Empty(t *testing.T) {
	if err := ValidateImageMIMEType(""); err != nil {
		t.Errorf("empty MIME type should pass through, got error: %v", err)
	}
}

func TestValidatePayloadImages_ValidPayload(t *testing.T) {
	payload := []byte(`{
		"request": {
			"contents": [{
				"parts": [
					{"text": "hello"},
					{"inlineData": {"mimeType": "image/png", "data": "abc123"}}
				]
			}]
		}
	}`)

	if err := ValidatePayloadImages(payload); err != nil {
		t.Errorf("expected valid payload, got error: %v", err)
	}
}

func TestValidatePayloadImages_SVGRejected(t *testing.T) {
	payload := []byte(`{
		"request": {
			"contents": [{
				"parts": [
					{"inlineData": {"mimeType": "image/svg+xml", "data": "abc123"}}
				]
			}]
		}
	}`)

	err := ValidatePayloadImages(payload)
	if err == nil {
		t.Fatal("expected SVG to be rejected")
	}
	if !contains([]string{"image/svg+xml"}, "") && err == nil {
		t.Fatal("expected error for SVG")
	}
}

func TestValidatePayloadImages_NoImages(t *testing.T) {
	payload := []byte(`{
		"request": {
			"contents": [{
				"parts": [{"text": "just text"}]
			}]
		}
	}`)

	if err := ValidatePayloadImages(payload); err != nil {
		t.Errorf("expected no error for text-only payload, got: %v", err)
	}
}

func TestValidatePayloadImages_EmptyPayload(t *testing.T) {
	if err := ValidatePayloadImages([]byte(`{}`)); err != nil {
		t.Errorf("expected no error for empty payload, got: %v", err)
	}
}
