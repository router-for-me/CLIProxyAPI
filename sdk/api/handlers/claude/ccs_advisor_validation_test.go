package claude

import (
	"net/http"
	"strings"
	"testing"
)

func TestCCSAdvisorInvalidDeclarationReturnsBadRequest(t *testing.T) {
	t.Setenv("CCS_CODEX_ADVISOR_MODEL", ccsAdvisor)
	for _, tc := range []struct{ name, tools string }{
		{"duplicate", nativeAdvisor + "," + nativeAdvisor},
		{"zero limit", strings.Replace(nativeAdvisor, "}", `,"max_uses":0}`, 1)},
		{"wrong limit type", strings.Replace(nativeAdvisor, "}", `,"max_uses":"two"}`, 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := ccsHandler(t, "codex", ccsWorker, ccsAdvisor)
			w := ccsRequest(h, strings.Replace(ccsBody, nativeAdvisor, tc.tools, 1), false)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("want 400; got %d: %s", w.Code, w.Body)
			}
		})
	}
}
