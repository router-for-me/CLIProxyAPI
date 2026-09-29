package kitutil

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

func GetPointer[T any](v T) *T {
	return &v
}

func Interface2String(inter any) string {
	switch inter.(type) {
	case string:
		return inter.(string)
	case int:
		return fmt.Sprintf("%d", inter.(int))
	case float64:
		return strconv.FormatFloat(inter.(float64), 'f', -1, 64)
	case bool:
		if inter.(bool) {
			return "true"
		} else {
			return "false"
		}
	case nil:
		return ""
	}
	return fmt.Sprintf("%v", inter)
}

func String2Int(str string) int {
	num, err := strconv.Atoi(str)
	if err != nil {
		return 0
	}
	return num
}

func GetUUID() string {
	code := uuid.New().String()
	code = strings.Replace(code, "-", "", -1)
	return code
}

func GetTimestamp() int64 {
	return time.Now().Unix()
}
