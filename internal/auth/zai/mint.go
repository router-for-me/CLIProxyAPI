package zai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

const (
	// MintAPIBaseURL is the standard OpenAI-compatible coding-plan endpoint
	// that accepts the minted API key.
	MintAPIBaseURL = "https://api.z.ai/api/coding/paas/v4"
	// mintKeyName matches the official client so re-login reuses the same key.
	mintKeyName = "zcode-api-key"
	// defaultOrgName / defaultProjectName are the account's default
	// organization and project names; a matching entry is preferred.
	defaultOrgName     = "默认机构"
	defaultProjectName = "默认项目"
)

// MintAPIKey provisions or reuses the account's coding-plan API key and
// returns the combined "apiKey.secretKey" credential together with the
// inference base URL. The business token from the Z.ai login authorizes the
// business API calls.
func (s *Service) MintAPIKey(ctx context.Context, businessToken string) (string, error) {
	businessToken = strings.TrimSpace(businessToken)
	if businessToken == "" {
		return "", fmt.Errorf("Z.ai API key mint is missing business token")
	}
	// rawRequest adds the Bearer prefix itself; pass the bare token.

	// 1. Resolve organization + project from the customer info.
	var info customerInfo
	if errInfo := s.doEnvelope(ctx, "GET", s.bizBaseURL+"/customer/getCustomerInfo", businessToken, nil, &info); errInfo != nil {
		return "", fmt.Errorf("Z.ai customer info: %w", errInfo)
	}
	org, proj, errOrg := selectOrgProject(&info)
	if errOrg != nil {
		return "", errOrg
	}

	// 2. Find the existing coding-plan key, or create it.
	keysURL := fmt.Sprintf("%s/v1/organization/%s/projects/%s/api_keys", s.bizBaseURL, url.PathEscape(org), url.PathEscape(proj))
	var listed []struct {
		Name   string `json:"name"`
		APIKey string `json:"apiKey"`
	}
	apiKey := ""
	if listData, errList := s.bizRequest(ctx, "GET", keysURL, businessToken, nil); errList == nil {
		if json.Unmarshal(listData, &listed) == nil {
			for _, k := range listed {
				if k.Name == mintKeyName && strings.TrimSpace(k.APIKey) != "" {
					apiKey = strings.TrimSpace(k.APIKey)
					break
				}
			}
		}
	}
	if apiKey == "" {
		body, _ := json.Marshal(map[string]string{"name": mintKeyName})
		var created struct {
			APIKey string `json:"apiKey"`
		}
		if errCreate := s.doEnvelope(ctx, "POST", keysURL, businessToken, body, &created); errCreate != nil {
			return "", fmt.Errorf("Z.ai create api key: %w", errCreate)
		}
		apiKey = strings.TrimSpace(created.APIKey)
	}
	if apiKey == "" {
		return "", fmt.Errorf("Z.ai api key response is missing apiKey")
	}

	// 3. Copy the secret key; final credential is "apiKey.secretKey".
	var copied struct {
		SecretKey string `json:"secretKey"`
	}
	if errCopy := s.doEnvelope(ctx, "GET", keysURL+"/copy/"+url.PathEscape(apiKey), businessToken, nil, &copied); errCopy != nil {
		return "", fmt.Errorf("Z.ai api key copy: %w", errCopy)
	}
	secretKey := strings.TrimSpace(copied.SecretKey)
	if secretKey == "" {
		return "", fmt.Errorf("Z.ai api key copy response is missing secretKey")
	}
	return apiKey + "." + secretKey, nil
}

type customerInfo struct {
	Organizations []struct {
		OrganizationID   string `json:"organizationId"`
		OrganizationName string `json:"organizationName"`
		Projects         []struct {
			ProjectID   string `json:"projectId"`
			ProjectName string `json:"projectName"`
		} `json:"projects"`
	} `json:"organizations"`
}

func selectOrgProject(info *customerInfo) (orgID, projectID string, err error) {
	if info == nil || len(info.Organizations) == 0 {
		return "", "", fmt.Errorf("Z.ai account has no organization (no active coding plan?)")
	}
	// Prefer an organization that has projects, favoring the account default.
	org := info.Organizations[0]
	for _, candidate := range info.Organizations {
		if len(candidate.Projects) == 0 {
			continue
		}
		isDefault := strings.Contains(candidate.OrganizationName, defaultOrgName)
		if len(org.Projects) == 0 || isDefault {
			org = candidate
			if isDefault {
				break
			}
		}
	}
	if len(org.Projects) == 0 {
		return "", "", fmt.Errorf("Z.ai organization has no projects")
	}
	proj := org.Projects[0]
	for _, candidate := range org.Projects {
		if strings.Contains(candidate.ProjectName, defaultProjectName) {
			proj = candidate
			break
		}
	}
	if strings.TrimSpace(org.OrganizationID) == "" || strings.TrimSpace(proj.ProjectID) == "" {
		return "", "", fmt.Errorf("Z.ai organization or project id is missing")
	}
	return strings.TrimSpace(org.OrganizationID), strings.TrimSpace(proj.ProjectID), nil
}

// bizRequest performs a business API GET/POST and returns the raw body.
func (s *Service) bizRequest(ctx context.Context, method, endpoint, authorization string, body []byte) ([]byte, error) {
	raw, err := s.rawRequest(ctx, method, endpoint, authorization, body)
	if err != nil {
		return nil, err
	}
	// Business endpoints wrap results in {code,msg,data}; unwrap when present.
	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Data) > 0 {
		switch envelope.Code.String() {
		case "", "0", "200":
			return envelope.Data, nil
		default:
			if strings.TrimSpace(envelope.Msg) != "" {
				return nil, fmt.Errorf("%s", strings.TrimSpace(envelope.Msg))
			}
			return nil, fmt.Errorf("business error %s", envelope.Code.String())
		}
	}
	return raw, nil
}

// doEnvelopeAllowEmpty behaves like doEnvelope but tolerates endpoints that
// return an envelope with an empty data member.
func (s *Service) doEnvelopeAllowEmpty(ctx context.Context, method, endpoint, bearer string, body []byte, target any) error {
	raw, err := s.rawRequest(ctx, method, endpoint, bearer, body)
	if err != nil {
		return err
	}
	var envelope struct {
		Code json.Number     `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Data) > 0 {
		switch envelope.Code.String() {
		case "", "0", "200":
			if errDecode := json.Unmarshal(envelope.Data, target); errDecode != nil {
				return fmt.Errorf("decode Z.ai response data: %w (body: %s)", errDecode, truncateForLog(raw))
			}
			return nil
		default:
			if strings.TrimSpace(envelope.Msg) != "" {
				return fmt.Errorf("Z.ai rejected: %s", strings.TrimSpace(envelope.Msg))
			}
			return fmt.Errorf("Z.ai rejected with code %s", envelope.Code.String())
		}
	}
	if errDecode := json.Unmarshal(raw, target); errDecode != nil {
		return fmt.Errorf("decode Z.ai response: %w (body: %s)", errDecode, truncateForLog(raw))
	}
	return nil
}
