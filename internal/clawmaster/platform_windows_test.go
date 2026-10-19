//go:build windows

package clawmaster

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// privateTestDir is a directory only this user (and SYSTEM and Administrators) may use, as a user's
// profile directory is; the system temporary directory may grant others more.
func privateTestDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("icacls", dir, "/inheritance:r", "/grant:r", "*"+user.String()+":(OI)(CI)F", "*S-1-5-18:(OI)(CI)F", "*S-1-5-32-544:(OI)(CI)F").CombinedOutput()
	if err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	return dir
}

func TestWindowsPrivateFileAcceptsOnlyFilesOthersCannotRead(t *testing.T) {
	dir := privateTestDir(t)
	path := filepath.Join(dir, "client.key")
	if err := os.WriteFile(path, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwnedSecurity(sd, true); err != nil {
		t.Fatalf("a new file in a private directory (%s) fails the check: %v", sd, err)
	}
	f, err := openPrivateFile(path)
	if err != nil {
		t.Fatalf("a file only this user can read was refused: %v", err)
	}
	_ = f.Close()
	if err := ownedDirectory(dir, false, true); err != nil {
		t.Fatalf("a directory only this user can use was refused: %v", err)
	}
	if readableByOthers(path, nil) {
		t.Fatal("readableByOthers refused a private file")
	}

	for _, grant := range []string{"*S-1-1-0:(R)", "*S-1-5-32-545:(R)"} { // Everyone, Users
		if out, err := exec.Command("icacls", path, "/grant", grant).CombinedOutput(); err != nil {
			t.Fatalf("icacls: %v: %s", err, out)
		}
		if f, err := openPrivateFile(path); err == nil {
			_ = f.Close()
			t.Fatalf("a file readable through %s was accepted as private", grant)
		}
		if !readableByOthers(path, nil) {
			t.Fatalf("readableByOthers accepted a file readable through %s", grant)
		}
		if out, err := exec.Command("icacls", path, "/remove:g", grant[:len(grant)-4]).CombinedOutput(); err != nil {
			t.Fatalf("icacls: %v: %s", err, out)
		}
	}

	// A directory others may write is refused; one they may only read is refused only when private.
	if out, err := exec.Command("icacls", dir, "/grant", "*S-1-5-32-545:(OI)(CI)(RX)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	if err := ownedDirectory(dir, false, false); err != nil {
		t.Fatalf("a directory others may only read was refused as a shared directory: %v", err)
	}
	if err := ownedDirectory(dir, false, true); err == nil {
		t.Fatal("a directory others may read was accepted as private")
	}
	if out, err := exec.Command("icacls", dir, "/grant", "*S-1-5-32-545:(OI)(CI)(M)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	if err := ownedDirectory(dir, false, false); err == nil {
		t.Fatal("a directory others may change was accepted")
	}
}

func TestWindowsOpenNoFollowRefusesALink(t *testing.T) {
	dir := privateTestDir(t)
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("cannot create a symbolic link to test with (run as an administrator or in developer mode): %v", err)
	}
	if f, err := openNoFollow(link, os.O_RDONLY, 0); err == nil {
		_ = f.Close()
		t.Fatal("openNoFollow followed a symbolic link")
	}
	f, err := openNoFollow(target, os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := openNoFollow(filepath.Join(dir, "missing"), os.O_RDONLY, 0); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a missing file = %v, want ErrNotExist", err)
	}
}

func TestWindowsFileLockIsExclusive(t *testing.T) {
	path := filepath.Join(privateTestDir(t), "profile.lock")
	first, err := openNoFollow(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = first.Close() }()
	second, err := openNoFollow(path, os.O_RDWR, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = second.Close() }()
	if err := tryLockFile(first, true); err != nil {
		t.Fatal(err)
	}
	if err := tryLockFile(second, false); !errors.Is(err, errLockBusy) {
		t.Fatalf("a second lock while the first is held = %v, want errLockBusy", err)
	}
	_ = first.Close()
	if err := tryLockFile(second, true); err != nil {
		t.Fatalf("the lock is still held after its handle closed: %v", err)
	}
}

func TestWindowsRefusesProfiles(t *testing.T) {
	if _, err := OpenProfile("claude-test", false); err == nil {
		t.Fatal("OpenProfile succeeded on Windows")
	}
}

