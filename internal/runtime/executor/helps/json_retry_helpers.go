package helps

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

var (
	reRetryDelaySeconds = regexp.MustCompile(`(?:after|resets in)\s+(\d+)s\.?`)
	reRetryDelayHuman   = regexp.MustCompile(`(?:after|resets in)\s+((?:\d+h)?(?:\d+m)?(?:\d+s)?)\.?`)
)

// DeleteJSONField removes a top-level or nested JSON field from a payload.
func DeleteJSONField(body []byte, key string) []byte {
	if key == "" || len(body) == 0 {
		return body
	}
	updated, err := sjson.DeleteBytes(body, key)
	if err != nil {
		return body
	}
	return updated
}

// ParseRetryDelay extracts the retry delay from a Google API 429 error response.
func ParseRetryDelay(errorBody []byte) (*time.Duration, error) {
	details := gjson.GetBytes(errorBody, "error.details")
	if details.Exists() && details.IsArray() {
		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.RetryInfo" {
				continue
			}
			retryDelay := detail.Get("retryDelay").String()
			if retryDelay == "" {
				continue
			}
			duration, err := time.ParseDuration(retryDelay)
			if err != nil {
				return nil, fmt.Errorf("failed to parse duration")
			}
			return &duration, nil
		}

		for _, detail := range details.Array() {
			if detail.Get("@type").String() != "type.googleapis.com/google.rpc.ErrorInfo" {
				continue
			}
			quotaResetDelay := detail.Get("metadata.quotaResetDelay").String()
			if quotaResetDelay == "" {
				quotaResetDelay = detail.Get("metadata.quota_reset_delay").String()
			}
			if quotaResetDelay == "" {
				continue
			}
			duration, err := time.ParseDuration(quotaResetDelay)
			if err == nil && duration > 0 {
				return &duration, nil
			}
		}
	}

	message := gjson.GetBytes(errorBody, "error.message").String()
	if message != "" {
		lowerMsg := strings.ToLower(message)
		if matches := reRetryDelaySeconds.FindStringSubmatch(lowerMsg); len(matches) > 1 {
			seconds, err := strconv.Atoi(matches[1])
			if err == nil && seconds > 0 {
				duration := time.Duration(seconds) * time.Second
				return &duration, nil
			}
		}
		if matches := reRetryDelayHuman.FindStringSubmatch(lowerMsg); len(matches) > 1 {
			duration, err := time.ParseDuration(matches[1])
			if err == nil && duration > 0 {
				return &duration, nil
			}
		}
	}

	return nil, fmt.Errorf("no RetryInfo found")
}
