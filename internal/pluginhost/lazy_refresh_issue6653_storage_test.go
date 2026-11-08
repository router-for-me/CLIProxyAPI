package pluginhost

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func issue6653StorageOutputs(t *testing.T, storage *pluginTokenStorage) map[string][]byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "auth.json")
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"RawJSON": storage.RawJSON(), "file": saved}
}

func TestIssue6653StorageSliceViews(t *testing.T) {
	for _, path := range []string{"constructor", "SetMetadata"} {
		for _, order := range []string{"long-first", "short-first", "zero-first", "zero-last"} {
			t.Run(path+"/"+order, func(t *testing.T) {
				items := []string{"a", "b"}
				var rows []any
				var want [][]string
				switch order {
				case "long-first":
					rows, want = []any{items, items[:1]}, [][]string{{"a", "b"}, {"a"}}
				case "short-first":
					rows, want = []any{items[:1], items}, [][]string{{"a"}, {"a", "b"}}
				case "zero-first":
					rows, want = []any{items[:0], items[:1], items}, [][]string{{}, {"a"}, {"a", "b"}}
				case "zero-last":
					rows, want = []any{items, items[:1], items[:0]}, [][]string{{"a", "b"}, {"a"}, {}}
				}
				meta := map[string]any{"rows": rows}
				var storage *pluginTokenStorage
				if path == "constructor" {
					auth := pluginAuthDataToCoreAuth(pluginapi.AuthData{Provider: "qa-storage", Metadata: meta}, "", "", "")
					storage = auth.Storage.(*pluginTokenStorage)
				} else {
					storage = &pluginTokenStorage{provider: "qa-storage"}
					storage.SetMetadata(meta)
				}
				check := func(phase string) {
					t.Helper()
					for name, payload := range issue6653StorageOutputs(t, storage) {
						gotRows := ojson.ParseBytes(payload).Get("rows").Array()
						got := make([][]string, len(gotRows))
						for i, row := range gotRows {
							got[i] = make([]string, 0, len(row.Array()))
							for _, cell := range row.Array() {
								got[i] = append(got[i], cell.String())
							}
						}
						if !reflect.DeepEqual(got, want) {
							t.Errorf("%s %s rows=%v, want %v (exact slice lengths)", phase, name, got, want)
						}
					}
				}
				check("initial")
				items[0], items[1] = "changed-a", "changed-b"
				rows[0] = []string{"replaced"}
				meta["added"] = true
				check("after input mutation")
			})
		}
	}
}

func TestIssue6653StorageNestedOwnership(t *testing.T) {
	for _, path := range []string{"constructor", "SetMetadata"} {
		t.Run(path, func(t *testing.T) {
			nested := map[string]any{"value": "original"}
			names := []string{"original"}
			rows := []any{nested, names}
			meta := map[string]any{"nested": nested, "rows": rows}
			raw := []byte(`{"raw_only":"original","type":"old-provider"}`)
			auth := pluginAuthDataToCoreAuth(pluginapi.AuthData{Provider: "qa-storage", StorageJSON: raw}, "", "", "")
			if path == "constructor" {
				auth = pluginAuthDataToCoreAuth(pluginapi.AuthData{Provider: "qa-storage", StorageJSON: raw, Metadata: meta}, "", "", "")
			}
			storage := auth.Storage.(*pluginTokenStorage)
			if path == "SetMetadata" {
				storage.SetMetadata(meta)
			}
			before := storage.RawJSON()
			nested["value"] = "mutated"
			names[0] = "mutated"
			rows[0] = "replaced"
			meta["nested"] = "replaced"
			if path == "constructor" {
				auth.Metadata["rows"].([]any)[1].([]string)[0] = "auth-mutated"
			}
			for i := range raw {
				raw[i] = 'x'
			}
			returned := storage.RawJSON()
			for i := range returned {
				returned[i] = 'x'
			}
			for name, payload := range issue6653StorageOutputs(t, storage) {
				if !bytes.Equal(payload, before) {
					t.Errorf("%s changed after external mutation: %s, want %s", name, payload, before)
				}
				result := ojson.ParseBytes(payload)
				if result.Get("nested.value").String() != "original" || result.Get("rows.1.0").String() != "original" ||
					result.Get("raw_only").String() != "original" || result.Get("type").String() != "qa-storage" {
					t.Errorf("%s lost nested ownership or raw/provider merge: %s", name, payload)
				}
			}
		})
	}
}

