package zcode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Credential is the resolved static coding-plan credential.
type Credential struct {
	APIKey string
	Secret string
	JWT    string
	UserID string
}

// FullKey returns the apiKey (Bigmodel: apiKey.secret).
func (c *Credential) FullKey() string {
	if c.Secret != "" {
		return c.APIKey + "." + c.Secret
	}
	return c.APIKey
}

// Resolver turns an OAuth access token into a static coding-plan API key via
// the Z.ai biz API, mirroring the ZCode desktop client.
type Resolver struct {
	HTTP *http.Client
	// TestHost overrides the api.z.ai origin (httptest in tests).
	TestHost string
}

const defaultResolverHost = "https://api.z.ai"

func (r *Resolver) host() string {
	if r.TestHost != "" {
		return r.TestHost
	}
	return defaultResolverHost
}

func (r *Resolver) doJSON(ctx context.Context, method, url string, bearer string, body any, out any) error {
	httpClient := r.HTTP
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	var reader io.Reader
	if body != nil {
		raw, _ := json.Marshal(body)
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	var wrap struct {
		Data json.RawMessage `json:"data"`
		Msg  string          `json:"msg"`
	}
	if err := json.Unmarshal(raw, &wrap); err != nil {
		// Some endpoints return the object directly.
		wrap.Data = raw
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if wrap.Msg != "" {
			return fmt.Errorf("zcode: %s (%d): %s", url, resp.StatusCode, wrap.Msg)
		}
		return fmt.Errorf("zcode: %s failed: status=%d", url, resp.StatusCode)
	}
	if out != nil {
		if len(wrap.Data) > 0 {
			if err := json.Unmarshal(wrap.Data, out); err != nil {
				// Some endpoints wrap the data key, others return the object
				// directly; fall back to the top-level body if the data decode
				// fails (e.g. data is an empty wrapper).
				if err2 := json.Unmarshal(raw, out); err2 != nil {
					return fmt.Errorf("zcode: decode %s: %w", url, err)
				}
			}
		} else if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("zcode: decode %s: %w", url, err)
		}
	}
	return nil
}

func (r *Resolver) resolveCustomerInfo(ctx context.Context, auth string) (orgID, projectID string, err error) {
	var info struct {
		Organizations []struct {
			OrganizationID   string `json:"organizationId"`
			OrganizationName string `json:"organizationName"`
			Projects         []struct {
				ProjectID   string `json:"projectId"`
				ProjectName string `json:"projectName"`
			} `json:"projects"`
		} `json:"organizations"`
		Orgs []struct {
			ID               string `json:"id"`
			OrganizationName string `json:"organizationName"`
			Projects         []struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			} `json:"projects"`
		} `json:"orgs"`
	}
	if err := r.doJSON(ctx, http.MethodGet, r.host()+"/api/biz/customer/getCustomerInfo", auth, nil, &info); err != nil {
		return "", "", err
	}
	orgs := info.Organizations
	if len(orgs) == 0 {
		orgs = make([]struct {
			OrganizationID   string `json:"organizationId"`
			OrganizationName string `json:"organizationName"`
			Projects         []struct {
				ProjectID   string `json:"projectId"`
				ProjectName string `json:"projectName"`
			} `json:"projects"`
		}, len(info.Orgs))
		for i, o := range info.Orgs {
			orgs[i].OrganizationID = o.ID
			orgs[i].OrganizationName = o.OrganizationName
			for _, p := range o.Projects {
				orgs[i].Projects = append(orgs[i].Projects, struct {
					ProjectID   string `json:"projectId"`
					ProjectName string `json:"projectName"`
				}{ProjectID: p.ID, ProjectName: p.Name})
			}
		}
	}
	if len(orgs) == 0 {
		return "", "", fmt.Errorf("zcode: no organizations found")
	}
	// Prefer the default-org named entry ("默认机构"), else first.
	org := orgs[0]
	for _, o := range orgs {
		if strings.Contains(o.OrganizationName, "默认") || strings.Contains(o.OrganizationName, "default") {
			org = o
			break
		}
	}
	if len(org.Projects) == 0 {
		return "", "", fmt.Errorf("zcode: no projects found")
	}
	proj := org.Projects[0]
	for _, p := range org.Projects {
		if strings.Contains(p.ProjectName, "默认") || strings.Contains(p.ProjectName, "default") {
			proj = p
			break
		}
	}
	return org.OrganizationID, proj.ProjectID, nil
}

func (r *Resolver) findOrCreateAPIKey(ctx context.Context, host, auth, orgID, projectID string) (string, error) {
	listURL := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys", host, orgID, projectID)
	var existing []struct {
		Name   string `json:"name"`
		APIKey string `json:"apiKey"`
	}
	_ = r.doJSON(ctx, http.MethodGet, listURL, auth, nil, &existing)
	for _, k := range existing {
		if k.Name == "zcode-api-key" && k.APIKey != "" {
			return k.APIKey, nil
		}
	}
	var created struct {
		APIKey string `json:"apiKey"`
	}
	if err := r.doJSON(ctx, http.MethodPost, listURL, auth, map[string]any{"name": "zcode-api-key"}, &created); err != nil {
		return "", err
	}
	if created.APIKey == "" {
		return "", fmt.Errorf("zcode: API key creation returned empty apiKey")
	}
	return created.APIKey, nil
}

func (r *Resolver) copySecret(ctx context.Context, host, auth, orgID, projectID, apiKey string) string {
	url := fmt.Sprintf("%s/api/biz/v1/organization/%s/projects/%s/api_keys/copy/%s",
		host, orgID, projectID, apiKey)
	var data struct {
		SecretKey string `json:"secretKey"`
		Secret    string `json:"secret"`
	}
	if err := r.doJSON(ctx, http.MethodGet, url, auth, nil, &data); err != nil {
		return ""
	}
	if data.SecretKey != "" {
		return data.SecretKey
	}
	return data.Secret
}

// ResolveZaiCredential exchanges a Z.ai OAuth access token for a static
// coding-plan API key and its secret.
func (r *Resolver) ResolveZaiCredential(ctx context.Context, accessToken string) (*Credential, error) {
	var login struct {
		AccessToken string `json:"access_token"`
	}
	if err := r.doJSON(ctx, http.MethodPost, r.host()+"/api/auth/z/login",
		"", map[string]any{"token": accessToken}, &login); err != nil {
		return nil, err
	}
	if login.AccessToken == "" {
		return nil, fmt.Errorf("zcode: z/login returned no biz token")
	}
	auth := "Bearer " + login.AccessToken
	orgID, projectID, err := r.resolveCustomerInfo(ctx, auth)
	if err != nil {
		return nil, err
	}
	apiKey, err := r.findOrCreateAPIKey(ctx, r.host(), auth, orgID, projectID)
	if err != nil {
		return nil, err
	}
	secret := r.copySecret(ctx, r.host(), auth, orgID, projectID, apiKey)
	return &Credential{APIKey: apiKey, Secret: secret}, nil
}
