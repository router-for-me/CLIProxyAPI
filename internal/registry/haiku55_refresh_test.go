package registry

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
)

func TestHaiku55DefaultCatalogRefresh(t *testing.T) {
	original := getModels()
	t.Cleanup(func() {
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = original
		modelsCatalogStore.mu.Unlock()
	})
	var remote map[string]json.RawMessage
	if errDecode := json.Unmarshal(embeddedModelsJSON, &remote); errDecode != nil {
		t.Fatal(errDecode)
	}
	var models []*ModelInfo
	if errDecode := json.Unmarshal(remote["claude"], &models); errDecode != nil {
		t.Fatal(errDecode)
	}
	kept := models[:0]
	for _, model := range models {
		if model.ID != "claude-haiku-5-5" {
			kept = append(kept, model)
		}
	}
	var errEncode error
	remote["claude"], errEncode = json.Marshal(kept)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	remote["future-section"] = json.RawMessage(`{"keep":true}`)
	payload, errEncode := json.Marshal(remote)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	var remotePayload atomic.Value
	remotePayload.Store(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(remotePayload.Load().([]byte)) }))
	defer server.Close()
	fetch := generalCatalogFetcher([]string{server.URL})
	// Both startup and later refreshes publish a catalog that still serves Haiku.
	for attempt := 0; attempt < 2; attempt++ {
		data, errFetch := fetch(context.Background(), "")
		if errFetch != nil {
			t.Fatal(errFetch)
		}
		if _, errPublish := publishCatalogBytes(data); errPublish != nil {
			t.Fatal(errPublish)
		}
		model := LookupStaticModelInfo("claude-haiku-5-5")
		if model == nil || model.Thinking == nil || !model.Thinking.DynamicAllowed || model.ContextLength != 1000000 {
			t.Fatal("default refresh removed Haiku capabilities")
		}
		var actual map[string]json.RawMessage
		if errDecode := json.Unmarshal(data, &actual); errDecode != nil {
			t.Fatal(errDecode)
		}
		if string(actual["future-section"]) != `{"keep":true}` {
			t.Fatal("unknown catalog section changed")
		}
	}
	path := filepath.Join(t.TempDir(), "catalog.json")
	if errWrite := os.WriteFile(path, payload, 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	for _, source := range []string{path, server.URL} {
		data, errFetch := fetch(context.Background(), source)
		if errFetch != nil {
			t.Fatal(errFetch)
		}
		if string(data) != string(payload) {
			t.Fatal("explicit catalog omission was overridden")
		}
	}
	// When official metadata arrives, it takes precedence without duplicates.
	kept = append(kept, &ModelInfo{ID: "claude-haiku-5-5", ContextLength: 999999})
	remote["claude"], errEncode = json.Marshal(kept)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	payload, errEncode = json.Marshal(remote)
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	remotePayload.Store(payload)
	data, errFetch := fetch(context.Background(), "")
	if errFetch != nil {
		t.Fatal(errFetch)
	}
	if string(data) != string(payload) {
		t.Fatal("official Haiku metadata was overridden")
	}
}
