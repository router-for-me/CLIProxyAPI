//go:build windows

package helps

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestClaudeCodeCompactionWindowsACLIsCurrentUserOnly(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := filepath.Join(t.TempDir(), "state")
	key, _ := NewClaudeCodeCompactionLaneKey("acl-session", "model", "auth")
	lane := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	if _, err := lane.Commit(ClaudeCodeCompactionState{EnvelopeHash: "secured"}); err != nil {
		lane.Unlock()
		t.Fatalf("persist secured state: %v", err)
	}
	lane.Unlock()

	assertClaudeCodeCompactionCurrentUserOnlyACL(t, stateDir, true)
	statePath := claudeCodeCompactionStatePath(stateDir, key)
	if _, err := os.Stat(statePath); err != nil {
		t.Fatalf("stat persisted state: %v", err)
	}
	assertClaudeCodeCompactionCurrentUserOnlyACL(t, statePath, false)
}

func assertClaudeCodeCompactionCurrentUserOnlyACL(t *testing.T, path string, directory bool) {
	t.Helper()
	const fileAllAccess windows.ACCESS_MASK = 0x1f01ff
	descriptor, err := windows.GetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION,
	)
	if err != nil {
		t.Fatalf("read ACL for %q: %v", path, err)
	}
	control, _, err := descriptor.Control()
	if err != nil {
		t.Fatalf("read security descriptor control for %q: %v", path, err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("ACL for %q still inherits from its parent", path)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("read DACL for %q: %v", path, err)
	}
	if dacl == nil || dacl.AceCount == 0 {
		if dacl == nil {
			t.Fatalf("DACL for %q is nil", path)
		}
		t.Fatalf("DACL for %q has no entries", path)
	}
	currentUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("get current process user: %v", err)
	}
	var inheritance uint8
	hasEffectiveEntry := false
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			t.Fatalf("read DACL entry %d for %q: %v", i, path, err)
		}
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE {
			t.Fatalf("DACL entry %d for %q has type %d, want allow", i, path, ace.Header.AceType)
		}
		aceSID := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		if !aceSID.Equals(currentUser.User.Sid) {
			t.Fatalf("DACL entry %d for %q belongs to %s, want current user %s", i, path, aceSID.String(), currentUser.User.Sid.String())
		}
		if ace.Mask&windows.GENERIC_ALL == 0 && ace.Mask&fileAllAccess != fileAllAccess {
			t.Fatalf("DACL entry %d for %q has mask %#x without full control", i, path, ace.Mask)
		}
		if ace.Header.AceFlags&windows.INHERITED_ACE != 0 {
			t.Fatalf("DACL entry %d for %q is inherited", i, path)
		}
		if ace.Header.AceFlags&windows.INHERIT_ONLY_ACE == 0 {
			hasEffectiveEntry = true
		}
		inheritance |= ace.Header.AceFlags & (windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE)
	}
	if !hasEffectiveEntry {
		t.Fatalf("DACL for %q has no entry that applies to the object itself", path)
	}
	wantInheritance := uint8(0)
	if directory {
		wantInheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if inheritance != wantInheritance {
		t.Fatalf("DACL for %q inheritance flags = %#x, want %#x", path, inheritance, wantInheritance)
	}
}
