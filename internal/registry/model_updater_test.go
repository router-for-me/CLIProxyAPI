package registry

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func TestRefreshModelsRetainsCatalogAndNotifiesRecovery(t *testing.T) {
	previousURLs := modelsURLs
	previousData := getModels()
	refreshCallbackMu.Lock()
	previousCallback := refreshCallback
	previousPending := pendingRefreshChanges
	refreshCallback = nil
	pendingRefreshChanges = nil
	refreshCallbackMu.Unlock()
	t.Cleanup(func() {
		modelsURLs = previousURLs
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = previousData
		modelsCatalogStore.mu.Unlock()
		refreshCallbackMu.Lock()
		refreshCallback = previousCallback
		pendingRefreshChanges = previousPending
		refreshCallbackMu.Unlock()
	})
	if err := loadModelsFromBytes([]byte(`{"codex-team":[{"id":"existing-model"}]}`), "test"); err != nil {
		t.Fatalf("load initial catalog: %v", err)
	}
	initialData := getModels()
	var notifications [][]string
	SetModelRefreshCallback(func(providers []string) {
		notifications = append(notifications, providers)
	})
	status := http.StatusServiceUnavailable
	body := "unavailable"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer server.Close()
	modelsURLs = []string{server.URL}

	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unavailable", http.StatusServiceUnavailable, "unavailable"},
		{"invalid catalog", http.StatusOK, `{"codex-team":[{}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, body = tc.status, tc.body
			if tryRefreshModels(context.Background(), "test refresh") {
				t.Fatal("failed refresh reported success")
			}
			if getModels() != initialData || len(notifications) != 0 {
				t.Fatal("failed refresh changed the catalog or notified consumers")
			}
		})
	}

	status = http.StatusOK
	body = `{"codex-team":[{"id":"existing-model"},{"id":"new-model"}]}`
	if !tryRefreshModels(context.Background(), "test recovery") {
		t.Fatal("valid refresh reported failure")
	}
	models := GetCodexTeamModels()
	found := false
	for _, model := range models {
		if model.ID == "new-model" {
			found = true
		}
	}
	if !found {
		t.Fatal("recovered catalog does not expose the new model")
	}
	if !reflect.DeepEqual(notifications, [][]string{{"codex"}}) {
		t.Fatalf("notifications = %v, want [[codex]]", notifications)
	}
	if !tryRefreshModels(context.Background(), "test unchanged refresh") {
		t.Fatal("unchanged catalog should count as a successful refresh")
	}
	if len(notifications) != 1 {
		t.Fatalf("unchanged catalog notified consumers again: %v", notifications)
	}
}

func TestDetectChangedProviders_CodexConfigurationUpdate(t *testing.T) {
	oldData := &staticModelsJSON{
		CodexFree: []*ModelInfo{{ID: "gpt-6-luna"}},
	}
	newData := &staticModelsJSON{
		CodexFree: []*ModelInfo{{ID: "gpt-6-luna", SupportConfigurationUpdate: true}},
	}

	changed := detectChangedProviders(oldData, newData)
	if len(changed) != 1 || changed[0] != "codex" {
		t.Fatalf("configuration_update-only change: got providers %v, want [codex]", changed)
	}
}

func TestDetectChangedProviders_KimiAliases(t *testing.T) {
	oldData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}},
	}
	newData := &staticModelsJSON{
		Kimi: []*ModelInfo{{ID: "kimi-k2"}, {ID: "kimi-k3"}},
	}

	changed := detectChangedProviders(oldData, newData)
	expected := map[string]bool{
		"kimi":     false,
		"kimi-ai":  false,
		"kimi.ai":  false,
		"kimi.com": false,
	}

	for _, p := range changed {
		if _, ok := expected[p]; ok {
			expected[p] = true
		}
	}

	for p, found := range expected {
		if !found {
			t.Errorf("expected changed provider %q to be reported, got %v", p, changed)
		}
	}
}