// TestMain runs the tests with a temporary directory private to this user, as a user's own
// (%LOCALAPPDATA%\Temp) is; the system's temporary directory lets other users in.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "clawmaster-test-")
	if err == nil {
		err = restrictToOwner(dir)
	}
	if err != nil {
		println("cannot make a private temporary directory:", err.Error())
		os.Exit(2)
	}
	_ = os.Setenv("TMP", dir)
	_ = os.Setenv("TEMP", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

// loosenForTest lets Everyone read path.
func loosenForTest(t *testing.T, path string) {
	t.Helper()
	if out, err := exec.Command("icacls", path, "/grant", "*S-1-1-0:(R)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
}

// writeFakeClaude puts a `claude` that exits 0 in bin.
func writeFakeClaude(t *testing.T, bin string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, "claude.cmd"), []byte("@exit /b 0\r\n"), 0o700); err != nil {
		t.Fatal(err)
	}
}

func systemPathForTest() string { return filepath.Join(os.Getenv("SystemRoot"), "System32") }

// makeReadOnlyForTest lets this user create or change nothing in dir until the returned func runs.
func makeReadOnlyForTest(t *testing.T, dir string) func() {
	t.Helper()
	user, err := currentUserSID()
	if err != nil {
		t.Fatal(err)
	}
	deny := "*" + user.String() + ":(W,D,DC)"
	if out, err := exec.Command("icacls", dir, "/deny", deny).CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	return func() { _ = exec.Command("icacls", dir, "/remove:d", "*"+user.String()).Run() }
}

// writeGymShim puts a `claude` in bin that runs this test binary's gym helper.
func writeGymShim(t *testing.T, bin, binaryEnv string) {
	t.Helper()
	shim := "@\"%" + binaryEnv + "%\" -test.run=TestForwarderGymHelper -test.count=1 -- %*\r\n@exit /b %ERRORLEVEL%\r\n"
	if err := os.WriteFile(filepath.Join(bin, "claude.cmd"), []byte(shim), 0o700); err != nil {
		t.Fatal(err)
	}
}

// setHomeForTest makes home the user's home directory for this test: Windows reads USERPROFILE.
func setHomeForTest(t *testing.T, home string) {
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

// requirePrivateFile fails unless only this user (and SYSTEM and Administrators) can open path.
func requirePrivateFile(t *testing.T, path string) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || readableByOthers(path, info) {
		t.Fatalf("%s can be read by others", path)
	}
}

// The parent here passes read access for Everyone to new files; a private file must not get it,
// not even for the instant before its permissions are replaced.
func TestWindowsPrivateFilesArePrivateFromCreation(t *testing.T) {
	dir := privateTestDir(t)
	if out, err := exec.Command("icacls", dir, "/grant", "*S-1-1-0:(OI)(CI)(R)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	f, err := createPrivateFile(filepath.Join(dir, "client.key"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sd, err := windows.GetSecurityInfo(windows.Handle(f.Fd()), windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	if err := checkOwnedSecurity(sd, true); err != nil {
		t.Fatalf("a file just created is not private (%s): %v", sd, err)
	}
	sub := filepath.Join(dir, "sub")
	if err := mkdirPrivate(sub); err != nil {
		t.Fatal(err)
	}
	if err := ownedDirectory(sub, false, true); err != nil {
		t.Fatalf("a directory just created is not private: %v", err)
	}
}

// A private directory that passes access for others on to what is created in it is not private.
func TestWindowsPrivateDirectoryRefusesInheritableGrants(t *testing.T) {
	dir := privateTestDir(t)
	if err := ownedDirectory(dir, false, true); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", dir, "/grant", "*S-1-1-0:(OI)(CI)(IO)(R)").CombinedOutput(); err != nil {
		t.Fatalf("icacls: %v: %s", err, out)
	}
	if err := ownedDirectory(dir, false, true); err == nil {
		t.Fatal("a directory whose new files Everyone may read was accepted as private")
	}
}

func TestWindowsManagedSettingsAreReadFromClaudeCodesFixedPath(t *testing.T) {
	t.Setenv("ProgramFiles", `D:\elsewhere`)
	if got := managedSettingsDir(); got != `C:\Program Files\ClaudeCode` {
		t.Fatalf("managed settings directory = %s, want C:\\Program Files\\ClaudeCode", got)
	}
}
