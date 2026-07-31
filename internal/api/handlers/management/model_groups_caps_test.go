package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestValidateModelCapsFor(t *testing.T) {
	rpm := func(v int) *int { return &v }
	budget := func(v float64) *float64 { return &v }

	cases := []struct {
		name    string
		allowed []string
		routes  []store.ModelRoute
		wantErr bool
	}{
		{
			name:    "no_routes_ok",
			allowed: []string{"gpt-4o"},
			wantErr: false,
		},
		{
			name:    "routes_without_caps_ok",
			allowed: []string{"gpt-4o"},
			routes:  []store.ModelRoute{{Model: "gpt-4o", Providers: []string{"openai"}}},
			wantErr: false,
		},
		{
			name:    "rpm_cap_ok",
			allowed: []string{"gpt-4o"},
			routes:  []store.ModelRoute{{Model: "gpt-4o", RPMLimit: rpm(60)}},
			wantErr: false,
		},
		{
			name:    "budget_cap_ok",
			allowed: []string{"gpt-4o"},
			routes:  []store.ModelRoute{{Model: "gpt-4o", MaxBudgetUSD: budget(25.0)}},
			wantErr: false,
		},
		{
			name:    "cap_covered_by_wildcard_ok",
			allowed: []string{"gpt-4*"},
			routes:  []store.ModelRoute{{Model: "gpt-4o", RPMLimit: rpm(60)}},
			wantErr: false,
		},
		{
			name:    "cap_on_not_allowed_rejected",
			allowed: []string{"deepseek-v4-pro"},
			routes:  []store.ModelRoute{{Model: "gpt-4o", RPMLimit: rpm(60)}},
			wantErr: true,
		},
		{
			name:    "cap_on_wildcard_route_rejected",
			allowed: []string{"gpt-4*"},
			routes:  []store.ModelRoute{{Model: "gpt-4*", RPMLimit: rpm(60)}},
			wantErr: true,
		},
		{
			name:    "negative_rpm_rejected",
			allowed: []string{"gpt-4o"},
			routes:  []store.ModelRoute{{Model: "gpt-4o", RPMLimit: rpm(-5)}},
			wantErr: true,
		},
		{
			name:    "negative_budget_rejected",
			allowed: []string{"gpt-4o"},
			routes:  []store.ModelRoute{{Model: "gpt-4o", MaxBudgetUSD: budget(-1)}},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateModelCapsFor(tc.allowed, tc.routes)
			if (got != "") != tc.wantErr {
				t.Fatalf("validateModelCapsFor() = %q, wantErr=%v", got, tc.wantErr)
			}
		})
	}
}

func TestDeriveModelCaps(t *testing.T) {
	rpm := func(v int) *int { return &v }
	budget := func(v float64) *float64 { return &v }

	routes := []store.ModelRoute{
		{Model: "gpt-4o", Providers: []string{"openai"}, RPMLimit: rpm(60), MaxBudgetUSD: budget(25)},
		{Model: "gpt-4o-mini", Providers: []string{"openai"}}, // no caps
		{Model: "claude-sonnet", MaxBudgetUSD: budget(5)},     // budget-only
	}
	rpmMap, budgetMap, discountMap := deriveModelCaps(routes)

	if len(rpmMap) != 1 || rpmMap["gpt-4o"] != 60 {
		t.Fatalf("rpm map mismatch: %+v", rpmMap)
	}
	if len(budgetMap) != 2 || budgetMap["gpt-4o"] != 25 || budgetMap["claude-sonnet"] != 5 {
		t.Fatalf("budget map mismatch: %+v", budgetMap)
	}
	if _, ok := budgetMap["gpt-4o-mini"]; ok {
		t.Fatalf("budget map must not contain cap-less models: %+v", budgetMap)
	}
	if len(discountMap) != 0 {
		t.Fatalf("discount map must be empty when no route carries a discount: %+v", discountMap)
	}
}
