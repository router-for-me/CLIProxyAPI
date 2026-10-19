//go:build windows

package clawmaster

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errLockBusy reports a file lock another process holds.
var errLockBusy = errors.New("the lock is held by another process")

// openNoFollow opens path without following a final symbolic link or junction: the handle is opened
// on the reparse point itself and refused if that is what it names.
func openNoFollow(path string, flag int, perm os.FileMode) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	access := uint32(windows.GENERIC_READ)
	switch flag & (os.O_RDONLY | os.O_WRONLY | os.O_RDWR) {
	case os.O_WRONLY:
		access = windows.GENERIC_WRITE
	case os.O_RDWR:
		access = windows.GENERIC_READ | windows.GENERIC_WRITE
	}
	disposition := uint32(windows.OPEN_EXISTING)
	switch {
	case flag&os.O_CREATE != 0 && flag&os.O_EXCL != 0:
		disposition = windows.CREATE_NEW
	case flag&os.O_CREATE != 0:
		disposition = windows.OPEN_ALWAYS
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := windows.CreateFile(name, access, share, nil, disposition, windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, &fs.PathError{Op: "open", Path: path, Err: err}
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		_ = windows.CloseHandle(handle)
		return nil, &fs.PathError{Op: "open", Path: path, Err: errors.New("is a symbolic link or junction")}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// tryLockFile takes a shared or exclusive lock on f without waiting; errLockBusy if another
// process holds a conflicting one. Closing f releases it.
func tryLockFile(f *os.File, exclusive bool) error {
	flags := uint32(windows.LOCKFILE_FAIL_IMMEDIATELY)
	if exclusive {
		flags |= windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	err := windows.LockFileEx(windows.Handle(f.Fd()), flags, 0, 1, 0, new(windows.Overlapped))
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
		return errLockBusy
	}
	return err
}

// fileInode has no cheap equivalent in a Windows FileInfo; callers also compare size and time.
func fileInode(os.FileInfo) uint64 { return 0 }

const (
	fileDeleteChild    = 0x40 // FILE_DELETE_CHILD
	systemAuditACEType = 2    // SYSTEM_AUDIT_ACE_TYPE
)

// Rights that let a holder change a file or directory, or its entries.
const windowsWriteRights = windows.FILE_WRITE_DATA | windows.FILE_APPEND_DATA | windows.FILE_WRITE_EA |
	fileDeleteChild | windows.FILE_WRITE_ATTRIBUTES | windows.DELETE | windows.WRITE_DAC |
	windows.WRITE_OWNER | windows.GENERIC_WRITE | windows.GENERIC_ALL

// checkOwnedDirectory requires a directory this user owns that others cannot change; private, that
// grants others nothing at all. SYSTEM and Administrators may hold rights, as they do on every
// directory in a user's profile.
func checkOwnedDirectory(abs string, _ os.FileInfo, private bool) error {
	sd, err := windows.GetNamedSecurityInfo(abs, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return errors.New("cannot inspect the directory's owner and permissions")
	}
	if err := checkOwnedSecurity(sd, private); err != nil {
		return errors.New("profile directories must be owned by you, and others may not change them (private directories: no access for others)")
	}
	return nil
}

// privateFile requires an open file this user owns, regular, with one link, that grants others no
// access.
func privateFile(f *os.File) error {
	handle := windows.Handle(f.Fd())
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return errors.New("cannot inspect private file")
	}
	const notRegular = windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT | windows.FILE_ATTRIBUTE_DEVICE
	sd, err := windows.GetSecurityInfo(handle, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	if err != nil || info.FileAttributes&notRegular != 0 || info.NumberOfLinks != 1 || checkOwnedSecurity(sd, true) != nil {
		return errors.New("private files must be owned by you, regular, unlinked, and give others no access")
	}
	return nil
}

// readableByOthers reports a private file whose owner is someone else or whose DACL lets others read it.
func readableByOthers(path string, _ fs.FileInfo) bool {
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION)
	return err != nil || checkOwnedSecurity(sd, true) != nil
}

// privateSecurityAttributes is the owner-only descriptor restrictToOwner applies, for creating a
// file or directory that is private from its first instant: no handle can be opened on it under
// the parent's inherited permissions before they are replaced.
func privateSecurityAttributes() (*windows.SecurityAttributes, error) {
	user, err := currentUserSID()
	if err != nil {
		return nil, err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.String() + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		return nil, err
	}
	return &windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}, nil
}

// createPrivateFile creates path, which must not exist, for reading and writing, private to this user.
func createPrivateFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: path, Err: err}
	}
	sa, err := privateSecurityAttributes()
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: path, Err: err}
	}
	share := uint32(windows.FILE_SHARE_READ | windows.FILE_SHARE_WRITE | windows.FILE_SHARE_DELETE)
	handle, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, share, sa, windows.CREATE_NEW, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &fs.PathError{Op: "create", Path: path, Err: err}
	}
	return os.NewFile(uintptr(handle), path), nil
}

