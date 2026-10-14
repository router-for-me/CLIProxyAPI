package codex

import "testing"

func TestCredentialRefreshPatchChecksPartitionsIndependently(t *testing.T) {
	for _, rotated := range []string{"outer", "siwc"} {
		t.Run(rotated, func(t *testing.T) {
			before := map[string]any{
				"access_token": "outer-old", "refresh_token": "outer-ref-old", "last_refresh": "2026-10-12T12:00:00Z",
				"siwc": map[string]any{"access_token": "siwc-old", "refresh_token": "siwc-ref-old", "last_refresh": "2026-10-12T12:00:00Z"},
			}
			after := cloneStringAnyMap(before)
			after["access_token"], after["refresh_token"] = "outer-new", "outer-ref-new"
			after["siwc"].(map[string]any)["access_token"] = "siwc-new"
			after["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-new"
			patch := NewCredentialRefreshPatch(before, after, true, true)
			disk := cloneStringAnyMap(before)
			wantOuter, wantSIWC := "outer-ref-new", "siwc-ref-new"
			if rotated == "outer" {
				disk["access_token"], disk["refresh_token"] = "outer-bound", "outer-ref-bound"
				wantOuter = "outer-ref-bound"
			} else {
				disk["siwc"].(map[string]any)["access_token"] = "siwc-bound"
				disk["siwc"].(map[string]any)["refresh_token"] = "siwc-ref-bound"
				wantSIWC = "siwc-ref-bound"
			}
			// Changing source/result maps later must not mutate the immutable patch.
			before["refresh_token"] = "mutated-before"
			after["refresh_token"] = "mutated-after"
			saved := patch.Apply(disk)
			if saved["refresh_token"] != wantOuter || saved["siwc"].(map[string]any)["refresh_token"] != wantSIWC {
				t.Fatalf("partition version check lost a valid refresh or overwrote a bind: %#v", saved)
			}
		})
	}
}
