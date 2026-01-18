// 独立测试脚本：排查 Kiro Token 403 错误
// 运行方式: go run test_kiro_debug.go
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Token 结构 - 匹配 Kiro IDE 格式
type KiroIDEToken struct {
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
	ExpiresAt    string `json:"expiresAt"`
	ClientIDHash string `json:"clientIdHash,omitempty"`
	AuthMethod   string `json:"authMethod"`
	Provider     string `json:"provider"`
	Region       string `json:"region,omitempty"`
}

// Token 结构 - 匹配 CLIProxyAPIPlus 格式
type CLIProxyToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ProfileArn   string `json:"profile_arn"`
	ExpiresAt    string `json:"expires_at"`
	AuthMethod   string `json:"auth_method"`
	Provider     string `json:"provider"`
	ClientID     string `json:"client_id,omitempty"`
	ClientSecret string `json:"client_secret,omitempty"`
	Email        string `json:"email,omitempty"`
	Type         string `json:"type"`
}

func main() {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println("  Kiro Token 403 错误排查工具")
	fmt.Println("=" + strings.Repeat("=", 59))

	homeDir, _ := os.UserHomeDir()

	// Step 1: 检查 Kiro IDE Token 文件
	fmt.Println("\n[Step 1] 检查 Kiro IDE Token 文件")
	fmt.Println("-" + strings.Repeat("-", 59))

	ideTokenPath := filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
	ideToken, err := loadKiroIDEToken(ideTokenPath)
	if err != nil {
		fmt.Printf("❌ 无法加载 Kiro IDE Token: %v\n", err)
		return
	}
	fmt.Printf("✅ Token 文件: %s\n", ideTokenPath)
	fmt.Printf("   AuthMethod: %s\n", ideToken.AuthMethod)
	fmt.Printf("   Provider: %s\n", ideToken.Provider)
	fmt.Printf("   Region: %s\n", ideToken.Region)
	fmt.Printf("   ExpiresAt: %s\n", ideToken.ExpiresAt)
	fmt.Printf("   AccessToken (前50字符): %s...\n", truncate(ideToken.AccessToken, 50))

	// Step 2: 检查 Token 过期状态
	fmt.Println("\n[Step 2] 检查 Token 过期状态")
	fmt.Println("-" + strings.Repeat("-", 59))

	expiresAt, err := parseExpiresAt(ideToken.ExpiresAt)
	if err != nil {
		fmt.Printf("❌ 无法解析过期时间: %v\n", err)
	} else {
		now := time.Now()
		if now.After(expiresAt) {
			fmt.Printf("❌ Token 已过期！过期时间: %s，当前时间: %s\n", expiresAt.Format(time.RFC3339), now.Format(time.RFC3339))
		} else {
			remaining := expiresAt.Sub(now)
			fmt.Printf("✅ Token 未过期，剩余: %s\n", remaining.Round(time.Second))
		}
	}

	// Step 3: 检查 CLIProxyAPIPlus 保存的 Token
	fmt.Println("\n[Step 3] 检查 CLIProxyAPIPlus 保存的 Token")
	fmt.Println("-" + strings.Repeat("-", 59))

	cliProxyDir := filepath.Join(homeDir, ".cli-proxy-api")
	files, _ := os.ReadDir(cliProxyDir)
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "kiro") && strings.HasSuffix(f.Name(), ".json") {
			filePath := filepath.Join(cliProxyDir, f.Name())
			cliToken, err := loadCLIProxyToken(filePath)
			if err != nil {
				fmt.Printf("❌ %s: 加载失败 - %v\n", f.Name(), err)
				continue
			}
			fmt.Printf("📄 %s:\n", f.Name())
			fmt.Printf("   AuthMethod: %s\n", cliToken.AuthMethod)
			fmt.Printf("   Provider: %s\n", cliToken.Provider)
			fmt.Printf("   ExpiresAt: %s\n", cliToken.ExpiresAt)
			fmt.Printf("   AccessToken (前50字符): %s...\n", truncate(cliToken.AccessToken, 50))

			// 比较 Token
			if cliToken.AccessToken == ideToken.AccessToken {
				fmt.Printf("   ✅ AccessToken 与 IDE Token 一致\n")
			} else {
				fmt.Printf("   ⚠️ AccessToken 与 IDE Token 不一致！\n")
			}
		}
	}

	// Step 4: 直接测试 Token 有效性 (调用 Kiro API)
	fmt.Println("\n[Step 4] 直接测试 Token 有效性")
	fmt.Println("-" + strings.Repeat("-", 59))

	testTokenValidity(ideToken.AccessToken, ideToken.Region)

	// Step 5: 测试不同的请求头格式
	fmt.Println("\n[Step 5] 测试不同的请求头格式")
	fmt.Println("-" + strings.Repeat("-", 59))

	testDifferentHeaders(ideToken.AccessToken, ideToken.Region)

	// Step 6: 解析 JWT 内容
	fmt.Println("\n[Step 6] 解析 JWT Token 内容")
	fmt.Println("-" + strings.Repeat("-", 59))

	parseJWT(ideToken.AccessToken)

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("  排查完成")
	fmt.Println(strings.Repeat("=", 60))
}

