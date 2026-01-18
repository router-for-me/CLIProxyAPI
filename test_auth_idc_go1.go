// 测试脚本 1：模拟 kiro2api_go1 的 IdC 认证方式
// 这个脚本完整模拟 kiro-gateway/temp/kiro2api_go1 的认证逻辑
// 运行方式: go run test_auth_idc_go1.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 配置常量 - 来自 kiro2api_go1/config/config.go
const (
	IdcRefreshTokenURL  = "https://oidc.us-east-1.amazonaws.com/token"
	CodeWhispererAPIURL = "https://codewhisperer.us-east-1.amazonaws.com"
)

// AuthConfig - 来自 kiro2api_go1/auth/config.go
type AuthConfig struct {
	AuthType     string `json:"auth"`
	RefreshToken string `json:"refreshToken"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
}

// IdcRefreshRequest - 来自 kiro2api_go1/types/token.go
type IdcRefreshRequest struct {
	ClientId     string `json:"clientId"`
	ClientSecret string `json:"clientSecret"`
	GrantType    string `json:"grantType"`
	RefreshToken string `json:"refreshToken"`
}

// RefreshResponse - 来自 kiro2api_go1/types/token.go
type RefreshResponse struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken,omitempty"`
	ExpiresIn    int    `json:"expiresIn"`
	TokenType    string `json:"tokenType,omitempty"`
}

// Fingerprint - 简化的指纹结构
type Fingerprint struct {
	OSType             string
	ConnectionBehavior string
	AcceptLanguage     string
	SecFetchMode       string
	AcceptEncoding     string
}

func generateFingerprint() *Fingerprint {
	osTypes := []string{"darwin", "windows", "linux"}
	connections := []string{"keep-alive", "close"}
	languages := []string{"en-US,en;q=0.9", "zh-CN,zh;q=0.9", "en-GB,en;q=0.9"}
	fetchModes := []string{"cors", "navigate", "no-cors"}

	return &Fingerprint{
		OSType:             osTypes[rand.Intn(len(osTypes))],
		ConnectionBehavior: connections[rand.Intn(len(connections))],
		AcceptLanguage:     languages[rand.Intn(len(languages))],
		SecFetchMode:       fetchModes[rand.Intn(len(fetchModes))],
		AcceptEncoding:     "gzip, deflate, br",
	}
}

