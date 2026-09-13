package claudemaster

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestProxyDiagnosticsContainOnlyCountersAndStop(t *testing.T) {
	proxy, _, _ := proxyTestStart(t, http.NotFoundHandler(), nil)
	var output bytes.Buffer
	stop := startProxyDiagnostics(proxy, &output)
	stop()
	stop()
	var fields map[string]uint64
	if err := json.Unmarshal(output.Bytes(), &fields); err != nil {
		t.Fatalf("diagnostics are not a single numeric snapshot: %v", err)
	}
	if len(fields) != 7 || strings.Contains(output.String(), proxy.URL()) {
		t.Fatal("unexpected diagnostic fields or private proxy URL")
	}
	startProxyDiagnostics(proxy, nil)()
}