func TestIssue6653StorageCallbackFallbackAndFilePriority(t *testing.T) {
	h := newHostWithRecords(capabilityRecord{
		id: "qa-storage",
		plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{
			AuthProvider: fakeAuthProvider{
				identifier: "qa-storage",
				parseAuth: func(context.Context, pluginapi.AuthParseRequest) (pluginapi.AuthParseResponse, error) {
					// Deliberately omit StorageJSON so the production callback fills it.
					return pluginapi.AuthParseResponse{Handled: true, Auth: pluginapi.AuthData{ID: "qa-storage"}}, nil
				},
			},
		}},
	})
	h.runtimeConfig = &config.Config{AuthDir: t.TempDir()}
	raw := []byte(`{"type":"qa-storage","raw_only":{"name":"original"},"priority":9,"request-retry":2}`)
	path := filepath.Join(h.runtimeConfig.AuthDir, "auth.json")
	auths, err := h.buildAuthsFromFileData(context.Background(), path, raw)
	if err != nil || len(auths) != 1 {
		t.Fatalf("callback fallback auths=%d err=%v", len(auths), err)
	}
	storage := auths[0].Storage.(*pluginTokenStorage)
	for i := range raw {
		raw[i] = 'x'
	}
	for name, payload := range issue6653StorageOutputs(t, storage) {
		got := ojson.ParseBytes(payload)
		if got.Get("raw_only.name").String() != "original" || got.Get("priority").Int() != 9 ||
			got.Get("request_retry").Int() != 2 || got.Get("request-retry").Exists() {
			t.Errorf("%s lost callback fallback, priority or canonical aliases: %s", name, payload)
		}
	}
	storage.SetMetadata(map[string]any{"priority": 7})
	if got := ojson.ParseBytes(storage.RawJSON()).Get("priority").Int(); got != 7 {
		t.Fatalf("metadata priority=%d, want 7", got)
	}
	storage.SetMetadata(map[string]any{"disabled": false})
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if ojson.ParseBytes(saved).Get("priority").Exists() {
		t.Errorf("removed priority returned in file: %s", saved)
	}
	if !os.SameFile(before, after) {
		t.Error("unchanged payload replaced the auth file")
	}
}

func TestIssue6653StorageCloneSliceCycles(t *testing.T) {
	source := make([]any, 2)
	source[0] = source
	source[1] = source[:1]
	cloned := cloneInterceptorMetadata(map[string]any{"cycle": source})["cycle"].([]any)
	if len(cloned) != 2 {
		t.Fatalf("long cycle length=%d, want 2", len(cloned))
	}
	self := cloned[0].([]any)
	short := cloned[1].([]any)
	if len(self) != 2 || len(short) != 1 {
		t.Fatalf("cycle view lengths=%d/%d, want 2/1", len(self), len(short))
	}
	if reflect.ValueOf(self).Pointer() != reflect.ValueOf(cloned).Pointer() ||
		reflect.ValueOf(short[0].([]any)).Pointer() != reflect.ValueOf(cloned).Pointer() {
		t.Fatal("clone failed to preserve references back to the long slice")
	}
	if reflect.ValueOf(cloned).Pointer() == reflect.ValueOf(source).Pointer() ||
		reflect.ValueOf(short).Pointer() == reflect.ValueOf(source).Pointer() {
		t.Fatal("clone retained the source backing array")
	}
	self[0] = "changed clone"
	if _, ok := source[0].([]any); !ok {
		t.Fatal("mutating the cloned cycle changed the source")
	}
}
