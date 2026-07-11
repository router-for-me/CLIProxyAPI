//go:build windows

package helps

import (
	"fmt"

	"golang.org/x/sys/windows"
)

func secureClaudeCodeCompactionStateDirectory(path string) error {
	return secureClaudeCodeCompactionWindowsPath(path, windows.SUB_CONTAINERS_AND_OBJECTS_INHERIT)
}

func secureClaudeCodeCompactionStateFile(path string) error {
	return secureClaudeCodeCompactionWindowsPath(path, windows.NO_INHERITANCE)
}

func secureClaudeCodeCompactionWindowsPath(path string, inheritance uint32) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return fmt.Errorf("get current process user: %w", err)
	}
	acl, err := windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{{
		AccessPermissions: windows.GENERIC_ALL,
		AccessMode:        windows.SET_ACCESS,
		Inheritance:       inheritance,
		Trustee: windows.TRUSTEE{
			TrusteeForm:  windows.TRUSTEE_IS_SID,
			TrusteeType:  windows.TRUSTEE_IS_USER,
			TrusteeValue: windows.TrusteeValueFromSID(user.User.Sid),
		},
	}}, nil)
	if err != nil {
		return fmt.Errorf("build current-user access list: %w", err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		acl,
		nil,
	); err != nil {
		return fmt.Errorf("set protected current-user access list: %w", err)
	}
	return nil
}
