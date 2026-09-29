package authz

import "strconv"

// Permission identifies a single action on a resource.
type Permission struct {
	Resource string
	Action   string
}

// PermissionsMap is a resource -> action -> allowed lookup.
type PermissionsMap map[string]map[string]bool

const (
	EffectAllow = "allow"
	EffectDeny  = "deny"
)

// UserSubject is the casbin subject string for a single user.
func UserSubject(userID int) string {
	return "user:" + strconv.Itoa(userID)
}

// RoleSubject is the casbin subject string for a role.
func RoleSubject(roleKey string) string {
	return "role:" + roleKey
}
