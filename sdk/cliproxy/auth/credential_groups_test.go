package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type credentialGroupTestExecutor struct{}

func (*credentialGroupTestExecutor) Identifier() string { return "credential-group-test" }

func (*credentialGroupTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*credentialGroupTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (*credentialGroupTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*credentialGroupTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (*credentialGroupTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestCredentialGroupEligibility(t *testing.T) {
	eligibility := authSelectionEligibilityForRequest(nil, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.CredentialGroupsMetadataKey: []string{"team-a"},
	}})

	tests := []struct {
		name string
		auth *Auth
		want bool
	}{
		{name: "matching attribute", auth: &Auth{Attributes: map[string]string{"credential_group": "team-a"}}, want: true},
		{name: "matching metadata list", auth: &Auth{Metadata: map[string]any{"credential_groups": []any{"team-b", "team-a"}}}, want: true},
		{name: "different group", auth: &Auth{Metadata: map[string]any{"credential_group": "team-b"}}, want: false},
		{name: "missing group", auth: &Auth{}, want: false},
		{name: "nil auth", auth: nil, want: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := eligibility.allows(test.auth); got != test.want {
				t.Fatalf("allows() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestEmptyCredentialGroupMetadataDeniesSelection(t *testing.T) {
	eligibility := authSelectionEligibilityForRequest(nil, cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.CredentialGroupsMetadataKey: []string{"", "   "},
	}})
	if eligibility.allows(&Auth{Attributes: map[string]string{"credential_group": "team-a"}}) {
		t.Fatal("explicit empty credential group metadata must deny every credential")
	}
}

func TestMissingCredentialGroupMetadataKeepsLegacyBehavior(t *testing.T) {
	eligibility := authSelectionEligibilityForRequest(nil, cliproxyexecutor.Options{})
	if !eligibility.allows(&Auth{}) {
		t.Fatal("missing credential group metadata must retain legacy unrestricted behavior")
	}
}

func TestManagerSelectAuthKeepsResidentialCredentialGroupsIsolated(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &credentialGroupTestExecutor{}
	manager.RegisterExecutor(executor)

	ctx := context.Background()
	credentials := []*Auth{
		{ID: "credential-a", Provider: executor.Identifier(), Status: StatusActive, Attributes: map[string]string{"credential_group": "a-residential"}},
		{ID: "credential-b", Provider: executor.Identifier(), Status: StatusActive, Attributes: map[string]string{"credential_group": "b-residential"}},
	}
	for _, credential := range credentials {
		if _, err := manager.Register(ctx, credential); err != nil {
			t.Fatalf("register %s: %v", credential.ID, err)
		}
	}

	tests := []struct {
		name       string
		groups     []string
		wantAuthID string
		wantError  bool
	}{
		{name: "account A", groups: []string{"a-residential"}, wantAuthID: "credential-a"},
		{name: "account B", groups: []string{"b-residential"}, wantAuthID: "credential-b"},
		{name: "explicit empty groups", groups: []string{}, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected, err := manager.SelectAuth(ctx, executor.Identifier(), "", cliproxyexecutor.Options{Metadata: map[string]any{
				cliproxyexecutor.CredentialGroupsMetadataKey: test.groups,
			}})
			if test.wantError {
				if err == nil {
					t.Fatalf("SelectAuth() selected %#v, want fail-closed error", selected)
				}
				return
			}
			if err != nil {
				t.Fatalf("SelectAuth() error = %v", err)
			}
			if selected == nil || selected.ID != test.wantAuthID {
				t.Fatalf("SelectAuth() = %#v, want auth %q", selected, test.wantAuthID)
			}
		})
	}
}
