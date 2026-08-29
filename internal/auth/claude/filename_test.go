package claude

import (
	"context"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type credentialListStore struct {
	records []*coreauth.Auth
}

func (s *credentialListStore) List(context.Context) ([]*coreauth.Auth, error) {
	return s.records, nil
}

func (*credentialListStore) Save(context.Context, *coreauth.Auth) (string, error) {
	panic("unexpected Save call")
}

func (*credentialListStore) Delete(context.Context, string) error {
	panic("unexpected Delete call")
}

func TestCredentialFileName(t *testing.T) {
	tests := []struct {
		name             string
		email            string
		organizationUUID string
		accountUUID      string
		want             string
	}{
		{
			name:             "organization hash keeps organizations with the same email distinct",
			email:            "user@example.com",
			organizationUUID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb",
			accountUUID:      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			want:             "claude-00f765af-user@example.com.json",
		},
		{
			name:             "different organization produces a different filename",
			email:            "user@example.com",
			organizationUUID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc",
			accountUUID:      "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
			want:             "claude-50d86b12-user@example.com.json",
		},
		{
			name:        "account hash is used without organization",
			email:       " user@example.com ",
			accountUUID: " aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa ",
			want:        "claude-303617b9-user@example.com.json",
		},
		{
			name:  "missing identities fall back to legacy email filename",
			email: " user@example.com ",
			want:  "claude-user@example.com.json",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CredentialFileName(tt.email, tt.organizationUUID, tt.accountUUID)
			if got != tt.want {
				t.Fatalf("CredentialFileName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFindMatchingLegacyCredential(t *testing.T) {
	const email = "user@example.com"
	legacyFileName := CredentialFileName(email, "", "")

	tests := []struct {
		name           string
		targetMetadata map[string]any
		legacyMetadata map[string]any
		wantMatch      bool
	}{
		{
			name: "matching organization",
			targetMetadata: map[string]any{
				"email": email, "organization_uuid": "organization-a", "account_uuid": "shared-account",
			},
			legacyMetadata: map[string]any{
				"email": email, "organization_uuid": "organization-a", "account_uuid": "shared-account",
			},
			wantMatch: true,
		},
		{
			name: "different organization with shared account",
			targetMetadata: map[string]any{
				"email": email, "organization_uuid": "organization-b", "account_uuid": "shared-account",
			},
			legacyMetadata: map[string]any{
				"email": email, "organization_uuid": "organization-a", "account_uuid": "shared-account",
			},
		},
		{
			name: "missing legacy organization remains ambiguous",
			targetMetadata: map[string]any{
				"email": email, "organization_uuid": "organization-a", "account_uuid": "shared-account",
			},
			legacyMetadata: map[string]any{
				"email": email, "account_uuid": "shared-account",
			},
		},
		{
			name: "matching account when neither has organization",
			targetMetadata: map[string]any{
				"email": email, "account_uuid": "account-a",
			},
			legacyMetadata: map[string]any{
				"email": email, "account_uuid": "account-a",
			},
			wantMatch: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target := &coreauth.Auth{
				ID:       CredentialFileName(email, metadataString(tt.targetMetadata, "organization_uuid"), metadataString(tt.targetMetadata, "account_uuid")),
				FileName: CredentialFileName(email, metadataString(tt.targetMetadata, "organization_uuid"), metadataString(tt.targetMetadata, "account_uuid")),
				Provider: "claude",
				Metadata: tt.targetMetadata,
			}
			legacy := &coreauth.Auth{
				ID: legacyFileName, FileName: legacyFileName, Provider: "claude", Metadata: tt.legacyMetadata,
			}
			got, err := FindMatchingLegacyCredential(context.Background(), &credentialListStore{records: []*coreauth.Auth{legacy}}, target)
			if err != nil {
				t.Fatalf("FindMatchingLegacyCredential() error = %v", err)
			}
			if (got != nil) != tt.wantMatch {
				t.Fatalf("FindMatchingLegacyCredential() match = %v, want %v", got != nil, tt.wantMatch)
			}
		})
	}
}
