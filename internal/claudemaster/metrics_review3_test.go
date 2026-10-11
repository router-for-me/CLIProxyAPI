package claudemaster

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"encoding/json"

	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func freshBounds(t *testing.T) {
	t.Helper()
	oldModels, oldAccounts := models, accounts
	models, accounts = newBoundedValues(maxDistinctModels), newBoundedValues(maxDistinctAccounts)
	t.Cleanup(func() { models, accounts = oldModels, oldAccounts })
}

func requestWithModel(model string) *http.Request {
	body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"hi"}]}`, model)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Claude-Code-Session-Id", "model-test")
	return req.WithContext(coreexecutor.WithNativeClaudeProtocolHeaders(req.Context(), req.Header))
}

// The client chooses the model string. Whatever it sends, no series is made from it and it is not logged.
func TestAModelNameFromTheClientIsNeverUsedAsWritten(t *testing.T) {
	freshBounds(t)
	for raw, want := range map[string]string{
		"claude-opus-5":                        "claude-opus-5",
		"Claude-Opus-5":                        "claude-opus-5",
		"claude-sonnet-4-5-20250929":           "claude-sonnet-4-5-20250929",
		"anthropic.claude-3-5-sonnet":          "anthropic.claude-3-5-sonnet",
		"":                                     "unknown",
		"someone@example.com":                  "other",
		"claude opus 5":                        "other",
		"550e8400-e29b-41d4-a716-446655440000": "other",
		"sk-ant-api03-abcdefghijkl":            "other",
		strings.Repeat("a", 65):                "other",
	} {
		if got := modelLabel(raw); got != want {
			t.Errorf("modelLabel(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestOnlyABoundedNumberOfDistinctModelsAndAccountsEverBecomeSeries(t *testing.T) {
	freshBounds(t)
	seen := map[string]bool{}
	for i := 0; i < 500; i++ {
		seen[modelLabel(fmt.Sprintf("claude-model-%d", i))] = true
	}
	if len(seen) != maxDistinctModels+1 || !seen["other"] {
		t.Fatalf("500 distinct models gave %d labels, want %d plus other", len(seen), maxDistinctModels)
	}
	if modelLabel("claude-model-0") != "claude-model-0" {
		t.Fatal("a model already admitted must keep its own label")
	}
	startMetrics(t, nil)
	keys := map[string]bool{}
	for i := 0; i < 1000; i++ {
		body := fmt.Sprintf(`{"metadata":{"user_id":"user_x_account_%08d-1111-2222-3333-444444444444_session_s"}}`, i)
		keys[clientAccountKey([]byte(body))] = true
	}
	if len(keys) != maxDistinctAccounts+1 || !keys["other"] {
		t.Fatalf("1000 distinct accounts gave %d keys, want %d plus other", len(keys), maxDistinctAccounts)
	}
}

func TestARequestWithASecretLookingModelLeavesNoTraceInMetricsOrLogs(t *testing.T) {
	freshBounds(t)
	reader := startMetrics(t, nil)
	logs := captureLogs(t, "debug")
	backend := newBackendNativeResponseFixture(t)
	backend.seriesSelector.names = map[string]string{backend.authIDs[0]: "claude-connor"}
	backend.manager.SetRoundTripperProvider(&upstream{})
	for _, model := range []string{"ejc3@example.com", "my-secret-project-name with spaces"} {
		_ = serve(t, backend, requestWithModel(model))
	}
	rm := collect(t, reader)
	encoded, _ := json.Marshal(rm)
	for _, secret := range []string{"ejc3@example.com", "secret-project", "spaces"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("%q reached the metrics", secret)
		}
		if strings.Contains(logs.String(), secret) {
			t.Fatalf("%q reached the log:\n%s", secret, logs.String())
		}
	}
	if got := sumOf(rm, "claude_master.inference.requests.by_model", map[string]string{"model": "other"}); got != 2 {
		t.Fatalf("inference.requests.by_model{model=other} = %d, want 2", got)
	}
}

// Anthropic's own compound windows (the 7d_oi family) keep their window; they are not folded into "all".
func TestCompoundRateLimitWindowsKeepTheirName(t *testing.T) {
	for header, want := range map[string][2]string{
		"unified-7d_oi-utilization": {"7d_oi", "utilization"},
		"unified-7d_oi-reset":       {"7d_oi", "reset"},
		"unified-5h_x2-remaining":   {"5h_x2", "remaining"},
		"unified-7d-utilization":    {"7d", "utilization"},
		"unified-status":            {"all", "status"},
		"unified-overage-status":    {"all", "overage_status"},
		"unified-7d_-utilization":   {"all", "7d__utilization"}, // not a window: a trailing underscore is junk
	} {
		if window, measure := splitLimitHeader(header); window != want[0] || measure != want[1] {
			t.Errorf("%s -> %s/%s, want %s/%s", header, window, measure, want[0], want[1])
		}
	}
}

// OpenFile's mode only applies to a file it creates; one that already exists and is world readable must
// still end up private, along with every rotated copy.
func TestAnExistingLooseLogFileAndItsRotationsBecomePrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "server.log")
	for _, name := range []string{path, path + ".1", path + ".2"} {
		if err := os.WriteFile(name, []byte("old line\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(name, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	file, err := openRotatingFile(path, 100, 3)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	mode := func(name string) os.FileMode {
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		return info.Mode().Perm()
	}
	if got := mode(path); got != 0o600 {
		t.Fatalf("the live log is %v, want 0600", got)
	}
	if _, err := file.Write([]byte(strings.Repeat("x", 120) + "\n")); err != nil { // forces a rotation
		t.Fatal(err)
	}
	for _, name := range []string{path, path + ".1", path + ".2", path + ".3"} {
		if _, err := os.Stat(name); err != nil {
			continue
		}
		if got := mode(name); got != 0o600 {
			t.Errorf("%s is %v, want 0600", filepath.Base(name), got)
		}
	}
}

// CloudWatch bills every distinct combination of a metric's attributes as its own custom metric. A metric that
// crosses profile, model, client and account explodes into hundreds. Each axis has its own projection and no
// metric carries more than three attributes; model never meets profile or client, and client never meets
// profile or model.
func TestNoMetricCrossesTheAxes(t *testing.T) {
	freshBounds(t)
	reader := startMetrics(t, map[string]string{testAccountUUID: "colton"})
	backend := newBackendNativeResponseFixture(t)
	backend.seriesSelector.names = map[string]string{backend.authIDs[0]: "claude-connor"}
	backend.manager.SetRoundTripperProvider(&upstream{status: 529, headers: http.Header{"Anthropic-Ratelimit-Unified-5h-Utilization": {"0.1"}}})
	_ = serve(t, backend, inferenceRequest(testAccountUUID, true))
	observeControl(httptest.NewRequest(http.MethodGet, "/v1/sessions", nil))
	rm := collect(t, reader)
	seen := 0
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			for _, set := range attributeSets(m) {
				seen++
				if len(set) > 3 {
					t.Errorf("%s carries %d attributes %v: at most 3, or the series count multiplies", m.Name, len(set), keysOf(set))
				}
				if _, ok := set["model"]; ok && (has(set, "profile") || has(set, "client") || has(set, "client_account")) {
					t.Errorf("%s crosses model with profile, client or account: %v", m.Name, keysOf(set))
				}
				if _, ok := set["client"]; ok && (has(set, "profile") || has(set, "model") || has(set, "status_class")) {
					t.Errorf("%s crosses client with profile, model or status: %v", m.Name, keysOf(set))
				}
			}
		}
	}
	if seen < 20 {
		t.Fatalf("only %d data points inspected: the test is not looking at the request metrics", seen)
	}
}

func has(set map[string]string, key string) bool { _, ok := set[key]; return ok }

func keysOf(set map[string]string) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	return out
}

// attributeSets is the attributes of every data point of a metric, whatever its type.
func attributeSets(m metricdata.Metrics) []map[string]string {
	var out []map[string]string
	switch data := m.Data.(type) {
	case metricdata.Sum[int64]:
		for _, p := range data.DataPoints {
			out = append(out, attributesOf(p.Attributes))
		}
	case metricdata.Gauge[int64]:
		for _, p := range data.DataPoints {
			out = append(out, attributesOf(p.Attributes))
		}
	case metricdata.Gauge[float64]:
		for _, p := range data.DataPoints {
			out = append(out, attributesOf(p.Attributes))
		}
	case metricdata.Histogram[float64]:
		for _, p := range data.DataPoints {
			out = append(out, attributesOf(p.Attributes))
		}
	case metricdata.Histogram[int64]:
		for _, p := range data.DataPoints {
			out = append(out, attributesOf(p.Attributes))
		}
	}
	return out
}
