package api

import (
	"strings"
	"testing"
)

func TestInjectModelPriceDropdownClipPatch_InsertsBeforeBodyClose(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	out := injectModelPriceDropdownClipPatch(input)
	result := string(out)

	if !strings.Contains(result, "__cpa_model_price_dropdown_clip_patch__") {
		t.Fatal("expected model price dropdown clip patch marker in output")
	}

	idxBody := strings.LastIndex(result, "</body>")
	idxMarker := strings.Index(result, "__cpa_model_price_dropdown_clip_patch__")
	if idxBody < 0 || idxMarker < 0 || idxMarker > idxBody {
		t.Fatal("expected model price dropdown clip patch to be injected before </body>")
	}
}

func TestInjectModelPriceDropdownClipPatch_OnlyInjectsOnce(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	first := injectModelPriceDropdownClipPatch(input)
	second := injectModelPriceDropdownClipPatch(first)
	result := string(second)

	if strings.Count(result, "__cpa_model_price_dropdown_clip_patch__") != 1 {
		t.Fatal("expected model price dropdown clip patch marker to appear exactly once")
	}
}

func TestInjectModelPriceDropdownClipPatch_AppendsWhenBodyMissing(t *testing.T) {
	input := []byte("<html><div>content</div></html>")
	out := injectModelPriceDropdownClipPatch(input)
	result := string(out)

	if !strings.Contains(result, "__cpa_model_price_dropdown_clip_patch__") {
		t.Fatal("expected model price dropdown clip patch marker in output")
	}
	if !strings.HasSuffix(result, "</script>") {
		t.Fatal("expected model price dropdown clip patch appended to document end when </body> is missing")
	}
}

func TestInjectModelPriceDropdownClipPatch_IncludesSelectModelFallbackLabels(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	out := injectModelPriceDropdownClipPatch(input)
	result := string(out)

	for _, needle := range []string{
		"\\u9009\\u62e9\\u6a21\\u578b",
		"select model",
		"getBoundingClientRect",
	} {
		if !strings.Contains(result, needle) {
			t.Fatalf("expected model price dropdown clip patch to include fallback label %q", needle)
		}
	}
}

func TestInjectModelPriceDropdownClipPatch_DoesNotRegisterGlobalTriggerHooks(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	out := injectModelPriceDropdownClipPatch(input)
	result := string(out)

	for _, needle := range []string{
		"pointerdown",
		"focusin",
		"activeTrigger",
		"data-radix-popper-content-wrapper",
		"OVERLAY_SELECTOR",
	} {
		if strings.Contains(result, needle) {
			t.Fatalf("expected model price dropdown clip patch to avoid global trigger hook %q", needle)
		}
	}
}
