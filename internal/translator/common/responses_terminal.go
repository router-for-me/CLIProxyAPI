package common

import "github.com/MoeForge/moego/ojson"

// WrapResponsesTerminalEvent adapts a flat Responses object to the terminal
// event shape consumed by the existing Responses response translators.
func WrapResponsesTerminalEvent(body []byte) []byte {
	status := ojson.GetBytesNoCopy(body, "status").String()
	if status != "completed" && status != "incomplete" {
		return nil
	}
	doc := ojson.NewDocString(`{}`)
	_ = doc.Set("type", "response."+status)
	_ = doc.SetRawBytes("response", body)
	return doc.Bytes()
}
