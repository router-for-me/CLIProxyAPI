package configaccess

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v7/sdk/access"
)

func TestProviderPrincipalResolution(t *testing.T) {
	tests := []struct {
		name      string
		entries   []sdkaccess.APIKeyEntry
		key       string
		wantLabel string
	}{
		{
			name:      "named entry labels the raw key",
			entries:   []sdkaccess.APIKeyEntry{{Key: "k1", Name: "alice"}},
			key:       "k1",
			wantLabel: "alice",
		},
		{
			name:      "unnamed entry has no label",
			entries:   []sdkaccess.APIKeyEntry{{Key: "k1"}},
			key:       "k1",
			wantLabel: "",
		},
		{
			name:      "duplicate key adopts first non-empty name",
			entries:   []sdkaccess.APIKeyEntry{{Key: "k1"}, {Key: "k1", Name: "alice"}},
			key:       "k1",
			wantLabel: "alice",
		},
		{
			name:      "duplicate key keeps first name",
			entries:   []sdkaccess.APIKeyEntry{{Key: "k1", Name: "alice"}, {Key: "k1", Name: "bob"}},
			key:       "k1",
			wantLabel: "alice",
		},
		{
			name:      "whitespace is trimmed",
			entries:   []sdkaccess.APIKeyEntry{{Key: "  k1  ", Name: "  alice  "}},
			key:       "k1",
			wantLabel: "alice",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newProvider("", normalizeKeys(tt.entries))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+tt.key)

			result, authErr := p.Authenticate(context.Background(), req)
			if authErr != nil {
				t.Fatalf("Authenticate() error = %v", authErr)
			}
			if result.Principal != tt.key {
				t.Fatalf("Principal = %q, want raw key %q", result.Principal, tt.key)
			}
			if result.PrincipalLabel != tt.wantLabel {
				t.Fatalf("PrincipalLabel = %q, want %q", result.PrincipalLabel, tt.wantLabel)
			}
		})
	}
}

func TestProviderRejectsUnknownKey(t *testing.T) {
	p := newProvider("", normalizeKeys([]sdkaccess.APIKeyEntry{{Key: "k1", Name: "alice"}}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer other")

	if _, authErr := p.Authenticate(context.Background(), req); authErr == nil {
		t.Fatal("Authenticate() error = nil, want invalid credential error")
	}
}

func TestNormalizeKeysDropsEmptyEntries(t *testing.T) {
	got := normalizeKeys([]sdkaccess.APIKeyEntry{{Key: "  "}, {Key: "k1"}, {Key: ""}})
	if len(got) != 1 || got[0].Key != "k1" {
		t.Fatalf("normalizeKeys() = %#v, want one k1 entry", got)
	}
}