// mkdirPrivate creates the directory path private to this user.
func mkdirPrivate(path string) error {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	sa, err := privateSecurityAttributes()
	if err != nil {
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	if err := windows.CreateDirectory(name, sa); err != nil {
		return &fs.PathError{Op: "mkdir", Path: path, Err: err}
	}
	return nil
}

// restrictToOwner gives path a protected DACL, as mode 0600 or 0700 does on Unix: full control for
// this user, SYSTEM and Administrators (who hold it on every user's profile anyway), nothing
// inherited from the parent, and the same for anything later created inside.
func restrictToOwner(path string) error {
	sa, err := privateSecurityAttributes()
	if err != nil {
		return err
	}
	dacl, _, err := sa.SecurityDescriptor.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// executable: Windows has no execute bit; what runs is decided by the extension (claude.exe).
func executable(os.FileInfo) bool { return true }

// stopChild ends the native Claude. Windows cannot deliver SIGTERM to another process.
func stopChild(p *os.Process) error { return p.Kill() }

// directorySyncSupported: Windows cannot fsync a directory; NTFS journals its entries itself.
const directorySyncSupported = false

// syncReadOnlyFile makes f's contents durable. Windows flushes only through a handle that may
// write, so it opens one on the same file (refusing a link, as f was opened).
func syncReadOnlyFile(f *os.File) error {
	w, err := openNoFollow(f.Name(), os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	errSync := w.Sync()
	if err := w.Close(); errSync == nil {
		errSync = err
	}
	return errSync
}

// parentExited reports when the process that started this one ends (conhost, for the scheduled task).
func parentExited() (<-chan struct{}, error) {
	handle, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(os.Getppid()))
	if err != nil {
		return nil, fmt.Errorf("cannot open the parent process: %w", err)
	}
	gone := make(chan struct{})
	go func() {
		defer func() { _ = windows.CloseHandle(handle) }()
		_, _ = windows.WaitForSingleObject(handle, windows.INFINITE)
		close(gone)
	}()
	return gone, nil
}

// currentUserSIDString is this user's SID, for the scheduled task's trigger and principal.
func currentUserSIDString() (string, error) {
	sid, err := currentUserSID()
	if err != nil {
		return "", err
	}
	return sid.String(), nil
}

// profilesSupported: profiles hold the pool's subscription credentials, which only a server keeps.
func profilesSupported() error {
	return errors.New("profiles (login, run, serve) are not supported on Windows; on Windows use connect or agent")
}

// checkOwnedSecurity requires an owner that is this user, or Administrators when this user is one
// (an elevated administrator's new files are owned by the group, and any member may take ownership
// anyway), and a DACL in which every other trustee is SYSTEM, Administrators, or, unless private,
// holds no right to change the object.
func checkOwnedSecurity(sd *windows.SECURITY_DESCRIPTOR, private bool) error {
	owner, _, err := sd.Owner()
	if err != nil || owner == nil {
		return errors.New("no owner")
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	user := tokenUser.User.Sid
	system, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	creatorOwner, err := windows.CreateWellKnownSid(windows.WinCreatorOwnerSid)
	if err != nil {
		return err
	}
	if !owner.Equals(user) {
		member, err := windows.Token(0).IsMember(admins)
		if err != nil || !owner.Equals(admins) || !member {
			return fmt.Errorf("owned by %s, not this user %s", owner, user)
		}
	}
	dacl, _, err := sd.DACL()
	if err != nil || dacl == nil {
		return errors.New("no DACL: everyone has full access")
	}
	for i := uint32(0); i < uint32(dacl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(dacl, i, &ace); err != nil {
			return err
		}
		switch ace.Header.AceType {
		case windows.ACCESS_DENIED_ACE_TYPE, systemAuditACEType:
			continue
		case windows.ACCESS_ALLOWED_ACE_TYPE:
		default:
			return fmt.Errorf("an access entry of type %d this check does not understand", ace.Header.AceType)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart))
		// Inheritable entries count too: what is created inside gets them. CREATOR OWNER becomes
		// whoever creates the child.
		if sid.Equals(user) || sid.Equals(owner) || sid.Equals(system) || sid.Equals(admins) || sid.Equals(creatorOwner) {
			continue
		}
		if private || ace.Mask&windowsWriteRights != 0 {
			return fmt.Errorf("%s has access %#x", sid, ace.Mask)
		}
	}
	return nil
}

// currentUserSID is this process's user.
func currentUserSID() (*windows.SID, error) {
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, err
	}
	return tokenUser.User.Sid, nil
}
