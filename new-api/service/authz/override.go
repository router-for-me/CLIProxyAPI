package authz

import (
	"fmt"
	"sort"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/casbin/casbin/v2"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type overridePolicy struct {
	Resource string
	Action   string
	Effect   string
}

func SetUserPermissions(userID int, permissions PermissionsMap) error {
	e := currentEnforcer()
	if e == nil {
		return fmt.Errorf("authz enforcer is not initialized")
	}

	for resource, actions := range permissions {
		if !isKnownResource(resource) {
			continue
		}
		if _, err := e.RemoveFilteredPolicy(0, UserSubject(userID), resource); err != nil {
			return err
		}
		for _, policy := range userOverridePolicies(e, resource, actions) {
			if _, err := e.AddPolicy(UserSubject(userID), policy.Resource, policy.Action, policy.Effect); err != nil {
				return err
			}
		}
	}
	return nil
}

func SetUserPermissionsInTx(tx *gorm.DB, userID int, permissions PermissionsMap) error {
	e := currentEnforcer()
	if e == nil {
		return fmt.Errorf("authz enforcer is not initialized")
	}

	for resource, actions := range permissions {
		if !isKnownResource(resource) {
			continue
		}
		if err := tx.Where("ptype = ? AND v0 = ? AND v1 = ?", "p", UserSubject(userID), resource).Delete(&model.CasbinRule{}).Error; err != nil {
			return err
		}
		policies := userOverridePolicies(e, resource, actions)
		if len(policies) == 0 {
			continue
		}
		rules := make([]model.CasbinRule, 0, len(policies))
		for _, policy := range policies {
			rules = append(rules, newRule("p", []string{UserSubject(userID), policy.Resource, policy.Action, policy.Effect}))
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&rules).Error; err != nil {
			return err
		}
	}
	return nil
}

func ClearUserPermissions(userID int) error {
	e := currentEnforcer()
	if e == nil {
		return fmt.Errorf("authz enforcer is not initialized")
	}

	for _, resource := range registry {
		if _, err := e.RemoveFilteredPolicy(0, UserSubject(userID), resource.Resource); err != nil {
			return err
		}
	}
	return nil
}

func ClearUserPermissionsInTx(tx *gorm.DB, userID int) error {
	for _, resource := range registry {
		if err := tx.Where("ptype = ? AND v0 = ? AND v1 = ?", "p", UserSubject(userID), resource.Resource).Delete(&model.CasbinRule{}).Error; err != nil {
			return err
		}
	}
	return nil
}

func ClearUserAuthorization(userID int) error {
	return ClearUserPermissions(userID)
}

func ClearUserAuthorizationInTx(tx *gorm.DB, userID int) error {
	return ClearUserPermissionsInTx(tx, userID)
}

// ExplicitUserPermissions returns the effective permission matrix for the
// managed role plus any per-user overrides.
func ExplicitUserPermissions(userID int) PermissionsMap {
	return Capabilities(userID, common.RoleAdminUser)
}

// ExplicitUserOverrides returns only the per-user override entries.
func ExplicitUserOverrides(userID int) PermissionsMap {
	e := currentEnforcer()
	if e == nil {
		return PermissionsMap{}
	}

	result := PermissionsMap{}
	for _, resource := range registry {
		policies, err := e.GetFilteredPolicy(0, UserSubject(userID), resource.Resource)
		if err != nil {
			return PermissionsMap{}
		}
		actions := make(map[string]bool, len(policies))
		for _, policy := range policies {
			if len(policy) >= 3 && isKnownPermission(Permission{Resource: policy[1], Action: policy[2]}) {
				effect := policyEffect(policy)
				if effect == EffectAllow || effect == EffectDeny {
					actions[policy[2]] = effect == EffectAllow
				}
			}
		}
		if len(actions) > 0 {
			result[resource.Resource] = actions
		}
	}
	return result
}

// userOverridePolicies returns the override entries that differ from the managed
// role baseline; entries matching the baseline are omitted.
func userOverridePolicies(e *casbin.SyncedEnforcer, resource string, actions map[string]bool) []overridePolicy {
	overrides := make([]overridePolicy, 0, len(actions))
	for _, action := range catalogActions(resource) {
		desired, ok := actions[action.Action]
		if !ok {
			continue
		}
		permission := Permission{Resource: resource, Action: action.Action}
		if desired == roleBaselineAllows(e, managedRoleKey, permission) {
			continue
		}
		effect := EffectDeny
		if desired {
			effect = EffectAllow
		}
		overrides = append(overrides, overridePolicy{
			Resource: resource,
			Action:   action.Action,
			Effect:   effect,
		})
	}
	sort.Slice(overrides, func(i, j int) bool {
		return overrides[i].Action < overrides[j].Action
	})
	return overrides
}
