package management

import (
	"fmt"
	"strings"
	"time"
)

func parseUsageTime(raw string) (time.Time, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}, fmt.Errorf("time value cannot be empty")
	}
	if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return parsed, nil
	}
	if parsed, err := time.ParseInLocation(usageStatsDateLayout, raw, time.UTC); err == nil {
		return parsed, nil
	}
	return time.Time{}, fmt.Errorf("expected RFC3339 or YYYY-MM-DD")
}
