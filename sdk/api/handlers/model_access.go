package handlers

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

// ModelPolicyFromGin returns the model policy attached to the authenticated request, or nil
// when the caller is unrestricted.
func ModelPolicyFromGin(c *gin.Context) *sdkaccess.ModelPolicy {
	if c == nil {
		return nil
	}
	policy, _ := c.Get(sdkaccess.ModelPolicyContextKey)
	typed, _ := policy.(*sdkaccess.ModelPolicy)
	return typed
}

// FilterModelList drops the entries of a registry model list that the request's key may not use.
// Entries are identified by their "id" (OpenAI/Claude) or "name" (Gemini) field.
func FilterModelList(c *gin.Context, models []map[string]any) []map[string]any {
	policy := ModelPolicyFromGin(c)
	if policy == nil {
		return models
	}
	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		if policy.Allows(listedModelName(model)) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// FilterModelListResponseBody re-filters a serialized model-list response after
// plugin response interceptors have had an opportunity to replace its body.
// Restricted requests fail closed when the interceptor returns a malformed catalog.
func FilterModelListResponseBody(c *gin.Context, body []byte) ([]byte, error) {
	if ModelPolicyFromGin(c) == nil {
		return body, nil
	}
	if !ojson.ValidBytes(body) {
		return nil, errors.New("invalid model catalog JSON")
	}

	doc := ojson.NewDoc(body)
	foundList := false
	for _, field := range []string{"data", "models"} {
		result := ojson.GetBytesNoCopy(body, field)
		if !result.Exists() {
			continue
		}
		foundList = true
		if !result.IsArray() {
			return nil, errors.New("model catalog field is not an array")
		}
		filtered := filterOJSONModelArray(c, result)
		if err := doc.SetRawBytes(field, filtered); err != nil {
			return nil, err
		}
	}
	if !foundList {
		return nil, errors.New("model catalog has no data or models array")
	}
	return doc.Bytes(), nil
}

func filterOJSONModelArray(c *gin.Context, result ojson.Result) []byte {
	var out bytes.Buffer
	out.WriteByte('[')
	first := true
	for _, item := range result.Array() {
		if !item.IsObject() {
			continue
		}
		name := ""
		for _, field := range []string{"id", "name", "slug"} {
			value := item.Get(field)
			if value.Type == ojson.String && value.Str != "" {
				name = strings.TrimPrefix(value.Str, "models/")
				break
			}
		}
		if name == "" || !ModelPolicyFromGin(c).Allows(name) {
			continue
		}
		if !first {
			out.WriteByte(',')
		}
		first = false
		out.WriteString(item.Raw)
	}
	out.WriteByte(']')
	return out.Bytes()
}

func listedModelName(model map[string]any) string {
	for _, field := range []string{"id", "name", "slug"} {
		if name, _ := model[field].(string); name != "" {
			return strings.TrimPrefix(name, "models/")
		}
	}
	return ""
}

// modelAccessError rejects a request for a model the authenticated key may not use.
// The thinking suffix is ignored, and "auto" is checked against the model it resolves to.
func modelAccessError(ctx context.Context, modelName string) *interfaces.ErrorMessage {
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	policy := ModelPolicyFromGin(ginCtx)
	if policy == nil {
		return nil
	}
	base := strings.TrimSpace(thinking.ParseSuffix(modelName).ModelName)
	checkModel := base
	if checkModel == "auto" {
		checkModel = util.ResolveAutoModel(checkModel)
	}
	if !policy.Allows(checkModel) {
		body := ojson.NewDocString(`{"error":{"message":"","type":"invalid_request_error","code":"model_not_allowed","param":"model"}}`)
		_ = body.Set("error.message", "this API key is not allowed to use model "+modelName)
		return &interfaces.ErrorMessage{StatusCode: http.StatusForbidden, Error: errors.New(body.String())}
	}
	return nil
}
