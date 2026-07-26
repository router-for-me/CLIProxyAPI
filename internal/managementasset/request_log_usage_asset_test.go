package managementasset

import (
	"bytes"
	"strings"
	"testing"
)

func TestRequestLogUsageScriptReturnsCopy(t *testing.T) {
	first := RequestLogUsageScript()
	if len(first) == 0 {
		t.Fatal("RequestLogUsageScript() returned an empty asset")
	}
	originalFirstByte := first[0]
	first[0] ^= 0xff
	second := RequestLogUsageScript()
	if second[0] != originalFirstByte {
		t.Fatal("RequestLogUsageScript() exposed mutable embedded storage")
	}
	if bytes.Contains(second, []byte("localStorage")) {
		t.Fatal("request log usage script must not read credentials from localStorage")
	}
	if bytes.Contains(second, []byte("sessionStorage")) {
		t.Fatal("request log usage script must not read credentials from sessionStorage")
	}
	if !bytes.Contains(second, []byte("/v0/management/request-log-usage")) {
		t.Fatal("request log usage script is missing its management endpoint")
	}
	for _, required := range [][]byte{
		[]byte("XMLHttpRequest"),
		[]byte("window.fetch"),
		[]byte("Authorization"),
		[]byte("X-Management-Key"),
		[]byte("Key 日志用量"),
		[]byte("每日总量与每人用量"),
		[]byte("payload.days"),
		[]byte("clearCapturedAuth"),
		[]byte("'unauthorized'"),
		[]byte("'hashchange'"),
		[]byte("cache: 'no-store'"),
		[]byte("grok45"),
		[]byte("return 'Grok'"),
		[]byte("viewState"),
		[]byte("buildPager"),
		[]byte("上一页"),
		[]byte("下一页"),
		[]byte("搜索 Key 名称"),
		[]byte("slicePage"),
	} {
		if !bytes.Contains(second, required) {
			t.Fatalf("request log usage script is missing %q", required)
		}
	}
}

func TestInjectRequestLogUsageScript(t *testing.T) {
	html := []byte(`<!doctype html><html><head><script type="module" src="/app.js"></script></head><body><main>app</main></body></html>`)
	injected := InjectRequestLogUsageScript(html)
	tag := `<script src="` + RequestLogUsageScriptPath + `"></script>`
	text := string(injected)

	if strings.Count(text, RequestLogUsageScriptPath) != 1 {
		t.Fatalf("script path count = %d, want 1: %s", strings.Count(text, RequestLogUsageScriptPath), text)
	}
	tagIndex := strings.Index(text, tag)
	moduleIndex := strings.Index(text, `<script type="module"`)
	bodyIndex := strings.LastIndex(strings.ToLower(text), "</body>")
	if tagIndex < 0 || moduleIndex < 0 || tagIndex >= moduleIndex || bodyIndex < 0 || tagIndex >= bodyIndex {
		t.Fatalf("script was not inserted before control panel scripts: %s", text)
	}
	if strings.Contains(tag, "async") || strings.Contains(tag, "defer") || strings.Contains(tag, `type="module"`) {
		t.Fatalf("injected script must be parser-blocking classic JavaScript: %s", tag)
	}

	twice := InjectRequestLogUsageScript(injected)
	if !bytes.Equal(twice, injected) {
		t.Fatal("InjectRequestLogUsageScript() is not idempotent")
	}
}

func TestInjectRequestLogUsageScriptHandlesUppercaseBody(t *testing.T) {
	html := []byte(`<HTML><BODY>content</BODY></HTML>`)
	got := string(InjectRequestLogUsageScript(html))
	want := `content<script src="` + RequestLogUsageScriptPath + `"></script></BODY>`
	if !strings.Contains(got, want) {
		t.Fatalf("uppercase body injection = %s, want substring %s", got, want)
	}
}

func TestInjectRequestLogUsageScriptWithoutBodyAppendsAsset(t *testing.T) {
	html := []byte(`<main>fragment</main>`)
	got := string(InjectRequestLogUsageScript(html))
	want := string(html) + `<script src="` + RequestLogUsageScriptPath + `"></script>`
	if got != want {
		t.Fatalf("fragment injection = %s, want %s", got, want)
	}
}
