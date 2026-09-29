package model

import (
	"database/sql/driver"
	"encoding/json"
	"fmt"

	"github.com/QuantumNous/new-api/common"
)

// AuditOther is the structured metadata stored with an audit event. Privileged
// fields are separate so API projections can remove them without re-encoding JSON.
type AuditOther struct {
	Op          *AuditOperation   `json:"op,omitempty"`
	AdminInfo   *AuditAdminInfo   `json:"admin_info,omitempty"`
	AuditInfo   *AuditRequestInfo `json:"audit_info,omitempty"`
	RootInfo    AuditFields       `json:"root_info,omitempty"`
	LoginMethod string            `json:"login_method,omitempty"`
	UserAgent   string            `json:"user_agent,omitempty"`
}

type AuditOperation struct {
	Action string      `json:"action"`
	Params AuditFields `json:"params,omitempty"`
}

type AuditAdminInfo struct {
	AdminID       int    `json:"admin_id,omitempty"`
	AdminUsername string `json:"admin_username,omitempty"`
	AdminRole     int    `json:"admin_role,omitempty"`
	AuthMethod    string `json:"auth_method,omitempty"`
}

type AuditRequestInfo struct {
	Method  string            `json:"method"`
	Route   string            `json:"route"`
	Path    string            `json:"path"`
	Status  int               `json:"status"`
	Success bool              `json:"success"`
	Params  map[string]string `json:"params,omitempty"`
}

// AuditFields holds action-specific parameters and root-only extensions.
// Retain their encoded values when reading so arbitrary nested integers do not
// round-trip through float64 and lose precision before the API returns them.
type AuditFields map[string]any

func (fields *AuditFields) UnmarshalJSON(data []byte) error {
	var values map[string]json.RawMessage
	if err := common.Unmarshal(data, &values); err != nil {
		return err
	}
	if values == nil {
		*fields = nil
		return nil
	}
	decoded := make(AuditFields, len(values))
	for key, value := range values {
		decoded[key] = value
	}
	*fields = decoded
	return nil
}

func (other AuditOther) Value() (driver.Value, error) {
	data, err := common.Marshal(other)
	if err != nil {
		return nil, err
	}
	// PostgreSQL's simple protocol requires string, not a bytea parameter.
	return string(data), nil
}

func (other *AuditOther) Scan(value any) error {
	var data []byte
	switch value := value.(type) {
	case nil:
		*other = AuditOther{}
		return nil
	case string:
		data = []byte(value)
	case []byte:
		data = value
	default:
		return fmt.Errorf("unsupported audit metadata database type %T", value)
	}
	var decoded AuditOther
	if len(data) > 0 {
		if err := common.Unmarshal(data, &decoded); err != nil {
			return err
		}
	}
	*other = decoded
	return nil
}
