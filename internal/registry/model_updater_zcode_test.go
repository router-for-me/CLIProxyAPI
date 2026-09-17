package registry

import "testing"

// The remote catalog does not publish zcode; a refresh must not drop it while
// still allowing a genuine upstream change to managed sections to apply.
func TestCarryOverLocalOnlySections(t *testing.T) {
	local := &staticModelsJSON{
		ZCode:  []*ModelInfo{{ID: "glm-5.3"}, {ID: "glm-5.3-flash"}},
		Claude: []*ModelInfo{{ID: "claude-old"}},
	}
	remote := &staticModelsJSON{
		Claude: []*ModelInfo{{ID: "claude-new"}},
	}

	carryOverLocalOnlySections(local, remote)

	if len(remote.ZCode) != 2 || remote.ZCode[0].ID != "glm-5.3" {
		t.Fatalf("zcode section not preserved: %+v", remote.ZCode)
	}
	if len(remote.Claude) != 1 || remote.Claude[0].ID != "claude-new" {
		t.Fatalf("managed section must reflect the remote: %+v", remote.Claude)
	}

	// A remote that DOES publish zcode wins.
	remoteWithZCode := &staticModelsJSON{ZCode: []*ModelInfo{{ID: "glm-remote"}}}
	carryOverLocalOnlySections(local, remoteWithZCode)
	if remoteWithZCode.ZCode[0].ID != "glm-remote" {
		t.Fatalf("remote zcode must win: %+v", remoteWithZCode.ZCode)
	}
}
