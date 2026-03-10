package registry

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

func GetCodexModelsForPlan(planType string) []*ModelInfo {
	switch NormalizeCodexPlanType(planType) {
	case "pro":
		return GetCodexProModels()
	case "plus":
		return GetCodexPlusModels()
	case "team":
		return GetCodexTeamModels()
	case "free":
		fallthrough
	default:
		return GetCodexFreeModels()
	}
}

func GetCodexModelsUnion() []*ModelInfo {
	data := getModels()
	sections := [][]*ModelInfo{
		data.CodexFree,
		data.CodexTeam,
		data.CodexPlus,
		data.CodexPro,
	}
	seen := make(map[string]struct{})
	out := make([]*ModelInfo, 0)
	for _, models := range sections {
		for _, model := range models {
			if model == nil || strings.TrimSpace(model.ID) == "" {
				continue
			}
			if _, ok := seen[model.ID]; ok {
				continue
			}
			seen[model.ID] = struct{}{}
			out = append(out, cloneModelInfo(model))
		}
	}
	return out
}

func NormalizeCodexPlanType(planType string) string {
	switch strings.ToLower(strings.TrimSpace(planType)) {
	case "free":
		return "free"
	case "team", "business", "enterprise", "edu", "education":
		return "team"
	case "plus":
		return "plus"
	case "pro":
		return "pro"
	default:
		return ""
	}
}

func ResolveCodexPlanType(attributes map[string]string, metadata map[string]any) string {
	if attributes != nil {
		for _, key := range []string{"plan_type", "chatgpt_plan_type"} {
			if plan := NormalizeCodexPlanType(attributes[key]); plan != "" {
				return plan
			}
		}
	}
	plan, _ := EnsureCodexPlanTypeMetadata(metadata)
	return plan
}

func EnsureCodexPlanTypeMetadata(metadata map[string]any) (string, bool) {
	if metadata == nil {
		return "", false
	}
	for _, key := range []string{"plan_type", "chatgpt_plan_type"} {
		if raw, ok := metadata[key].(string); ok {
			if plan := NormalizeCodexPlanType(raw); plan != "" {
				current, _ := metadata["plan_type"].(string)
				if strings.TrimSpace(current) != plan {
					metadata["plan_type"] = plan
					return plan, true
				}
				return plan, false
			}
		}
	}
	idToken := firstString(metadata, "id_token")
	if idToken == "" {
		idToken = nestedString(metadata, "token", "id_token")
	}
	if idToken == "" {
		idToken = nestedString(metadata, "tokens", "id_token")
	}
	if idToken == "" {
		return "", false
	}
	plan, err := extractCodexPlanTypeFromJWT(idToken)
	if err != nil {
		return "", false
	}
	if plan == "" {
		return "", false
	}
	metadata["plan_type"] = plan
	return plan, true
}

func firstString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	if value, ok := metadata[key].(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func nestedString(metadata map[string]any, parent, key string) string {
	if metadata == nil {
		return ""
	}
	raw, ok := metadata[parent]
	if !ok {
		return ""
	}
	child, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	value, _ := child[key].(string)
	return strings.TrimSpace(value)
}

func extractCodexPlanTypeFromJWT(token string) (string, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid jwt format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Auth struct {
			PlanType string `json:"chatgpt_plan_type"`
		} `json:"https://api.openai.com/auth"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	return NormalizeCodexPlanType(claims.Auth.PlanType), nil
}
