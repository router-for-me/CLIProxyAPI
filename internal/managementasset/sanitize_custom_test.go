package managementasset

import (
	"strings"
	"testing"
)

func TestCustomSanitizerRemovesSponsoredProviders(t *testing.T) {
	input := []byte("Vx=[`gemini`,`codex`,`claude`,`vertex`,`openaiCompatibility`,`apikeyFun`,`claudeApi`,`code0`];" +
		"fv=`apikeyFun`,pv=`APIKEY.FUN`,ib=`ClaudeAPI`,lb=`code0`,ub=`Code0`;" +
		"https://apikey.fun/register?aff=AKCPA https://console.claudeapi.com/agent/register/pJq9T52Fpugrhpgo https://code0.ai")
	got := string(SanitizeManagementHTML(input))
	for _, blocked := range []string{"apikeyFun", "claudeApi", "code0", "APIKEY.FUN", "ClaudeAPI", "Code0", "apikey.fun", "claudeapi.com", "code0.ai"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("sanitized HTML still contains %q: %s", blocked, got)
		}
	}
	if !strings.Contains(got, "openaiCompatibility") {
		t.Fatalf("sanitized HTML removed normal provider category: %s", got)
	}
}

func TestCustomSanitizerPreservesVertexIdentifiers(t *testing.T) {
	input := []byte("function createVertexArray(){return t.createVertexArray()} const label=`Vertex JSON Login`;")
	got := string(SanitizeManagementHTML(input))
	if !strings.Contains(got, "createVertexArray") || strings.Contains(got, "createArray") {
		t.Fatalf("sanitizer corrupted Vertex-related identifier: %s", got)
	}
	if strings.Contains(got, "Vertex JSON Login") {
		t.Fatalf("sanitizer left visible Vertex UI content: %s", got)
	}
}

func TestCustomSanitizerRemovesVisibleQuickStartEntrances(t *testing.T) {
	input := []byte("Vx=[`gemini`,`codex`,`claude`,`vertex`,`openaiCompatibility`,`apikeyFun`,`claudeApi`,`code0`];" +
		",...T?[]:[{label:e(`dashboard.quick_start_card`),value:e(`dashboard.quick_start_entry`),icon:(0,R.jsx)(zs,{size:24}),path:`/quick-start`,sublabel:e(`dashboard.quick_start_entry_desc`)}];" +
		"ce={path:`/quick-start`,label:se?pv:void 0,labelKey:se?void 0:`nav.quick_start`,metaKey:`nav_meta.quick_start`,icon:d2.quickStart};" +
		",...se?[]:[ce],...se?[ce]:[];/plugin-store")

	got := string(SanitizeManagementHTML(input))
	for _, blocked := range []string{"dashboard.quick_start_card", "ce={path:`/quick-start`", "...se?[]:[ce]", "...se?[ce]:[]"} {
		if strings.Contains(got, blocked) {
			t.Fatalf("sanitized HTML still contains visible quick-start structure %q: %s", blocked, got)
		}
	}
	if !strings.Contains(got, "Vx=[`gemini`,`codex`,`claude`,`openaiCompatibility`]") {
		t.Fatalf("sanitized HTML did not reduce the visible provider categories: %s", got)
	}
	if !strings.Contains(got, "ce=null") || !strings.Contains(got, "/plugin-store") {
		t.Fatalf("sanitized HTML removed expected navigation structure: %s", got)
	}
}
