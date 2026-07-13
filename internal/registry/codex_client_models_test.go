package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEmbeddedCodexClientModelsCatalogIsValid(t *testing.T) {
	data, revision := GetCodexClientModelsSnapshot()
	if revision == 0 {
		t.Fatal("embedded Codex client model catalog revision = 0, want non-zero")
	}
	if err := ValidateCodexClientModelsJSON(data); err != nil {
		t.Fatalf("embedded Codex client model catalog is invalid: %v", err)
	}

	data[0] ^= 0xff
	second, secondRevision := GetCodexClientModelsSnapshot()
	if secondRevision != revision {
		t.Fatalf("snapshot revision = %d, want %d", secondRevision, revision)
	}
	if err := ValidateCodexClientModelsJSON(second); err != nil {
		t.Fatalf("mutating returned snapshot changed stored catalog: %v", err)
	}
}

func TestValidateCodexClientModelsJSON(t *testing.T) {
	validDefault := testCodexClientModel("gpt-5.5", 1)
	validOther := testCodexClientModel("gpt-5.6-sol", 2)
	emptySlug := testCodexClientModel("gpt-5.5", 1)
	emptySlug["slug"] = ""
	missingField := testCodexClientModel("gpt-5.5", 1)
	delete(missingField, "base_instructions")
	wrongFieldType := testCodexClientModel("gpt-5.5", 1)
	wrongFieldType["context_window"] = "372000"
	unsupportedDefault := testCodexClientModel("gpt-5.5", 1)
	unsupportedDefault["default_reasoning_level"] = "high"

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "malformed", raw: []byte(`{"models":`)},
		{name: "empty", raw: []byte(`{"models":[]}`)},
		{name: "empty slug", raw: testCodexClientCatalog(t, emptySlug)},
		{name: "duplicate slug", raw: testCodexClientCatalog(t, validDefault, validDefault)},
		{name: "missing default", raw: testCodexClientCatalog(t, validOther)},
		{name: "missing required field", raw: testCodexClientCatalog(t, missingField)},
		{name: "wrong required field type", raw: testCodexClientCatalog(t, wrongFieldType)},
		{name: "default reasoning level not supported", raw: testCodexClientCatalog(t, unsupportedDefault)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateCodexClientModelsJSON(tt.raw); err == nil {
				t.Fatal("ValidateCodexClientModelsJSON() error = nil, want error")
			}
		})
	}

	valid := testCodexClientCatalog(t, validDefault, validOther)
	if err := ValidateCodexClientModelsJSON(valid); err != nil {
		t.Fatalf("valid catalog rejected: %v", err)
	}
}

func TestLoadCodexClientModelsRejectsInvalidWithoutReplacing(t *testing.T) {
	original, _ := GetCodexClientModelsSnapshot()
	t.Cleanup(func() {
		if _, err := loadCodexClientModelsFromBytes(original, "test cleanup"); err != nil {
			t.Fatalf("restore original catalog: %v", err)
		}
	})

	valid := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	changed, err := loadCodexClientModelsFromBytes(valid, "test")
	if err != nil {
		t.Fatalf("load valid catalog: %v", err)
	}
	if !changed {
		t.Fatal("load valid catalog changed = false, want true")
	}
	beforeInvalid, revision := GetCodexClientModelsSnapshot()

	if _, err = loadCodexClientModelsFromBytes([]byte(`{"models":[]}`), "test invalid"); err == nil {
		t.Fatal("load invalid catalog error = nil, want error")
	}
	afterInvalid, afterRevision := GetCodexClientModelsSnapshot()
	if string(afterInvalid) != string(beforeInvalid) {
		t.Fatal("invalid catalog replaced current snapshot")
	}
	if afterRevision != revision {
		t.Fatalf("revision after invalid catalog = %d, want %d", afterRevision, revision)
	}
}

func TestFetchCodexClientModelsFallsBackToNextURL(t *testing.T) {
	invalidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`))
	}))
	defer invalidServer.Close()

	validCatalog := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	validServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(validCatalog)
	}))
	defer validServer.Close()

	previousURLs := codexClientModelsURLs
	codexClientModelsURLs = []string{invalidServer.URL, validServer.URL}
	t.Cleanup(func() { codexClientModelsURLs = previousURLs })

	data, sourceURL := fetchCodexClientModelsFromRemote(context.Background())
	if sourceURL != validServer.URL {
		t.Fatalf("source URL = %q, want %q", sourceURL, validServer.URL)
	}
	if string(data) != string(validCatalog) {
		t.Fatalf("catalog = %s, want %s", data, validCatalog)
	}
}

func TestRefreshCodexClientModelsKeepsLastValidSnapshot(t *testing.T) {
	original, _ := GetCodexClientModelsSnapshot()
	previousURLs := codexClientModelsURLs
	t.Cleanup(func() {
		codexClientModelsURLs = previousURLs
		if _, err := loadCodexClientModelsFromBytes(original, "test cleanup"); err != nil {
			t.Fatalf("restore original catalog: %v", err)
		}
	})

	lastValid := testCodexClientCatalog(t, testCodexClientModel("gpt-5.5", 1))
	if _, err := loadCodexClientModelsFromBytes(lastValid, "test last valid"); err != nil {
		t.Fatalf("load last valid catalog: %v", err)
	}

	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{name: "remote files missing", statusCode: http.StatusNotFound},
		{name: "remote JSON malformed", statusCode: http.StatusOK, body: `{"models":`},
		{name: "remote JSON incomplete", statusCode: http.StatusOK, body: `{"models":[{"slug":"gpt-5.5"}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			servers := make([]*httptest.Server, 0, 2)
			urls := make([]string, 0, 2)
			for range 2 {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(tt.statusCode)
					_, _ = w.Write([]byte(tt.body))
				}))
				servers = append(servers, server)
				urls = append(urls, server.URL)
			}
			defer func() {
				for _, server := range servers {
					server.Close()
				}
			}()

			before, revision := GetCodexClientModelsSnapshot()
			codexClientModelsURLs = urls
			tryRefreshCodexClientModels(context.Background(), "test refresh")
			after, afterRevision := GetCodexClientModelsSnapshot()
			if string(after) != string(before) {
				t.Fatal("failed remote refresh replaced last valid catalog")
			}
			if afterRevision != revision {
				t.Fatalf("revision after failed refresh = %d, want %d", afterRevision, revision)
			}
		})
	}
}

func testCodexClientModel(slug string, priority int) map[string]any {
	return map[string]any{
		"slug":                       slug,
		"display_name":               "Test " + slug,
		"description":                "Test model",
		"base_instructions":          "Test instructions",
		"minimal_client_version":     "0.144.0",
		"visibility":                 "list",
		"context_window":             372000,
		"max_context_window":         372000,
		"priority":                   priority,
		"default_reasoning_level":    "medium",
		"supported_reasoning_levels": []map[string]any{{"effort": "medium", "description": "Balanced"}},
	}
}

func testCodexClientCatalog(t *testing.T, models ...map[string]any) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"models": models})
	if err != nil {
		t.Fatalf("marshal test Codex client catalog: %v", err)
	}
	return data
}
