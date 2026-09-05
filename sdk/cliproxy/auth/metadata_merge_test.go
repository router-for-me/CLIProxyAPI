package auth

import (
	"reflect"
	"testing"
)

type dummyMetadataStorage struct {
	meta map[string]any
}

func (d *dummyMetadataStorage) SetMetadata(m map[string]any) {
	d.meta = m
}

func (d *dummyMetadataStorage) SaveTokenToFile(_ string) error {
	return nil
}

func TestMergeRefreshedAuth(t *testing.T) {
	t.Run("nil current returns clone of updated or base", func(t *testing.T) {
		base := &Auth{ID: "base"}
		updated := &Auth{ID: "updated"}

		got := MergeRefreshedAuth(base, nil, updated)
		if got == nil || got.ID != "updated" {
			t.Fatalf("expected updated clone, got %#v", got)
		}

		got = MergeRefreshedAuth(base, nil, nil)
		if got == nil || got.ID != "base" {
			t.Fatalf("expected base clone, got %#v", got)
		}

		got = MergeRefreshedAuth(nil, nil, nil)
		if got != nil {
			t.Fatalf("expected nil, got %#v", got)
		}
	})

	t.Run("nil updated returns clone of current", func(t *testing.T) {
		current := &Auth{ID: "current"}
		got := MergeRefreshedAuth(nil, current, nil)
		if got == nil || got.ID != "current" {
			t.Fatalf("expected current clone, got %#v", got)
		}
	})

	t.Run("preserves concurrent proxy_url and merges refreshed token", func(t *testing.T) {
		base := &Auth{
			ID:       "auth1",
			Metadata: map[string]any{"access_token": "token-old", "type": "antigravity"},
		}
		current := &Auth{
			ID:       "auth1",
			ProxyURL: "http://127.0.0.1:8080",
			Metadata: map[string]any{
				"access_token": "token-old",
				"type":         "antigravity",
				"proxy_url":    "http://127.0.0.1:8080",
				"user_note":    "important",
			},
		}
		updated := &Auth{
			ID: "auth1",
			Metadata: map[string]any{
				"access_token": "token-new",
				"type":         "antigravity",
				"expired":      "2030-01-01T00:00:00Z",
			},
		}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged == nil {
			t.Fatal("merged is nil")
		}

		if merged.ProxyURL != "http://127.0.0.1:8080" {
			t.Fatalf("ProxyURL = %q, want http://127.0.0.1:8080", merged.ProxyURL)
		}
		if got, _ := merged.Metadata["proxy_url"].(string); got != "http://127.0.0.1:8080" {
			t.Fatalf("Metadata[proxy_url] = %q, want http://127.0.0.1:8080", got)
		}
		if got, _ := merged.Metadata["user_note"].(string); got != "important" {
			t.Fatalf("Metadata[user_note] = %q, want important", got)
		}
		if got, _ := merged.Metadata["access_token"].(string); got != "token-new" {
			t.Fatalf("Metadata[access_token] = %q, want token-new", got)
		}
		if got, _ := merged.Metadata["expired"].(string); got != "2030-01-01T00:00:00Z" {
			t.Fatalf("Metadata[expired] = %q, want 2030-01-01T00:00:00Z", got)
		}
	})

	t.Run("preserves concurrent note edit while executor adds project_id", func(t *testing.T) {
		base := &Auth{
			ID: "auth2",
			Metadata: map[string]any{
				"access_token": "tok1",
				"note":         "original-note",
			},
		}
		current := &Auth{
			ID: "auth2",
			Metadata: map[string]any{
				"access_token": "tok1",
				"note":         "concurrently-updated-note",
			},
		}
		updated := &Auth{
			ID: "auth2",
			Metadata: map[string]any{
				"access_token": "tok2",
				"note":         "original-note",
				"project_id":   "discovered-proj-123",
			},
		}

		merged := MergeRefreshedAuth(base, current, updated)
		if got, _ := merged.Metadata["note"].(string); got != "concurrently-updated-note" {
			t.Fatalf("Metadata[note] = %q, want concurrently-updated-note", got)
		}
		if got, _ := merged.Metadata["project_id"].(string); got != "discovered-proj-123" {
			t.Fatalf("Metadata[project_id] = %q, want discovered-proj-123", got)
		}
		if got, _ := merged.Metadata["access_token"].(string); got != "tok2" {
			t.Fatalf("Metadata[access_token] = %q, want tok2", got)
		}
	})

	t.Run("propagates storage and runtime", func(t *testing.T) {
		storage := &dummyMetadataStorage{}
		base := &Auth{ID: "auth3"}
		current := &Auth{ID: "auth3"}
		updated := &Auth{
			ID:       "auth3",
			Storage:  storage,
			Runtime:  "mock-runtime",
			Metadata: map[string]any{"access_token": "tok-stored"},
		}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged.Storage != storage {
			t.Fatalf("Storage = %#v, want %#v", merged.Storage, storage)
		}
		if merged.Runtime != "mock-runtime" {
			t.Fatalf("Runtime = %v, want mock-runtime", merged.Runtime)
		}
	})

	t.Run("merges attributes and prefix", func(t *testing.T) {
		base := &Auth{
			ID:         "auth4",
			Prefix:     "old-prefix",
			Attributes: map[string]string{"shared": "base", "removed_by_exec": "true"},
		}
		current := &Auth{
			ID:         "auth4",
			Prefix:     "concurrent-prefix",
			Attributes: map[string]string{"shared": "base", "added_by_user": "custom"},
		}
		updated := &Auth{
			ID:         "auth4",
			Prefix:     "old-prefix",
			Attributes: map[string]string{"shared": "updated-by-exec"},
		}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged.Prefix != "concurrent-prefix" {
			t.Fatalf("Prefix = %q, want concurrent-prefix", merged.Prefix)
		}
		expectedAttrs := map[string]string{
			"shared":        "updated-by-exec",
			"added_by_user": "custom",
		}
		if !reflect.DeepEqual(merged.Attributes, expectedAttrs) {
			t.Fatalf("Attributes = %#v, want %#v", merged.Attributes, expectedAttrs)
		}
	})

	t.Run("stale registration epoch returns clone of current", func(t *testing.T) {
		base := &Auth{
			ID:                "auth5",
			RegistrationEpoch: 1,
			Metadata:          map[string]any{"access_token": "old-epoch-1-token"},
		}
		current := &Auth{
			ID:                "auth5",
			RegistrationEpoch: 2,
			Metadata:          map[string]any{"access_token": "new-epoch-2-token"},
		}
		updated := &Auth{
			ID:                "auth5",
			RegistrationEpoch: 1,
			Metadata:          map[string]any{"access_token": "refreshed-epoch-1-token"},
		}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged == nil {
			t.Fatal("merged is nil")
		}
		if merged.RegistrationEpoch != 2 {
			t.Fatalf("RegistrationEpoch = %d, want 2", merged.RegistrationEpoch)
		}
		if got, _ := merged.Metadata["access_token"].(string); got != "new-epoch-2-token" {
			t.Fatalf("Metadata[access_token] = %q, want new-epoch-2-token", got)
		}
	})

	t.Run("concurrently cleared proxy_url is not resurrected", func(t *testing.T) {
		base := &Auth{
			ID:       "auth6",
			ProxyURL: "http://proxy.old:8080",
			Metadata: map[string]any{
				"access_token": "tok-old",
				"proxy_url":    "http://proxy.old:8080",
			},
		}
		current := &Auth{
			ID:       "auth6",
			ProxyURL: "",
			Metadata: map[string]any{
				"access_token": "tok-old",
			},
		}
		updated := &Auth{
			ID:       "auth6",
			ProxyURL: "http://proxy.old:8080", // executor didn't touch ProxyURL, it inherited from base
			Metadata: map[string]any{
				"access_token": "tok-new",
				"proxy_url":    "http://proxy.old:8080",
			},
		}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged == nil {
			t.Fatal("merged is nil")
		}
		if merged.ProxyURL != "" {
			t.Fatalf("ProxyURL = %q, want empty (should not resurrect)", merged.ProxyURL)
		}
		if _, exists := merged.Metadata["proxy_url"]; exists {
			t.Fatalf("Metadata[proxy_url] should not exist, got %v", merged.Metadata["proxy_url"])
		}
		if got, _ := merged.Metadata["access_token"].(string); got != "tok-new" {
			t.Fatalf("Metadata[access_token] = %q, want tok-new", got)
		}
	})

	t.Run("single canonical proxy arbitration when both exist and only struct field modified", func(t *testing.T) {
		base := &Auth{ID: "auth7a", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}
		current := &Auth{ID: "auth7a", ProxyURL: "http://user-new:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}
		updated := &Auth{ID: "auth7a", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged.ProxyURL != "http://user-new:8080" {
			t.Fatalf("ProxyURL = %q, want http://user-new:8080", merged.ProxyURL)
		}
		if got, _ := merged.Metadata["proxy_url"].(string); got != "http://user-new:8080" {
			t.Fatalf("Metadata[proxy_url] = %q, want http://user-new:8080", got)
		}
	})

	t.Run("single canonical proxy arbitration when both exist and only metadata modified", func(t *testing.T) {
		base := &Auth{ID: "auth8a", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}
		current := &Auth{ID: "auth8a", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://user-new:8080"}}
		updated := &Auth{ID: "auth8a", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged.ProxyURL != "http://user-new:8080" {
			t.Fatalf("ProxyURL = %q, want http://user-new:8080", merged.ProxyURL)
		}
		if got, _ := merged.Metadata["proxy_url"].(string); got != "http://user-new:8080" {
			t.Fatalf("Metadata[proxy_url] = %q, want http://user-new:8080", got)
		}
	})

	t.Run("single canonical proxy arbitration when both exist and struct cleared", func(t *testing.T) {
		base := &Auth{ID: "auth7b", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}
		current := &Auth{ID: "auth7b", ProxyURL: "", Metadata: map[string]any{"proxy_url": "http://old:8080"}}
		updated := &Auth{ID: "auth7b", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged.ProxyURL != "" {
			t.Fatalf("ProxyURL = %q, want empty", merged.ProxyURL)
		}
		if _, exists := merged.Metadata["proxy_url"]; exists {
			t.Fatalf("Metadata[proxy_url] should not exist, got %v", merged.Metadata["proxy_url"])
		}
	})

	t.Run("single canonical proxy arbitration when both exist and metadata cleared", func(t *testing.T) {
		base := &Auth{ID: "auth8b", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}
		current := &Auth{ID: "auth8b", ProxyURL: "http://old:8080", Metadata: map[string]any{}}
		updated := &Auth{ID: "auth8b", ProxyURL: "http://old:8080", Metadata: map[string]any{"proxy_url": "http://old:8080"}}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged.ProxyURL != "" {
			t.Fatalf("ProxyURL = %q, want empty", merged.ProxyURL)
		}
		if _, exists := merged.Metadata["proxy_url"]; exists {
			t.Fatalf("Metadata[proxy_url] should not exist, got %v", merged.Metadata["proxy_url"])
		}
	})

	t.Run("merges executor disabled status", func(t *testing.T) {
		base := &Auth{ID: "auth-dis", Status: StatusActive, Disabled: false}
		current := &Auth{ID: "auth-dis", Status: StatusActive, Disabled: false}
		updated := &Auth{ID: "auth-dis", Status: StatusDisabled, Disabled: true}

		merged := MergeRefreshedAuth(base, current, updated)
		if !merged.Disabled {
			t.Fatal("Disabled = false, want true (executor disabled)")
		}
		if merged.Status != StatusDisabled {
			t.Fatalf("Status = %v, want StatusDisabled", merged.Status)
		}
		if disabledMeta, ok := merged.Metadata["disabled"].(bool); !ok || !disabledMeta {
			t.Fatalf("Metadata[disabled] = %v, want true", merged.Metadata["disabled"])
		}
	})

	t.Run("executor disabled takes precedence over concurrent 503 error", func(t *testing.T) {
		base := &Auth{ID: "auth-dis-503", Status: StatusActive, Disabled: false}
		current := &Auth{
			ID:            "auth-dis-503",
			Status:        StatusError,
			Unavailable:   true,
			StatusMessage: "upstream 503",
			LastError:     &Error{Message: "upstream 503"},
		}
		updated := &Auth{ID: "auth-dis-503", Status: StatusDisabled, Disabled: true}

		merged := MergeRefreshedAuth(base, current, updated)
		if !merged.Disabled {
			t.Fatal("Disabled = false, want true")
		}
		if merged.Status != StatusDisabled {
			t.Fatalf("Status = %v, want StatusDisabled (disabled must take precedence over concurrent 503)", merged.Status)
		}
		if disabledMeta, ok := merged.Metadata["disabled"].(bool); !ok || !disabledMeta {
			t.Fatalf("Metadata[disabled] = %v, want true", merged.Metadata["disabled"])
		}
	})

	t.Run("preserves new concurrent 503 error on current during refresh", func(t *testing.T) {
		base := &Auth{
			ID:     "auth9",
			Status: StatusActive,
		}
		current := &Auth{
			ID:            "auth9",
			Status:        StatusError,
			Unavailable:   true,
			StatusMessage: "upstream 503",
			LastError:     &Error{Message: "upstream 503"},
		}
		updated := &Auth{
			ID:     "auth9",
			Status: StatusActive,
		}

		merged := MergeRefreshedAuth(base, current, updated)
		if merged.Status != StatusError {
			t.Fatalf("Status = %v, want StatusError (concurrent 503 must be preserved)", merged.Status)
		}
		if !merged.Unavailable {
			t.Fatal("Unavailable = false, want true")
		}
		if merged.StatusMessage != "upstream 503" {
			t.Fatalf("StatusMessage = %q, want upstream 503", merged.StatusMessage)
		}
	})

	t.Run("MergePreparedAuth does not modify lifecycle fields", func(t *testing.T) {
		base := &Auth{
			ID:            "auth10",
			Status:        StatusError,
			Unavailable:   true,
			StatusMessage: "cooling_503",
			LastError:     &Error{Message: "cooling_503"},
		}
		current := base.Clone()
		updated := &Auth{
			ID:     "auth10",
			Status: StatusActive,
			Metadata: map[string]any{
				"project_id": "discovered-project",
			},
		}

		merged := MergePreparedAuth(base, current, updated)
		if merged.Status != StatusError {
			t.Fatalf("Status = %v, want StatusError", merged.Status)
		}
		if !merged.Unavailable {
			t.Fatal("Unavailable = false, want true")
		}
		if merged.LastError == nil || merged.LastError.Message != "cooling_503" {
			t.Fatalf("LastError = %v, want cooling_503", merged.LastError)
		}
		if got, _ := merged.Metadata["project_id"].(string); got != "discovered-project" {
			t.Fatalf("project_id = %q, want discovered-project", got)
		}
	})
}
