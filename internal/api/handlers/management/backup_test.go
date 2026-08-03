package management

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestParseBackupResources(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		want    []store.BackupResource
		wantErr string
	}{
		{name: "empty", input: "", want: nil},
		{name: "whitespace only", input: "  , ,  ", want: nil},
		{name: "single", input: "api_keys", want: []store.BackupResource{store.ResourceAPIKeys}},
		{name: "multiple", input: "api_keys,usage,sync_log", want: []store.BackupResource{
			store.ResourceAPIKeys, store.ResourceUsage, store.ResourceSyncLog,
		}},
		{name: "whitespace and dedupe", input: " api_keys , api_keys , usage ", want: []store.BackupResource{
			store.ResourceAPIKeys, store.ResourceUsage,
		}},
		{name: "unknown", input: "api_keys,nonsense", wantErr: "unknown resource: nonsense"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, errMsg := parseBackupResources(tc.input)
			if tc.wantErr != "" {
				if errMsg == "" {
					t.Fatalf("expected error %q, got none", tc.wantErr)
				}
				if errMsg != tc.wantErr {
					t.Fatalf("error = %q, want %q", errMsg, tc.wantErr)
				}
				return
			}
			if errMsg != "" {
				t.Fatalf("unexpected error %q", errMsg)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAllBackupResourcesValid(t *testing.T) {
	for _, r := range store.AllBackupResources {
		if !store.ValidBackupResource(string(r)) {
			t.Fatalf("AllBackupResources contains %q but ValidBackupResource says invalid", r)
		}
	}
	if !store.ValidBackupResource("api_keys") {
		t.Fatal("api_keys should be a valid resource")
	}
	if store.ValidBackupResource("does-not-exist") {
		t.Fatal("does-not-exist should not be a valid resource")
	}
}
