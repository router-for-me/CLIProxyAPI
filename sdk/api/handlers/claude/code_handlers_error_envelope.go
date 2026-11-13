package claude

import "github.com/MoeForge/moego/ojson"

func claudeErrorDocument(response claudeErrorResponse) *ojson.Doc {
	doc := ojson.NewDocString(`{"type":"error","error":{}}`)
	_ = doc.Set("type", response.Type)
	_ = doc.Set("error.type", response.Error.Type)
	_ = doc.Set("error.message", response.Error.Message)
	if response.Error.Code != "" {
		_ = doc.Set("error.code", response.Error.Code)
	}
	if response.Error.Retryable != nil {
		_ = doc.Set("error.retryable", *response.Error.Retryable)
	}
	if response.Error.Details != nil {
		_ = doc.Set("error.details.error_code", response.Error.Details.ErrorCode)
	}
	return doc
}