func loadKiroIDEToken(path string) (*KiroIDEToken, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var token KiroIDEToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func loadCLIProxyToken(path string) (*CLIProxyToken, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var token CLIProxyToken
	if err := json.Unmarshal(data, &token); err != nil {
		return nil, err
	}
	return &token, nil
}

func parseExpiresAt(s string) (time.Time, error) {
	formats := []string{
		time.RFC3339,
		"2006-01-02T15:04:05.000Z",
		"2006-01-02T15:04:05Z",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间格式: %s", s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func testTokenValidity(accessToken, region string) {
	if region == "" {
		region = "us-east-1"
	}

	// 测试 GetUsageLimits API
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

	fmt.Printf("请求 URL: %s\n", url)
	fmt.Printf("请求头:\n")
	for k, v := range req.Header {
		if k == "Authorization" {
			fmt.Printf("  %s: Bearer %s...\n", k, truncate(v[0][7:], 30))
		} else {
			fmt.Printf("  %s: %s\n", k, v[0])
		}
	}

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ 请求失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("响应状态: %d\n", resp.StatusCode)
	fmt.Printf("响应内容: %s\n", string(respBody))

	if resp.StatusCode == 200 {
		fmt.Println("✅ Token 有效！")
	} else if resp.StatusCode == 403 {
		fmt.Println("❌ Token 无效或已过期 (403)")
	}
}

func testDifferentHeaders(accessToken, region string) {
	if region == "" {
		region = "us-east-1"
	}

	tests := []struct {
		name    string
		headers map[string]string
	}{
		{
			name: "最小请求头",
			headers: map[string]string{
				"Content-Type":  "application/json",
				"Authorization": "Bearer " + accessToken,
			},
		},
		{
			name: "模拟 kiro2api_go1 风格",
			headers: map[string]string{
				"Content-Type":       "application/json",
				"Accept":             "text/event-stream",
				"Authorization":      "Bearer " + accessToken,
				"x-amzn-kiro-agent-mode":       "vibe",
				"x-amzn-codewhisperer-optout":  "true",
				"amz-sdk-invocation-id":        "test-invocation-id",
				"amz-sdk-request":              "attempt=1; max=3",
				"x-amz-user-agent":             "aws-sdk-js/1.0.27 KiroIDE-0.8.0-abc123",
				"User-Agent":                   "aws-sdk-js/1.0.27 ua/2.1 os/windows#10.0 lang/js md/nodejs#20.16.0 api/codewhispererstreaming#1.0.27 m/E KiroIDE-0.8.0-abc123",
			},
		},
		{
			name: "模拟 CLIProxyAPIPlus 风格",
			headers: map[string]string{
				"Content-Type":          "application/x-amz-json-1.0",
				"x-amz-target":          "AmazonCodeWhispererService.GetUsageLimits",
				"Authorization":         "Bearer " + accessToken,
				"Accept":                "application/json",
				"amz-sdk-invocation-id": "test-invocation-id",
				"amz-sdk-request":       "attempt=1; max=1",
				"Connection":            "close",
			},
		},
	}

	url := fmt.Sprintf("https://codewhisperer.%s.amazonaws.com", region)
	payload := map[string]interface{}{
		"origin":          "AI_EDITOR",
		"isEmailRequired": true,
		"resourceType":    "AGENTIC_REQUEST",
	}
	body, _ := json.Marshal(payload)

	for _, test := range tests {
		fmt.Printf("\n测试: %s\n", test.name)

		req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
		for k, v := range test.headers {
			req.Header.Set(k, v)
		}

		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			fmt.Printf("  ❌ 请求失败: %v\n", err)
			continue
		}

		respBody, _ := io.ReadAll(resp.Body)
		resp.Body.Close()

		if resp.StatusCode == 200 {
			fmt.Printf("  ✅ 成功 (HTTP %d)\n", resp.StatusCode)
		} else {
			fmt.Printf("  ❌ 失败 (HTTP %d): %s\n", resp.StatusCode, truncate(string(respBody), 100))
		}
	}
}

func parseJWT(token string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		fmt.Println("Token 不是 JWT 格式")
		return
	}

	// 解码 header
	headerData, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		fmt.Printf("无法解码 JWT header: %v\n", err)
	} else {
		var header map[string]interface{}
		json.Unmarshal(headerData, &header)
		fmt.Printf("JWT Header: %v\n", header)
	}

	// 解码 payload
	payloadData, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		fmt.Printf("无法解码 JWT payload: %v\n", err)
	} else {
		var payload map[string]interface{}
		json.Unmarshal(payloadData, &payload)
		fmt.Printf("JWT Payload:\n")
		for k, v := range payload {
			fmt.Printf("  %s: %v\n", k, v)
		}

		// 检查过期时间
		if exp, ok := payload["exp"].(float64); ok {
			expTime := time.Unix(int64(exp), 0)
			if time.Now().After(expTime) {
				fmt.Printf("  ⚠️ JWT 已过期! exp=%s\n", expTime.Format(time.RFC3339))
			} else {
				fmt.Printf("  ✅ JWT 未过期, 剩余: %s\n", expTime.Sub(time.Now()).Round(time.Second))
			}
		}
	}
}
