package helps

import (
	translatorcommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/common"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// A single space: some Gemini endpoints reject an empty text part, while
// Claude-on-Antigravity still rejects even this and is skipped upstream.
var placeholderGeminiUserTurnJSON = []byte(`{"role":"user","parts":[{"text":" "}]}`)

// EnsureGeminiLeadingUserContent prepends a placeholder user turn when the
// contents array at the given path starts with role=model.
func EnsureGeminiLeadingUserContent(payload []byte, path string) []byte {
	firstRole := gjson.GetBytes(payload, path+".0.role")
	if firstRole.String() != "model" {
		return payload
	}
	contents := util.GetGJSONBytesNoCopy(payload, path)
	if !contents.IsArray() {
		return payload
	}
	contentArray := contents.Array()
	if len(contentArray) == 0 {
		return payload
	}

	contentItems := make([][]byte, 0, len(contentArray)+1)
	contentItems = append(contentItems, placeholderGeminiUserTurnJSON)
	for _, content := range contentArray {
		contentItems = append(contentItems, []byte(content.Raw))
	}

	out, errSet := sjson.SetRawBytes(payload, path, translatorcommon.JoinRawArray(contentItems))
	if errSet != nil {
		return payload
	}
	return out
}
