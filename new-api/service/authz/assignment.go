package authz

import "github.com/QuantumNous/new-api/common"

// resolveSubjectRoles returns the role keys assigned to a subject. The mapping
// is derived from the caller's system role.
var resolveSubjectRoles = func(userID int, systemRole int) []string {
	switch {
	case systemRole >= common.RoleRootUser:
		return []string{BuiltInRoleRoot}
	case systemRole >= common.RoleAdminUser:
		return []string{BuiltInRoleAdmin}
	default:
		return nil
	}
}

// managedRoleKey is the role whose baseline per-user overrides are expressed
// relative to.
const managedRoleKey = BuiltInRoleAdmin
