// 测试脚本 2：模拟 kiro2Api_js 的认证方式
// 这个脚本完整模拟 kiro-gateway/temp/kiro2Api_js 的认证逻辑
// 运行方式: go run test_auth_js_style.go
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// 常量 - 来自 kiro2Api_js/src/kiro/auth.js
const (
	REFRESH_URL_TEMPLATE     = "https://prod.{{region}}.auth.desktop.kiro.dev/refreshToken"
	REFRESH_IDC_URL_TEMPLATE = "https://oidc.{{region}}.amazonaws.com/token"
	AUTH_METHOD_SOCIAL       = "social"
	AUTH_METHOD_IDC          = "IdC"
)

func main() {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println("  测试脚本 2: kiro2Api_js 风格认证")
	fmt.Println("=" + strings.Repeat("=", 59))

	// Step 1: 加载 token 文件
	fmt.Println("\n[Step 1] 加载 Token 文件")
	fmt.Println("-" + strings.Repeat("-", 59))

	tokenPaths := []string{
		filepath.Join(os.Getenv("USERPROFILE"), ".aws", "sso", "cache", "kiro-auth-token.json"),
		"E:/ai_project_2api/kiro-gateway/configs/kiro/kiro-auth-token-1768317098.json",
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

	// 提取字段 - 模拟 kiro2Api_js/src/kiro/auth.js initializeAuth
	accessToken, _ := tokenData["accessToken"].(string)
	refreshToken, _ := tokenData["refreshToken"].(string)
	clientId, _ := tokenData["clientId"].(string)
	clientSecret, _ := tokenData["clientSecret"].(string)
	authMethod, _ := tokenData["authMethod"].(string)
	region, _ := tokenData["region"].(string)

	if region == "" {
		region = "us-east-1"
		fmt.Println("⚠️ Region 未设置，使用默认值 us-east-1")
	}

	fmt.Printf("\nToken 信息:\n")
	fmt.Printf("  AuthMethod: %s\n", authMethod)
	fmt.Printf("  Region: %s\n", region)
	fmt.Printf("  有 ClientID: %v\n", clientId != "")
	fmt.Printf("  有 ClientSecret: %v\n", clientSecret != "")

	// Step 2: 测试当前 token
	fmt.Println("\n[Step 2] 测试当前 AccessToken")
	fmt.Println("-" + strings.Repeat("-", 59))

	if testAPI(accessToken, region) {
		fmt.Println("✅ 当前 AccessToken 有效")
		return
	}

	fmt.Println("⚠️ 当前 AccessToken 无效，开始刷新...")

	// Step 3: 根据 authMethod 选择刷新方式 - 模拟 doRefreshToken
	fmt.Println("\n[Step 3] 刷新 Token (JS 风格)")
	fmt.Println("-" + strings.Repeat("-", 59))

	var refreshURL string
	var requestBody map[string]interface{}

	// 判断认证方式 - 模拟 kiro2Api_js auth.js doRefreshToken
	if authMethod == AUTH_METHOD_SOCIAL {
		// Social 认证
		refreshURL = strings.Replace(REFRESH_URL_TEMPLATE, "{{region}}", region, 1)
		requestBody = map[string]interface{}{
			"refreshToken": refreshToken,
		}
		fmt.Println("使用 Social 认证方式")
	} else {
		// IdC 认证 (默认)
		refreshURL = strings.Replace(REFRESH_IDC_URL_TEMPLATE, "{{region}}", region, 1)
		requestBody = map[string]interface{}{
			"refreshToken": refreshToken,
			"clientId":     clientId,
			"clientSecret": clientSecret,
			"grantType":    "refresh_token",
		}
		fmt.Println("使用 IdC 认证方式")
	}

	fmt.Printf("刷新 URL: %s\n", refreshURL)
	fmt.Printf("请求字段: %v\n", getKeys(requestBody))

	// 发送刷新请求
	body, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("POST", refreshURL, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ 请求失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	fmt.Printf("\n响应状态: HTTP %d\n", resp.StatusCode)

	if resp.StatusCode != 200 {
		fmt.Printf("❌ 刷新失败: %s\n", string(respBody))

		// 分析错误
		var errResp map[string]interface{}
		if err := json.Unmarshal(respBody, &errResp); err == nil {
			if errType, ok := errResp["error"].(string); ok {
				fmt.Printf("错误类型: %s\n", errType)
				if errType == "invalid_grant" {
					fmt.Println("\n💡 提示: refresh_token 可能已过期，需要重新授权")
				}
			}
			if errDesc, ok := errResp["error_description"].(string); ok {
				fmt.Printf("错误描述: %s\n", errDesc)
			}
		}
		return
	}

	// 解析响应
	var refreshResp map[string]interface{}
	json.Unmarshal(respBody, &refreshResp)

	newAccessToken, _ := refreshResp["accessToken"].(string)
	newRefreshToken, _ := refreshResp["refreshToken"].(string)
	expiresIn, _ := refreshResp["expiresIn"].(float64)

	fmt.Println("✅ Token 刷新成功!")
	fmt.Printf("  新 AccessToken: %s...\n", truncate(newAccessToken, 50))
	fmt.Printf("  ExpiresIn: %.0f 秒\n", expiresIn)
	if newRefreshToken != "" {
		fmt.Printf("  新 RefreshToken: %s...\n", truncate(newRefreshToken, 50))
	}

	// Step 4: 验证新 token
	fmt.Println("\n[Step 4] 验证新 Token")
	fmt.Println("-" + strings.Repeat("-", 59))

	if testAPI(newAccessToken, region) {
		fmt.Println("✅ 新 Token 验证成功!")

		// 保存新 token - 模拟 saveCredentialsToFile
		tokenData["accessToken"] = newAccessToken
		if newRefreshToken != "" {
			tokenData["refreshToken"] = newRefreshToken
		}
		tokenData["expiresAt"] = time.Now().Add(time.Duration(expiresIn) * time.Second).Format(time.RFC3339)

		saveData, _ := json.MarshalIndent(tokenData, "", "  ")
		newPath := strings.TrimSuffix(loadedPath, ".json") + "_js_refreshed.json"
		os.WriteFile(newPath, saveData, 0644)
		fmt.Printf("✅ 已保存到: %s\n", newPath)
	} else {
		fmt.Println("❌ 新 Token 验证失败")
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("  测试完成")
	fmt.Println(strings.Repeat("=", 60))
}

func testAPI(accessToken, region string) bool {
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
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}

func getKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