func main() {
	rand.Seed(time.Now().UnixNano())

	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println("  测试脚本 1: kiro2api_go1 风格 IdC 认证")
	fmt.Println("=" + strings.Repeat("=", 59))

	// Step 1: 加载官方格式的 token 文件
	fmt.Println("\n[Step 1] 加载官方格式 Token 文件")
	fmt.Println("-" + strings.Repeat("-", 59))

	// 尝试从多个位置加载
	tokenPaths := []string{
		// 优先使用包含完整 clientId/clientSecret 的文件
		"E:/ai_project_2api/kiro-gateway/configs/kiro/kiro-auth-token-1768317098.json",
		filepath.Join(os.Getenv("USERPROFILE"), ".aws", "sso", "cache", "kiro-auth-token.json"),
	}

	var tokenData map[string]interface{}
	var loadedPath string

	for _, p := range tokenPaths {
		data, err := os.ReadFile(p)
		if err == nil {
			if err := json.Unmarshal(data, &tokenData); err == nil {
				loadedPath = p
				break
			}
		}
	}

	if tokenData == nil {
		fmt.Println("❌ 无法加载任何 token 文件")
		return
	}

	fmt.Printf("✅ 加载文件: %s\n", loadedPath)

	// 提取关键字段
	accessToken, _ := tokenData["accessToken"].(string)
	refreshToken, _ := tokenData["refreshToken"].(string)
	clientId, _ := tokenData["clientId"].(string)
	clientSecret, _ := tokenData["clientSecret"].(string)
	authMethod, _ := tokenData["authMethod"].(string)
	region, _ := tokenData["region"].(string)

	if region == "" {
		region = "us-east-1"
	}

	fmt.Printf("\n当前 Token 信息:\n")
	fmt.Printf("  AuthMethod: %s\n", authMethod)
	fmt.Printf("  Region: %s\n", region)
	fmt.Printf("  AccessToken: %s...\n", truncate(accessToken, 50))
	fmt.Printf("  RefreshToken: %s...\n", truncate(refreshToken, 50))
	fmt.Printf("  ClientID: %s\n", truncate(clientId, 30))
	fmt.Printf("  ClientSecret: %s...\n", truncate(clientSecret, 50))

	// Step 2: 验证 IdC 认证所需字段
	fmt.Println("\n[Step 2] 验证 IdC 认证必需字段")
	fmt.Println("-" + strings.Repeat("-", 59))

	missingFields := []string{}
	if refreshToken == "" {
		missingFields = append(missingFields, "refreshToken")
	}
	if clientId == "" {
		missingFields = append(missingFields, "clientId")
	}
	if clientSecret == "" {
		missingFields = append(missingFields, "clientSecret")
	}

	if len(missingFields) > 0 {
		fmt.Printf("❌ 缺少必需字段: %v\n", missingFields)
		fmt.Println("   IdC 认证需要: refreshToken, clientId, clientSecret")
		return
	}
	fmt.Println("✅ 所有必需字段都存在")

	// Step 3: 测试直接使用 accessToken 调用 API
	fmt.Println("\n[Step 3] 测试当前 AccessToken 有效性")
	fmt.Println("-" + strings.Repeat("-", 59))

	if testAPICall(accessToken, region) {
		fmt.Println("✅ 当前 AccessToken 有效，无需刷新")
	} else {
		fmt.Println("⚠️ 当前 AccessToken 无效，需要刷新")

		// Step 4: 使用 kiro2api_go1 风格刷新 token
		fmt.Println("\n[Step 4] 使用 kiro2api_go1 风格刷新 Token")
		fmt.Println("-" + strings.Repeat("-", 59))

		newToken, err := refreshIdCToken(AuthConfig{
			AuthType:     "IdC",
			RefreshToken: refreshToken,
			ClientID:     clientId,
			ClientSecret: clientSecret,
		}, region)

		if err != nil {
			fmt.Printf("❌ 刷新失败: %v\n", err)
			return
		}

		fmt.Println("✅ Token 刷新成功！")
		fmt.Printf("  新 AccessToken: %s...\n", truncate(newToken.AccessToken, 50))
		fmt.Printf("  ExpiresIn: %d 秒\n", newToken.ExpiresIn)

		// Step 5: 验证新 token
		fmt.Println("\n[Step 5] 验证新 Token")
		fmt.Println("-" + strings.Repeat("-", 59))

		if testAPICall(newToken.AccessToken, region) {
			fmt.Println("✅ 新 Token 验证成功！")

			// 保存新 token
			saveNewToken(loadedPath, newToken, tokenData)
		} else {
			fmt.Println("❌ 新 Token 验证失败")
		}
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("  测试完成")
	fmt.Println(strings.Repeat("=", 60))
}

// refreshIdCToken - 完全模拟 kiro2api_go1/auth/refresh.go 的 refreshIdCToken 函数
func refreshIdCToken(authConfig AuthConfig, region string) (*RefreshResponse, error) {
	refreshReq := IdcRefreshRequest{
		ClientId:     authConfig.ClientID,
		ClientSecret: authConfig.ClientSecret,
		GrantType:    "refresh_token",
		RefreshToken: authConfig.RefreshToken,
	}

	reqBody, err := json.Marshal(refreshReq)
	if err != nil {
		return nil, fmt.Errorf("序列化IdC请求失败: %v", err)
	}

	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(reqBody))
	if err != nil {
		return nil, fmt.Errorf("创建IdC请求失败: %v", err)
	}

	// 设置 IdC 特殊 headers（使用指纹随机化）- 完全模拟 kiro2api_go1
	fp := generateFingerprint()

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Host", fmt.Sprintf("oidc.%s.amazonaws.com", region))
	req.Header.Set("Connection", fp.ConnectionBehavior)
	req.Header.Set("x-amz-user-agent", fmt.Sprintf("aws-sdk-js/3.738.0 ua/2.1 os/%s lang/js md/browser#unknown_unknown api/sso-oidc#3.738.0 m/E KiroIDE", fp.OSType))
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", fp.AcceptLanguage)
	req.Header.Set("sec-fetch-mode", fp.SecFetchMode)
	req.Header.Set("User-Agent", "node")
	req.Header.Set("Accept-Encoding", fp.AcceptEncoding)

	fmt.Println("发送刷新请求:")
	fmt.Printf("  URL: %s\n", url)
	fmt.Println("  Headers:")
	for k, v := range req.Header {
		if k == "Content-Type" || k == "Host" || k == "X-Amz-User-Agent" || k == "User-Agent" {
			fmt.Printf("    %s: %s\n", k, v[0])
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("IdC请求失败: %v", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("IdC刷新失败: 状态码 %d, 响应: %s", resp.StatusCode, string(body))
	}

	var refreshResp RefreshResponse
	if err := json.Unmarshal(body, &refreshResp); err != nil {
		return nil, fmt.Errorf("解析IdC响应失败: %v", err)
	}

	return &refreshResp, nil
}

func testAPICall(accessToken, region string) bool {
	url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com", region)

	payload := map[string]interface{}{
		"origin":          "AI_EDITOR",
		"isEmailRequired": true,
		"resourceType":    "AGENTIC_REQUEST",
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/x-amz-json-1.0")
	req.Header.Set("x-amz-target", "AmazonCodeWhispererService.GetUsageLimits")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  请求错误: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("  API 响应: HTTP %d\n", resp.StatusCode)

	if resp.StatusCode == 200 {
		return true
	}

	fmt.Printf("  错误详情: %s\n", truncate(string(respBody), 200))
	return false
}

func saveNewToken(originalPath string, newToken *RefreshResponse, originalData map[string]interface{}) {
	// 更新 token 数据
	originalData["accessToken"] = newToken.AccessToken
	if newToken.RefreshToken != "" {
		originalData["refreshToken"] = newToken.RefreshToken
	}
	originalData["expiresAt"] = time.Now().Add(time.Duration(newToken.ExpiresIn) * time.Second).Format(time.RFC3339)

	data, _ := json.MarshalIndent(originalData, "", "  ")

	// 保存到新文件
	newPath := strings.TrimSuffix(originalPath, ".json") + "_refreshed.json"
	if err := os.WriteFile(newPath, data, 0644); err != nil {
		fmt.Printf("⚠️ 保存失败: %v\n", err)
	} else {
		fmt.Printf("✅ 新 Token 已保存到: %s\n", newPath)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
