package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

func jwtExpiry(token string) (time.Time, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return time.Time{}, false
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := decodeJWTPart(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp json.Number `json:"exp"`
	}
	if err = json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, false
	}
	unix, errUnix := claims.Exp.Int64()
	if errUnix != nil {
		seconds, errFloat := claims.Exp.Float64()
		if errFloat != nil || seconds <= 0 {
			return time.Time{}, false
		}
		unix = int64(seconds)
	}
	if unix <= 0 {
		return time.Time{}, false
	}
	return time.Unix(unix, 0).UTC(), true
}

func decodeJWTPart(part string) ([]byte, error) {
	switch len(part) % 4 {
	case 2:
		part += "=="
	case 3:
		part += "="
	}
	return base64.URLEncoding.DecodeString(part)
}
