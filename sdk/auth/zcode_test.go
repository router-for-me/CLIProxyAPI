package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type fakeRunner struct {
	record *coreauth.Auth
}

func (f *fakeRunner) Run(context.Context, *config.Config, *LoginOptions) (*coreauth.Auth, error) {
	return f.record, nil
}

func TestZCodeAuthenticator_Delegates(t *testing.T) {
	want := &coreauth.Auth{ID: "zcode-1.json", Provider: "zcode", Label: "ZCode"}
	fake := &fakeRunner{record: want}
	a := ZCodeAuthenticator{run: fake}
	got, err := a.Login(context.Background(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != "zcode-1.json" {
		t.Fatalf("got %+v", got)
	}
}

func TestZCodeAuthenticator_ProviderAndRefreshLead(t *testing.T) {
	a := NewZCodeAuthenticator()
	if a.Provider() != "zcode" {
		t.Fatalf("Provider() = %q, want zcode", a.Provider())
	}
	if a.RefreshLead() != nil {
		t.Fatalf("RefreshLead() = %v, want nil (no refresh supported)", a.RefreshLead())
	}
}
