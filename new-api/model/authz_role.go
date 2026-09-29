package model

type AuthzRole struct {
	Id          uint   `json:"id" gorm:"primaryKey;autoIncrement"`
	Key         string `json:"key" gorm:"size:64;uniqueIndex;not null"`
	Name        string `json:"name" gorm:"size:100;not null"`
	Description string `json:"description" gorm:"type:text"`
	BuiltIn     bool   `json:"built_in"`
	Enabled     bool   `json:"enabled"`
	Sort        int    `json:"sort"`
	CreatedAt   int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt   int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

func (AuthzRole) TableName() string {
	return "authz_roles"
}
