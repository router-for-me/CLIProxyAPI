package joycode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	APIBaseURL    = "https://joycode-api.jd.com"
	UserInfoPath  = "/api/saas/user/v1/userInfo"
	ModelListPath = "/api/saas/models/v1/modelList"
	ChatPath      = "/api/saas/openai/v1/chat/completions"
	JoyCodeUA     = "JoyCode/2.4.8 (Windows)"
)

type JoyCodeAuth struct {
	httpClient *http.Client
}

func NewJoyCodeAuth(httpClient *http.Client) *JoyCodeAuth {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &JoyCodeAuth{httpClient: httpClient}
}

func (a *JoyCodeAuth) VerifyToken(ctx context.Context, ptKey string) (*JoyCodeTokenData, error) {
	for _, loginType := range []string{"", "IDE", "ERP"} {
		result, err := a.tryUserInfo(ctx, ptKey, loginType)
		if err != nil {
			log.Debugf("joycode: loginType=%s verify failed: %v", loginType, err)
			continue
		}
		if result != nil {
			return result, nil
		}
	}
	return nil, fmt.Errorf("joycode: all loginType attempts failed")
}

func (a *JoyCodeAuth) tryUserInfo(ctx context.Context, ptKey, loginType string) (*JoyCodeTokenData, error) {
	payload, _ := json.Marshal(map[string]interface{}{})
	req, err := http.NewRequestWithContext(ctx, "POST", APIBaseURL+UserInfoPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("ptKey", ptKey)
	req.Header.Set("loginType", loginType)
	req.Header.Set("User-Agent", JoyCodeUA)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("joycode: failed to parse userInfo response: %w", err)
	}

	code, _ := result["code"].(float64)
	if int(code) != 0 {
		msg, _ := result["msg"].(string)
		return nil, fmt.Errorf("joycode: userInfo returned code=%v msg=%s", code, msg)
	}

	data, _ := result["data"].(map[string]interface{})
	if data == nil {
		return nil, fmt.Errorf("joycode: userInfo returned nil data")
	}

	userID, _ := data["userId"].(string)
	tenant, _ := data["tenant"].(string)
	if tenant == "" {
		tenant = "JD"
	}
	orgFullName, _ := data["orgFullName"].(string)
	returnedPTKey, _ := data["ptKey"].(string)
	if returnedPTKey == "" {
		returnedPTKey = ptKey
	}
	effectiveLoginType := loginType
	if effectiveLoginType == "" {
		effectiveLoginType = "IDE"
	}

	return &JoyCodeTokenData{
		PTKey:       returnedPTKey,
		UserID:      userID,
		Tenant:      tenant,
		OrgFullName: orgFullName,
		LoginType:   effectiveLoginType,
	}, nil
}

func (a *JoyCodeAuth) FetchModelList(ctx context.Context, ptKey string) ([]interface{}, error) {
	payload, _ := json.Marshal(map[string]interface{}{})
	req, err := http.NewRequestWithContext(ctx, "POST", APIBaseURL+ModelListPath, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("ptKey", ptKey)
	req.Header.Set("User-Agent", JoyCodeUA)

	resp, err := a.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("joycode: failed to parse model list response: %w", err)
	}

	code, _ := result["code"].(float64)
	if int(code) != 0 {
		return nil, fmt.Errorf("joycode: model list returned code=%v", code)
	}

	data, _ := result["data"].([]interface{})
	return data, nil
}
