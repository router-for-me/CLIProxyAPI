package helps

import (
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
)

// antigravitySuspendedMessagePrefix starts the message Cloud Code returns once
// an account is suspended. Earlier suspensions sent only this message, without
// an ErrorInfo detail.
const antigravitySuspendedMessagePrefix = "This service has been disabled in this account for violation of Terms of Service."

// IsAntigravityAccountSuspended reports whether an upstream response is the 403
// Cloud Code returns on every call once Google suspends the account for a Terms
// of Service violation.
func IsAntigravityAccountSuspended(statusCode int, body []byte) bool {
	if statusCode != http.StatusForbidden {
		return false
	}
	details := ojson.GetBytesNoCopy(body, "error.details")
	if details.IsArray() {
		for _, detail := range details.Array() {
			if detail.Get("@type").String() == "type.googleapis.com/google.rpc.ErrorInfo" && detail.Get("reason").String() == "TOS_VIOLATION" {
				return true
			}
		}
	}
	return strings.HasPrefix(ojson.GetBytesNoCopy(body, "error.message").String(), antigravitySuspendedMessagePrefix)
}
