package vertex

import "github.com/QuantumNous/new-api/common"

func GetModelRegion(other string, localModelName string) string {
	// if other is json string
	if common.IsJsonObject(other) {
		m, err := common.StrToMap(other)
		if err != nil {
			return other // return original if parsing fails
		}
		if m[localModelName] != nil {
			return m[localModelName].(string)
		} else {
			if v, ok := m["default"]; ok {
				return v.(string)
			}
			return "global"
		}
	}
	return other
}
