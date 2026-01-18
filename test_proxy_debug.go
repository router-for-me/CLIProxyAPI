// 测试脚本 2：通过 CLIProxyAPIPlus 代理层排查问题
// 运行方式: go run test_proxy_debug.go
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

const (
	ProxyURL = "http://localhost:8317"
	APIKey   = "your-api-key-1"
)

func main() {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println("  CLIProxyAPIPlus 代理层问题排查")
	fmt.Println("=" + strings.Repeat("=", 59))

	// Step 1: 检查代理服务状态
	fmt.Println("\n[Step 1] 检查代理服务状态")
	fmt.Println("-" + strings.Repeat("-", 59))

	resp, err := http.Get(ProxyURL + "/health")
	if err != nil {
		fmt.Printf("❌ 代理服务不可达: %v\n", err)
		fmt.Println("请确保服务正在运行: go run ./cmd/server/main.go")
		return
	}
	resp.Body.Close()
	fmt.Printf("✅ 代理服务正常 (HTTP %d)\n", resp.StatusCode)

	// Step 2: 获取模型列表
	fmt.Println("\n[Step 2] 获取模型列表")
	fmt.Println("-" + strings.Repeat("-", 59))

	models := getModels()
	if len(models) == 0 {
		fmt.Println("❌ 没有可用的模型，检查凭据加载")
		checkCredentials()
		return
	}
	fmt.Printf("✅ 找到 %d 个模型:\n", len(models))
	for _, m := range models {
		fmt.Printf("   - %s\n", m)
	}

	// Step 3: 测试模型请求 - 捕获详细错误
	fmt.Println("\n[Step 3] 测试模型请求（详细日志）")
	fmt.Println("-" + strings.Repeat("-", 59))

	if len(models) > 0 {
		testModel := models[0]
		testModelRequest(testModel)
	}

	// Step 4: 检查代理内部 Token 状态
	fmt.Println("\n[Step 4] 检查代理服务加载的凭据")
	fmt.Println("-" + strings.Repeat("-", 59))

	checkProxyCredentials()

	// Step 5: 对比直接请求和代理请求
	fmt.Println("\n[Step 5] 对比直接请求 vs 代理请求")
	fmt.Println("-" + strings.Repeat("-", 59))

	compareDirectVsProxy()

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("  排查完成")
	fmt.Println(strings.Repeat("=", 60))
}

func getModels() []string {
	req, _ := http.NewRequest("GET", ProxyURL+"/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+APIKey)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ 请求失败: %v\n", err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != 200 {
		fmt.Printf("❌ HTTP %d: %s\n", resp.StatusCode, string(body))
		return nil
	}

	var result struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal(body, &result)

	models := make([]string, len(result.Data))
	for i, m := range result.Data {
		models[i] = m.ID
	}
	return models
}

func checkCredentials() {
	homeDir, _ := os.UserHomeDir()
	cliProxyDir := filepath.Join(homeDir, ".cli-proxy-api")

	fmt.Printf("\n检查凭据目录: %s\n", cliProxyDir)
	files, err := os.ReadDir(cliProxyDir)
	if err != nil {
		fmt.Printf("❌ 无法读取目录: %v\n", err)
		return
	}

	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".json") {
			fmt.Printf("   📄 %s\n", f.Name())
		}
	}
}

