package helps

import (
	"strconv"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StripAntigravityCLIIdentity addresses an observed system-prefix rejection on
// gemini-3-flash. Other models and non-leading occurrences are left unchanged.
// Call before payload configuration so user rules remain the final authority.
func StripAntigravityCLIIdentity(model string, body []byte) []byte {
	if model != "gemini-3-flash" {
		return body
	}
	parts := gjson.GetBytes(body, "request.systemInstruction.parts")
	if !parts.IsArray() {
		return body
	}
	partList := parts.Array()
	for i, part := range partList {
		text := part.Get("text")
		if text.Type != gjson.String || text.String() == "" {
			continue
		}
		stripped := util.StripLeadingCLIIdentityPrefix(text.String())
		if stripped == text.String() {
			return body
		}
		path := "request.systemInstruction.parts." + strconv.Itoa(i)
		var out []byte
		var errSet error
		if stripped != "" {
			out, errSet = sjson.SetBytes(body, path+".text", stripped)
		} else if len(partList) == 1 {
			out, errSet = sjson.DeleteBytes(body, "request.systemInstruction")
		} else {
			out, errSet = sjson.DeleteBytes(body, path)
		}
		if errSet != nil {
			return body
		}
		return out
	}
	return body
}
