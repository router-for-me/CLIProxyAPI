package api

import (
	"bytes"
	"testing"
)

func TestInjectKiroCreditScriptBeforeBody(t *testing.T) {
	html := []byte("<html><body><div id=\"root\"></div></body></html>")

	out := injectKiroCreditScript(html)

	if !bytes.Contains(out, []byte(kiroCreditScriptMarker)) {
		t.Fatalf("injected html missing marker: %s", string(out))
	}
	if bytes.Index(out, []byte(kiroCreditScriptMarker)) > bytes.Index(out, []byte("</body>")) {
		t.Fatalf("script was not injected before closing body: %s", string(out))
	}
}

func TestInjectKiroCreditScriptIdempotent(t *testing.T) {
	html := injectKiroCreditScript([]byte("<html><body></body></html>"))

	out := injectKiroCreditScript(html)

	if bytes.Count(out, []byte(kiroCreditScriptMarker)) != 1 {
		t.Fatalf("script marker count = %d, want 1", bytes.Count(out, []byte(kiroCreditScriptMarker)))
	}
}