func testModelRequest(model string) {
	fmt.Printf("测试模型: %s\n", model)

	payload := map[string]interface{}{
		"model": model,
		"messages": []map[string]string{
			{"role": "user", "content": "Say 'OK' if you receive this."},
		},
		"max_tokens": 50,
		"stream":     false,
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", ProxyURL+"/v1/chat/completions", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+APIKey)
	req.Header.Set("Content-Type", "application/json")

	fmt.Println("\n发送请求:")
	fmt.Printf("  URL: %s/v1/chat/completions\n", ProxyURL)
	fmt.Printf("  Model: %s\n", model)

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ 请求失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	fmt.Printf("\n响应:\n")
	fmt.Printf("  Status: %d\n", resp.StatusCode)
	fmt.Printf("  Headers:\n")
	for k, v := range resp.Header {
		fmt.Printf("    %s: %s\n", k, strings.Join(v, ", "))
	}

	// 格式化 JSON 输出
	var prettyJSON bytes.Buffer
	if err := json.Indent(&prettyJSON, respBody, "  ", "  "); err == nil {
		fmt.Printf("  Body:\n  %s\n", prettyJSON.String())
	} else {
		fmt.Printf("  Body: %s\n", string(respBody))
	}

	if resp.StatusCode == 200 {
		fmt.Println("\n✅ 请求成功！")
	} else {
		fmt.Println("\n❌ 请求失败！分析错误原因...")
		analyzeError(respBody)
	}
}

func analyzeError(body []byte) {
	var errResp struct {
		Message string `json:"message"`
		Reason  string `json:"reason"`
		Error   struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	json.Unmarshal(body, &errResp)

	if errResp.Message != "" {
		fmt.Printf("错误消息: %s\n", errResp.Message)
	}
	if errResp.Reason != "" {
		fmt.Printf("错误原因: %s\n", errResp.Reason)
	}
	if errResp.Error.Message != "" {
		fmt.Printf("错误详情: %s (类型: %s)\n", errResp.Error.Message, errResp.Error.Type)
	}

	// 分析常见错误
	bodyStr := string(body)
	if strings.Contains(bodyStr, "bearer token") || strings.Contains(bodyStr, "invalid") {
		fmt.Println("\n可能的原因:")
		fmt.Println("  1. Token 已过期 - 需要刷新")
		fmt.Println("  2. Token 格式不正确 - 检查凭据文件")
		fmt.Println("  3. 代理服务加载了旧的 Token")
	}
}

func checkProxyCredentials() {
	// 尝试通过管理 API 获取凭据状态
	req, _ := http.NewRequest("GET", ProxyURL+"/v0/management/auth/list", nil)
	// 使用配置中的管理密钥 admin123
	req.Header.Set("Authorization", "Bearer admin123")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ 无法访问管理 API: %v\n", err)
		return
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 200 {
		fmt.Println("管理 API 返回的凭据列表:")
		var prettyJSON bytes.Buffer
		if err := json.Indent(&prettyJSON, body, "  ", "  "); err == nil {
			fmt.Printf("%s\n", prettyJSON.String())
		} else {
			fmt.Printf("%s\n", string(body))
		}
	} else {
		fmt.Printf("管理 API 返回: HTTP %d\n", resp.StatusCode)
		fmt.Printf("响应: %s\n", truncate(string(body), 200))
	}
}

func compareDirectVsProxy() {
	homeDir, _ := os.UserHomeDir()
	tokenPath := filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")

	data, err := os.ReadFile(tokenPath)
	if err != nil {
		fmt.Printf("❌ 无法读取 Token 文件: %v\n", err)
		return
	}

	var token struct {
		AccessToken string `json:"accessToken"`
		Region      string `json:"region"`
	}
	json.Unmarshal(data, &token)

	if token.Region == "" {
		token.Region = "us-east-1"
	}

	// 直接请求
	fmt.Println("\n1. 直接请求 Kiro API:")
	directSuccess := testDirectKiroAPI(token.AccessToken, token.Region)

	// 通过代理请求
	fmt.Println("\n2. 通过代理请求:")
	proxySuccess := testProxyAPI()

	// 结论
	fmt.Println("\n结论:")
	if directSuccess && !proxySuccess {
		fmt.Println("  ⚠️ 直接请求成功，代理请求失败")
		fmt.Println("  问题在于 CLIProxyAPIPlus 代理层")
		fmt.Println("  可能原因:")
		fmt.Println("    1. 代理服务使用了过期的 Token")
		fmt.Println("    2. Token 刷新逻辑有问题")
		fmt.Println("    3. 请求头构造不正确")
	} else if directSuccess && proxySuccess {
		fmt.Println("  ✅ 两者都成功")
	} else if !directSuccess && !proxySuccess {
		fmt.Println("  ❌ 两者都失败 - Token 本身可能有问题")
	}
}

func testDirectKiroAPI(accessToken, region string) bool {
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
		fmt.Printf("  ❌ 请求失败: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		fmt.Printf("  ✅ 成功 (HTTP %d)\n", resp.StatusCode)
		return true
	}
	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("  ❌ 失败 (HTTP %d): %s\n", resp.StatusCode, truncate(string(respBody), 100))
	return false
}

func testProxyAPI() bool {
	models := getModels()
	if len(models) == 0 {
		fmt.Println("  ❌ 没有可用模型")
		return false
	}

	payload := map[string]interface{}{
		"model": models[0],
		"messages": []map[string]string{
			{"role": "user", "content": "Say OK"},
		},
		"max_tokens": 10,
		"stream":     false,
	}
	body, _ := json.Marshal(payload)

	req, _ := http.NewRequest("POST", ProxyURL+"/v1/chat/completions", bytes.NewBuffer(body))
	req.Header.Set("Authorization", "Bearer "+APIKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("  ❌ 请求失败: %v\n", err)
		return false
	}
	defer resp.Body.Close()

	if resp.StatusCode == 200 {
		fmt.Printf("  ✅ 成功 (HTTP %d)\n", resp.StatusCode)
		return true
	}
	respBody, _ := io.ReadAll(resp.Body)
	fmt.Printf("  ❌ 失败 (HTTP %d): %s\n", resp.StatusCode, truncate(string(respBody), 100))
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
