package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRemoteStoresDisabledLoginReachesTokenStorage(t *testing.T) {
	tests := []struct {
		name  string
		store cliproxyauth.Store
	}{
		{
			name: "postgres",
			store: &PostgresStore{
				authDir: filepath.Join(t.TempDir(), "auths"),
			},
		},
		{
			name: "object",
			store: &ObjectTokenStore{
				authDir: filepath.Join(t.TempDir(), "auths"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errReachedStorage := errors.New("reached token storage")
			auth := &cliproxyauth.Auth{
				ID:       "canonical-disabled.json",
				FileName: "canonical-disabled.json",
				Provider: "claude",
				Disabled: true,
				Storage: &callbackTokenStorage{save: func(string) error {
					return errReachedStorage
				}},
				Metadata: map[string]any{"type": "claude"},
			}

			_, errSave := tt.store.Save(context.Background(), auth)
			if !errors.Is(errSave, errReachedStorage) {
				t.Fatalf("Save() error = %v, want token storage sentinel", errSave)
			}
		})
	}
}
