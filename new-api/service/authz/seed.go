package authz

import (
	"fmt"

	"github.com/QuantumNous/new-api/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func seedBuiltInRoles(db *gorm.DB) error {
	for _, spec := range builtInRoles {
		role := model.AuthzRole{
			Key:         spec.Key,
			Name:        spec.Name,
			Description: spec.Description,
			BuiltIn:     spec.BuiltIn,
			Enabled:     true,
			Sort:        spec.Sort,
		}
		if err := db.Clauses(clause.OnConflict{
			Columns: []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{
				"name",
				"description",
				"built_in",
				"enabled",
				"sort",
			}),
		}).Create(&role).Error; err != nil {
			return err
		}
	}
	return nil
}

func resetBuiltInRolePolicies(db *gorm.DB) error {
	subjects := make([]string, 0, len(builtInRoles))
	for _, spec := range builtInRoles {
		subjects = append(subjects, RoleSubject(spec.Key))
	}
	// Scoped legacy rules must survive baseline reseeding so the adapter can
	// retain their restrictions instead of replacing them with global grants.
	return db.Where("ptype = ? AND v0 IN ?", "p", subjects).
		Where("(v4 = ? OR v4 IS NULL) AND (v5 = ? OR v5 IS NULL)", "", "").
		Delete(&model.CasbinRule{}).Error
}

func seedDefaultPolicies() error {
	e := currentEnforcer()
	if e == nil {
		return fmt.Errorf("authz enforcer is not initialized")
	}

	for _, spec := range builtInRoles {
		if spec.Superuser {
			continue
		}
		for _, permission := range PermissionsForRole(spec.Key) {
			if _, err := e.AddPolicy(RoleSubject(spec.Key), permission.Resource, permission.Action, EffectAllow); err != nil {
				return err
			}
		}
	}
	return nil
}
