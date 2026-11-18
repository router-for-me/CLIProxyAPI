package auth

import "testing"

type patchSupportExecutor struct {
	*replaceAwareExecutor
	supported bool
}

func (e *patchSupportExecutor) SupportsApplyPatch() bool { return e.supported }

func TestApplyPatchManagerAllCandidates(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.RegisterExecutor(&patchSupportExecutor{&replaceAwareExecutor{id: "custom-name"}, true})
	m.RegisterExecutor(&patchSupportExecutor{&replaceAwareExecutor{id: "denied"}, false})
	m.RegisterExecutor(&replaceAwareExecutor{id: "codex"})
	for _, tc := range []struct {
		providers []string
		want      bool
	}{
		{nil, false}, {[]string{"unknown"}, false}, {[]string{"codex"}, false},
		{[]string{"custom-name"}, true}, {[]string{"custom-name", "custom-name"}, true},
		{[]string{"custom-name", "denied"}, false}, {[]string{"custom-name", "unknown"}, false},
		{[]string{"custom-name", "codex"}, false},
	} {
		if got := m.SupportsApplyPatchForProviders(tc.providers); got != tc.want {
			t.Errorf("providers %v: got %v, want %v", tc.providers, got, tc.want)
		}
	}
	var absent *Manager
	if absent.SupportsApplyPatchForProviders([]string{"custom-name"}) {
		t.Fatal("nil manager has capability")
	}
}
