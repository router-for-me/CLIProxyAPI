package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	claudemodels "github.com/router-for-me/CLIProxyAPI/v8/internal/client/claude/models"
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
// Entries are identified by their "id" (OpenAI/Claude), "name" (Gemini) or "slug" (Codex)
// field. Thinking suffixes are ignored, matching execution, and entries whose identity
// fields disagree are dropped.
func FilterModelList(c *gin.Context, models []map[string]any) []map[string]any {
	policy := ModelPolicyFromGin(c)
	if policy == nil {
		return models
	}
	filtered := make([]map[string]any, 0, len(models))
	for _, model := range models {
		name, conflict := mapModelIdentity(model)
		if conflict || name == "" {
			continue
		}
		if policy.Allows(policyModelName(name)) {
			filtered = append(filtered, model)
		}
	}
	return filtered
}

// FilterModelListResponseBody re-filters a serialized model-list response after
// plugin response interceptors have had an opportunity to replace its body.
// Restricted requests fail closed when the interceptor returns a malformed or
// ambiguous catalog, including one with duplicate object keys or with model entries
// whose identity fields disagree.
// The optional source format lets Claude catalogs match the decoded routing ID.
func FilterModelListResponseBody(c *gin.Context, body []byte, sourceFormat ...string) ([]byte, error) {
	policy := ModelPolicyFromGin(c)
	if policy == nil {
		return body, nil
	}
	if !ojson.ValidBytes(body) {
		return nil, errors.New("invalid model catalog JSON")
	}
	if errDuplicate := rejectDuplicateCatalogKeys(body); errDuplicate != nil {
		return nil, fmt.Errorf("ambiguous model catalog JSON: %w", errDuplicate)
	}

	doc := ojson.NewDoc(body)
	claudeCatalog := len(sourceFormat) > 0 && sourceFormat[0] == "claude"
	foundList := false
	for _, field := range []string{"data", "models"} {
		result := doc.Get(field)
		if !result.Exists() {
			continue
		}
		foundList = true
		if !result.IsArray() {
			return nil, errors.New("model catalog field is not an array")
		}
		if errFilter := filterOJSONModelArray(doc, field, policy, claudeCatalog); errFilter != nil {
			return nil, errFilter
		}
	}
	if !foundList {
		return nil, errors.New("model catalog has no data or models array")
	}
	if claudeCatalog && doc.Get("data").IsArray() {
		models := doc.Get("data").Array()
		firstID, lastID := "", ""
		if len(models) > 0 {
			firstID = models[0].Get("id").String()
			lastID = models[len(models)-1].Get("id").String()
		}
		for _, boundary := range []struct{ field, value string }{{"first_id", firstID}, {"last_id", lastID}} {
			if doc.Get(boundary.field).Exists() {
				if errSet := doc.Set(boundary.field, boundary.value); errSet != nil {
					return nil, errSet
				}
			}
		}
	}
	return doc.Bytes(), nil
}

func filterOJSONModelArray(doc *ojson.Doc, field string, policy *sdkaccess.ModelPolicy, claudeCatalog bool) error {
	models := doc.Get(field).Array()
	// Delete from the end so removal never shifts an index still to be checked.
	for index := len(models) - 1; index >= 0; index-- {
		item := models[index]
		if !item.IsObject() {
			if errDelete := doc.Delete(field + "." + strconv.Itoa(index)); errDelete != nil {
				return errDelete
			}
			continue
		}
		name, conflict := ojsonModelIdentity(item)
		if conflict {
			return errors.New("model catalog entry has conflicting identity fields")
		}
		if claudeCatalog {
			name = claudemodels.ResolveClaudeModelIDPrefix(name)
		}
		name = policyModelName(name)
		if name == "" || !policy.Allows(name) {
			if errDelete := doc.Delete(field + "." + strconv.Itoa(index)); errDelete != nil {
				return errDelete
			}
		}
	}
	return nil
}

// mapModelIdentity returns the catalog identity of a model map and reports whether
// its identity fields disagree.
func mapModelIdentity(model map[string]any) (string, bool) {
	fieldValue := func(field string) string {
		value, _ := model[field].(string)
		return value
	}
	return catalogIdentity(fieldValue("id"), fieldValue("name"), fieldValue("slug"))
}

// ojsonModelIdentity returns the catalog identity of a catalog entry and reports
// whether its identity fields disagree.
func ojsonModelIdentity(item ojson.Result) (string, bool) {
	fieldValue := func(field string) string {
		value := item.Get(field)
		if value.Type != ojson.String {
			return ""
		}
		return value.Str
	}
	return catalogIdentity(fieldValue("id"), fieldValue("name"), fieldValue("slug"))
}

// catalogIdentity returns the single model name carried by the identity fields of a
// catalog entry. Distinct values are reported as a conflict: the policy would then
// authorize one name while the client consumes another.
func catalogIdentity(values ...string) (string, bool) {
	name := ""
	for _, value := range values {
		identity := normalizeCatalogIdentity(value)
		if identity == "" {
			continue
		}
		if name == "" {
			name = identity
			continue
		}
		if !strings.EqualFold(identity, name) {
			return "", true
		}
	}
	return name, false
}

func normalizeCatalogIdentity(value string) string {
	value = strings.TrimSpace(value)
	return strings.TrimSpace(strings.TrimPrefix(value, "models/"))
}

// policyModelName normalizes a catalog identity for policy matching the same way
// execution does: the thinking suffix is ignored.
func policyModelName(name string) string {
	return strings.TrimSpace(thinking.ParseSuffix(name).ModelName)
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
