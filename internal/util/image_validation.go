package util

import (
	"fmt"
	"strings"

	"github.com/tidwall/gjson"
)

// supportedImageMIMETypes is the whitelist of image MIME types accepted by
// upstream APIs (Gemini/Antigravity). Unsupported types like image/svg+xml
// are rejected early to avoid wasted upstream API calls.
var supportedImageMIMETypes = map[string]bool{
	"image/jpeg": true,
	"image/png":  true,
	"image/gif":  true,
	"image/webp": true,
}

// ValidateImageMIMEType checks if the given MIME type is in the supported whitelist.
// Returns nil if valid, or an error describing which types are supported.
func ValidateImageMIMEType(mimeType string) error {
	lower := strings.ToLower(strings.TrimSpace(mimeType))
	if lower == "" {
		return nil // no MIME type specified, let upstream decide
	}
	if supportedImageMIMETypes[lower] {
		return nil
	}
	return fmt.Errorf("unsupported image type: %s. Supported types: image/jpeg, image/png, image/gif, image/webp", mimeType)
}

// ValidatePayloadImages walks a translated Antigravity request payload and
// validates all inline image MIME types. Returns an error for the first
// unsupported image found, or nil if all images are valid.
func ValidatePayloadImages(payload []byte) error {
	// Walk request.contents[].parts[].inlineData.mimeType
	contents := gjson.GetBytes(payload, "request.contents")
	if !contents.IsArray() {
		return nil
	}
	for _, content := range contents.Array() {
		parts := content.Get("parts")
		if !parts.IsArray() {
			continue
		}
		for _, part := range parts.Array() {
			inlineData := part.Get("inlineData")
			if !inlineData.Exists() {
				continue
			}
			mimeType := inlineData.Get("mimeType").String()
			if err := ValidateImageMIMEType(mimeType); err != nil {
				return err
			}
		}
	}
	return nil
}
