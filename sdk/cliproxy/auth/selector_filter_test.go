package auth_test

import (
	"context"
	"errors"
	"testing"

	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type filteredPolicy struct {
	auth.FillFirstSelector
	excluded string
	err      error
}

func (p *filteredPolicy) FilterCandidates(_ context.Context, _, _ string, _ executor.Options, accounts []*auth.Auth) ([]*auth.Auth, error) {
	if p.err != nil {
		return nil, p.err
	}
	var result []*auth.Auth
	for _, a := range accounts {
		if a.ID != p.excluded {
			result = append(result, a)
		}
	}
	return result, nil
}
func TestAffinityFiltersBeforeReusingBinding(t *testing.T) {
	policy := &filteredPolicy{}
	selector := auth.NewSessionAffinitySelector(policy)
	defer selector.Stop()
	ctx := context.Background()
	opts := executor.Options{Headers: map[string][]string{"Session_id": {"filter-session"}}}
	accounts := []*auth.Auth{{ID: "a"}, {ID: "z"}}
	selected, err := selector.Pick(ctx, "codex", "model", opts, accounts)
	if err != nil || selected == nil || selected.ID != "a" {
		t.Fatalf("initial pick: %v %v", selected, err)
	}
	policy.excluded = "a"
	selected, err = selector.Pick(ctx, "codex", "model", opts, accounts)
	if err != nil || selected == nil || selected.ID != "z" {
		t.Fatalf("excluded cached account reused: %v %v", selected, err)
	}
	policy.err = errors.New("quota unavailable")
	selected, err = selector.Pick(ctx, "codex", "model", opts, accounts)
	if selected != nil || !errors.Is(err, policy.err) {
		t.Fatalf("filter failure bypassed: %v %v", selected, err)
	}
}
