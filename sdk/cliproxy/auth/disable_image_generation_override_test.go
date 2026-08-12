package auth

import "testing"

func TestDisableImageGenerationOverride(t *testing.T) {
	if (&Auth{}).DisableImageGenerationOverride() {
		t.Fatal("nil metadata must be false")
	}
	if (&Auth{Metadata: map[string]any{"disable_image_generation": false}}).DisableImageGenerationOverride() {
		t.Fatal("false must be treated as unset/false")
	}
	if !(&Auth{Metadata: map[string]any{"disable_image_generation": true}}).DisableImageGenerationOverride() {
		t.Fatal("snake_case true must be honored")
	}
	if !(&Auth{Metadata: map[string]any{"disable-image-generation": true}}).DisableImageGenerationOverride() {
		t.Fatal("kebab-case true must be honored")
	}
	if !(&Auth{Metadata: map[string]any{"disable_image_generation": "true"}}).DisableImageGenerationOverride() {
		t.Fatal("string true must be honored")
	}
}
