package model

type CasbinRule struct {
	Id    uint   `gorm:"primaryKey;autoIncrement"`
	Ptype string `gorm:"size:100;index:idx_casbin_rule,priority:1;uniqueIndex:idx_casbin_rule_unique,priority:1"`
	V0    string `gorm:"size:100;index:idx_casbin_rule,priority:2;uniqueIndex:idx_casbin_rule_unique,priority:2"`
	V1    string `gorm:"size:100;index:idx_casbin_rule,priority:3;uniqueIndex:idx_casbin_rule_unique,priority:3"`
	V2    string `gorm:"size:100;index:idx_casbin_rule,priority:4;uniqueIndex:idx_casbin_rule_unique,priority:4"`
	V3    string `gorm:"size:100;index:idx_casbin_rule,priority:5;uniqueIndex:idx_casbin_rule_unique,priority:5"`
	V4    string `gorm:"size:100;index:idx_casbin_rule,priority:6;uniqueIndex:idx_casbin_rule_unique,priority:6"`
	V5    string `gorm:"size:100;index:idx_casbin_rule,priority:7;uniqueIndex:idx_casbin_rule_unique,priority:7"`
}

func (CasbinRule) TableName() string {
	return "casbin_rule"
}
