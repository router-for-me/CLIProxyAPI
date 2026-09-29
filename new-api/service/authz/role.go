package authz

const (
	BuiltInRoleRoot  = "root"
	BuiltInRoleAdmin = "admin"
)

// RoleSpec describes a role. A superuser role is allowed every permission
// without an explicit policy entry.
type RoleSpec struct {
	Key         string
	Name        string
	Description string
	BuiltIn     bool
	Superuser   bool
	Sort        int
}

var builtInRoles = []RoleSpec{
	{
		Key:         BuiltInRoleRoot,
		Name:        "Root",
		Description: "Built-in root authorization role",
		BuiltIn:     true,
		Superuser:   true,
		Sort:        0,
	},
	{
		Key:         BuiltInRoleAdmin,
		Name:        "Admin",
		Description: "Built-in admin authorization role",
		BuiltIn:     true,
		Superuser:   false,
		Sort:        10,
	},
}

// RoleDescriptor exposes a role together with its baseline grant matrix.
type RoleDescriptor struct {
	Key       string         `json:"key"`
	Name      string         `json:"name"`
	BuiltIn   bool           `json:"built_in"`
	Superuser bool           `json:"superuser"`
	Grants    PermissionsMap `json:"grants"`
}

// Roles returns the role descriptors with their baseline grants.
func Roles() []RoleDescriptor {
	result := make([]RoleDescriptor, 0, len(builtInRoles))
	for _, spec := range builtInRoles {
		result = append(result, RoleDescriptor{
			Key:       spec.Key,
			Name:      spec.Name,
			BuiltIn:   spec.BuiltIn,
			Superuser: spec.Superuser,
			Grants:    roleGrants(spec),
		})
	}
	return result
}

func roleGrants(spec RoleSpec) PermissionsMap {
	grants := make(PermissionsMap, len(registry))
	for _, resource := range registry {
		actions := make(map[string]bool, len(resource.Actions))
		for _, action := range resource.Actions {
			actions[action.Action] = spec.Superuser || actionHasRole(action, spec.Key)
		}
		grants[resource.Resource] = actions
	}
	return grants
}

func roleSpec(roleKey string) (RoleSpec, bool) {
	for _, spec := range builtInRoles {
		if spec.Key == roleKey {
			return spec, true
		}
	}
	return RoleSpec{}, false
}

func isSuperuserRole(roleKey string) bool {
	spec, ok := roleSpec(roleKey)
	return ok && spec.Superuser
}
