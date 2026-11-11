package registry

import "testing"

func TestReplaceClientModelsProjectionGeneration(t *testing.T) {
	for _, mode := range []string{"same-provider", "new-provider", "register"} {
		t.Run(mode, func(t *testing.T) {
			r := newTestModelRegistry()
			models := []*ModelInfo{{ID: "keep"}}
			r.RegisterClient("account", "antigravity", models)
			epoch := r.ClientRegistrationEpoch("account")
			if !r.ApplyClientModelProjections("account", epoch, 11, []ClientModelProjection{{ModelID: "keep", QuotaExceeded: true, Suspended: true, SuspendReason: "rate_limit"}}) {
				t.Fatal("initial projection rejected")
			}
			switch mode {
			case "register":
				r.RegisterClient("account", "antigravity", models)
			default:
				provider := "antigravity"
				if mode == "new-provider" {
					provider = "gemini"
				}
				if _, ok := r.ReplaceClientModels("account", provider, epoch, models); !ok {
					t.Fatal("replacement rejected")
				}
			}
			epoch = r.ClientRegistrationEpoch("account")
			accepted := r.ApplyClientModelProjections("account", epoch, 10, []ClientModelProjection{{ModelID: "keep"}})
			if mode == "same-provider" {
				if accepted || !r.IsModelQuotaExceededForClient("account", "keep") || r.models["keep"].SuspendedClients["account"] != "rate_limit" {
					t.Fatal("older generation overwrote retained scheduling state")
				}
				if !r.ApplyClientModelProjections("account", epoch, 12, []ClientModelProjection{{ModelID: "keep"}}) {
					t.Fatal("newer projection rejected")
				}
			} else if !accepted {
				t.Fatal("fresh registration did not reset generation")
			}
		})
	}
}

func TestReplaceClientModelsEpochAndAvailability(t *testing.T) {
	r := newTestModelRegistry()
	models := []*ModelInfo{{ID: "keep"}, {ID: "remove"}, {ID: "keep"}}
	if _, ok := r.ReplaceClientModels("account", "antigravity", 0, models); !ok {
		t.Fatal("initial epoch rejected")
	}
	epoch := r.ClientRegistrationEpoch("account")
	r.SuspendClientModel("account", "keep", "rate_limit")
	r.SetModelQuotaExceeded("account", "keep")
	if _, ok := r.ReplaceClientModels("account", "antigravity", epoch, []*ModelInfo{{ID: "keep"}, {ID: "add"}}); !ok {
		t.Fatal("replacement failed")
	}
	if r.models["keep"].SuspendedClients["account"] != "rate_limit" || !r.IsModelQuotaExceededForClient("account", "keep") {
		t.Fatal("retained model lost cooldown")
	}
	if r.ClientSupportsModel("account", "remove") || !r.ClientSupportsModel("account", "add") {
		t.Fatal("wrong model set")
	}
	if _, ok := r.ReplaceClientModels("account", "antigravity", epoch, models); ok {
		t.Fatal("stale epoch accepted")
	}
	r.RegisterClient("account", "antigravity", []*ModelInfo{{ID: "keep"}})
	if r.models["keep"].SuspendedClients["account"] != "" || r.IsModelQuotaExceededForClient("account", "keep") {
		t.Fatal("ordinary registration no longer clears state")
	}
	epoch = r.ClientRegistrationEpoch("account")
	if _, ok := r.ReplaceClientModels("account", "antigravity", epoch, nil); !ok {
		t.Fatal("empty catalog rejected")
	}
	if len(r.GetModelsForClient("account")) != 0 {
		t.Fatal("empty catalog did not revoke models")
	}
	emptyEpoch := r.ClientRegistrationEpoch("account")
	if applied, ok := r.ReplaceClientModels("account", "antigravity", emptyEpoch, models); !ok || emptyEpoch == epoch || applied != emptyEpoch+1 {
		t.Fatal("empty catalog cannot recover")
	}
	epoch = r.ClientRegistrationEpoch("account")
	r.UnregisterClient("account")
	if _, ok := r.ReplaceClientModels("account", "antigravity", epoch, models); ok {
		t.Fatal("removed client resurrected")
	}
}
