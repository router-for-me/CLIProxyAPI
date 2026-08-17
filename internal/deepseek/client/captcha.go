package client

import (
	"regexp"
	"strings"
)

// CaptchaChallenge describes a detected captcha/risk-control challenge.
type CaptchaChallenge struct {
	ImageURL    string
	Instruction string
	Rid         string
	CaptchaUUID string
}

var captchaTerms = regexp.MustCompile(`(?i)captcha|hcaptcha|shumei|verification|verify|risk|验证码|数美|风控|验证`)

// DetectCaptchaChallenge recursively scans a DeepSeek response payload for
// shumei/captcha risk-control signals. Returns nil when no challenge is found.
func DetectCaptchaChallenge(resp map[string]any) *CaptchaChallenge {
	if resp == nil {
		return nil
	}
	challenge := findChallengeObject(resp)
	if challenge == nil {
		return nil
	}
	return challenge
}

func findChallengeObject(value any) *CaptchaChallenge {
	return findChallengeObjectDepth(value, 0)
}

func findChallengeObjectDepth(value any, depth int) *CaptchaChallenge {
	if depth > 8 {
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		if ch := challengeFromMap(v); ch != nil {
			return ch
		}
		for _, child := range v {
			if ch := findChallengeObjectDepth(child, depth+1); ch != nil {
				return ch
			}
		}
	case []any:
		for _, child := range v {
			if ch := findChallengeObjectDepth(child, depth+1); ch != nil {
				return ch
			}
		}
	}
	return nil
}

func challengeFromMap(m map[string]any) *CaptchaChallenge {
	detail, _ := m["detail"].(map[string]any)
	if detail == nil {
		detail = m
	}

	imageURL := firstNonEmptyStr(
		asStringCaptcha(detail["bg"]),
		asStringCaptcha(detail["imageUrl"]),
		asStringCaptcha(detail["image"]),
		asStringCaptcha(detail["captchaImage"]),
		asStringCaptcha(detail["url"]),
		asStringCaptcha(m["imageUrl"]),
		asStringCaptcha(m["captchaImage"]),
	)
	instruction := joinInstruction(
		asStringCaptcha(detail["order"]),
		asStringCaptcha(detail["instruction"]),
		asStringCaptcha(detail["comment"]),
		asStringCaptcha(m["order"]),
		asStringCaptcha(m["instruction"]),
	)
	rid := firstNonEmptyStr(
		asStringCaptcha(detail["rid"]),
		asStringCaptcha(m["rid"]),
	)
	captchaUUID := firstNonEmptyStr(
		asStringCaptcha(detail["captchaUuid"]),
		asStringCaptcha(detail["captcha_uuid"]),
		asStringCaptcha(m["captchaUuid"]),
		asStringCaptcha(m["captcha_uuid"]),
	)

	bizCode := bizCodeFromResponse(m)
	msgText := asStringCaptcha(m["msg"]) + " " + asStringCaptcha(m["biz_msg"])
	if msgText == " " {
		if data, ok := m["data"].(map[string]any); ok {
			msgText = asStringCaptcha(data["biz_msg"]) + " " + asStringCaptcha(data["msg"])
		}
	}
	hasCaptchaKeyword := captchaTerms.MatchString(strings.TrimSpace(msgText))
	hasFailureCode := bizCode != 0

	if imageURL == "" && instruction == "" && (!hasFailureCode || !hasCaptchaKeyword) {
		return nil
	}

	return &CaptchaChallenge{
		ImageURL:    imageURL,
		Instruction: instruction,
		Rid:         rid,
		CaptchaUUID: captchaUUID,
	}
}

func asStringCaptcha(v any) string {
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func firstNonEmptyStr(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func joinInstruction(values ...string) string {
	var parts []string
	for _, v := range values {
		if v != "" {
			parts = append(parts, v)
		}
	}
	return strings.Join(parts, " / ")
}

func bizCodeFromResponse(m map[string]any) int {
	if data, ok := m["data"].(map[string]any); ok {
		if c := intFrom(data["biz_code"]); c != 0 {
			return c
		}
	}
	return intFrom(m["code"])
}

// intFrom coerces common JSON numeric representations into an int.
// It is local to this file because ds2api's client package shared this helper
// across multiple files; here it is only needed by captcha detection.
func intFrom(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
