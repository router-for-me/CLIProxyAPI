package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
)

func getString(m map[string]any, key string) string {
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func getBool(m map[string]any, key string) bool {
	v, ok := m[key]
	if !ok || v == nil {
		return false
	}
	switch typed := v.(type) {
	case bool:
		return typed
	case string:
		if parsed, err := strconv.ParseBool(typed); err == nil {
			return parsed
		}
	case int:
		return typed != 0
	case int64:
		return typed != 0
	case int32:
		return typed != 0
	case uint:
		return typed != 0
	case uint64:
		return typed != 0
	case float64:
		return typed != 0
	case float32:
		return typed != 0
	case json.Number:
		if parsed, err := strconv.ParseBool(typed.String()); err == nil {
			return parsed
		}
		if parsedFloat, err := typed.Float64(); err == nil {
			return parsedFloat != 0
		}
	}
	return false
}

func getFloat(m map[string]any, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch typed := v.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case int32:
		return float64(typed)
	case int16:
		return float64(typed)
	case int8:
		return float64(typed)
	case uint:
		return float64(typed)
	case uint64:
		return float64(typed)
	case uint32:
		return float64(typed)
	case uint16:
		return float64(typed)
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		if err != nil {
			return 0
		}
		return parsed
	case json.Number:
		parsed, err := typed.Float64()
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}
