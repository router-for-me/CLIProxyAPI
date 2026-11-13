package openai

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestHTTPSubmissionEmptyAcceptedNonStreamDoesNotReplay(t *testing.T) {
	var accepted atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, errRead := io.Copy(io.Discard, r.Body); errRead != nil {
			t.Error(errRead)
			return
		}
		accepted.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	response := submissionRequest(t, upstream.URL, false, nil)
	body := response.Body.String()
	t.Logf("accepted=%d status=%d retry=%q body=%s", accepted.Load(), response.Code, response.Header().Get("x-should-retry"), body)
	if accepted.Load() != 1 {
		t.Errorf("empty accepted response replayed: submissions=%d want1", accepted.Load())
	}
	if response.Code != http.StatusBadGateway {
		t.Errorf("status=%d want502", response.Code)
	}
	for _, field := range []string{`"code":"upstream_outcome_unknown"`, `"retryable":false`} {
		if !strings.Contains(body, field) {
			t.Errorf("missing %s in %s", field, body)
		}
	}
	if response.Header().Get("x-should-retry") != "false" {
		t.Error("missing nonretryable header")
	}
}
