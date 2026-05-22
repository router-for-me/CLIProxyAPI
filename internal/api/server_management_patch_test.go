package api

import (
	"strings"
	"testing"
)

func TestInjectAuthFilesWarningFilterPatch_InsertsBeforeBodyClose(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	out := injectAuthFilesWarningFilterPatch(input)
	result := string(out)

	if !strings.Contains(result, "__cpa_auth_warning_filter_patch__") {
		t.Fatal("expected warning filter patch marker in output")
	}

	idxBody := strings.LastIndex(result, "</body>")
	idxMarker := strings.Index(result, "__cpa_auth_warning_filter_patch__")
	if idxBody < 0 || idxMarker < 0 || idxMarker > idxBody {
		t.Fatal("expected patch script to be injected before </body>")
	}
}

func TestInjectAuthFilesWarningFilterPatch_OnlyInjectsOnce(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	first := injectAuthFilesWarningFilterPatch(input)
	second := injectAuthFilesWarningFilterPatch(first)
	result := string(second)

	if strings.Count(result, "__cpa_auth_warning_filter_patch__") != 1 {
		t.Fatal("expected patch marker to appear exactly once")
	}
}

func TestInjectAuthFilesWarningFilterPatch_AppendsWhenBodyMissing(t *testing.T) {
	input := []byte("<html><div>content</div></html>")
	out := injectAuthFilesWarningFilterPatch(input)
	result := string(out)

	if !strings.Contains(result, "__cpa_auth_warning_filter_patch__") {
		t.Fatal("expected warning filter patch marker in output")
	}
	if !strings.HasSuffix(result, "</script>") {
		t.Fatal("expected patch script appended to document end when </body> is missing")
	}
}

func TestInjectAuthFilesWarningFilterPatch_UsesHeaderMountingInsteadOfBottomFloating(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	out := injectAuthFilesWarningFilterPatch(input)
	result := string(out)

	for _, needle := range []string{
		"findAuthFilesCardHeader",
		"findHeaderActionsContainer",
		"mountBeforeHeaderActions",
		"MutationObserver",
		"floating-fallback",
		"insertBefore",
		"cpa-auth-clean-401-button",
		"/v0/management/auth-files/clean-codex-401",
	} {
		if !strings.Contains(result, needle) {
			t.Fatalf("expected auth warning filter patch to include %q", needle)
		}
	}

	for _, needle := range []string{
		"position:fixed;right:16px;bottom:16px",
		"mountNextToHeading",
		"Health Filter",
		"cpa-auth-warning-filter-select",
		"cpa_auth_warning_filter_mode",
		"health_status",
		"rewriteAuthFilesURL",
		"getMode",
		"setMode",
	} {
		if strings.Contains(result, needle) {
			t.Fatalf("expected auth warning filter patch to avoid %q", needle)
		}
	}
}

func TestInjectAuthFilesWarningFilterPatch_SanitizesHTMLErrorResponses(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	out := injectAuthFilesWarningFilterPatch(input)
	result := string(out)

	for _, needle := range []string{
		"looksLikeHTMLDocument",
		"buildCleanerErrorMessage",
		"text/html",
		"Cleanup request timed out. Please try again.",
		"Cleanup request returned an HTML error page. Please try again.",
		"\\u6e05\\u7406\\u8bf7\\u6c42\\u8d85\\u65f6",
	} {
		if !strings.Contains(result, needle) {
			t.Fatalf("expected auth warning filter patch to include %q", needle)
		}
	}
}

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

func TestManagementPatchChain_ContainsBothMarkers(t *testing.T) {
	input := []byte("<html><body><div>content</div></body></html>")
	out := injectAuthFilesWarningFilterPatch(input)
	out = injectModelPriceDropdownClipPatch(out)
	result := string(out)

	if !strings.Contains(result, "__cpa_auth_warning_filter_patch__") {
		t.Fatal("expected auth warning patch marker in chained output")
	}
	if !strings.Contains(result, "__cpa_model_price_dropdown_clip_patch__") {
		t.Fatal("expected model price dropdown patch marker in chained output")
	}
}
