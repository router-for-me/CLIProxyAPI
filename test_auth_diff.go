// 测试脚本 3：对比 CLIProxyAPIPlus 与官方格式的差异
// 这个脚本分析 CLIProxyAPIPlus 保存的 token 与官方格式的差异
// 运行方式: go run test_auth_diff.go
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

func main() {
	fmt.Println("=" + strings.Repeat("=", 59))
	fmt.Println("  测试脚本 3: Token 格式差异分析")
	fmt.Println("=" + strings.Repeat("=", 59))

	homeDir := os.Getenv("USERPROFILE")

	// 加载官方 IDE Token (Kiro IDE 生成)
	fmt.Println("\n[1] 官方 Kiro IDE Token 格式")
	fmt.Println("-" + strings.Repeat("-", 59))

	ideTokenPath := filepath.Join(homeDir, ".aws", "sso", "cache", "kiro-auth-token.json")
	ideToken := loadAndAnalyze(ideTokenPath, "Kiro IDE")

	// 加载 CLIProxyAPIPlus 保存的 Token
	fmt.Println("\n[2] CLIProxyAPIPlus 保存的 Token 格式")
	fmt.Println("-" + strings.Repeat("-", 59))

	cliProxyDir := filepath.Join(homeDir, ".cli-proxy-api")
	files, _ := os.ReadDir(cliProxyDir)

	var cliProxyTokens []map[string]interface{}
	for _, f := range files {
		if strings.HasPrefix(f.Name(), "kiro") && strings.HasSuffix(f.Name(), ".json") {
			p := filepath.Join(cliProxyDir, f.Name())
			token := loadAndAnalyze(p, f.Name())
			if token != nil {
				cliProxyTokens = append(cliProxyTokens, token)
			}
		}
	}

	// 对比分析
	fmt.Println("\n[3] 关键差异分析")
	fmt.Println("-" + strings.Repeat("-", 59))

	if ideToken == nil {
		fmt.Println("❌ 无法加载 IDE Token，跳过对比")
	} else if len(cliProxyTokens) == 0 {
		fmt.Println("❌ 无法加载 CLIProxyAPIPlus Token，跳过对比")
	} else {
		// 对比最新的 CLIProxyAPIPlus token
		cliToken := cliProxyTokens[0]

		fmt.Println("\n字段对比:")
		fmt.Printf("%-20s | %-15s | %-15s\n", "字段", "IDE Token", "CLIProxy Token")
		fmt.Println(strings.Repeat("-", 55))

		fields := []string{
			"accessToken", "refreshToken", "clientId", "clientSecret",
			"authMethod", "auth_method", "provider", "region", "expiresAt", "expires_at",
		}

		for _, field := range fields {
			ideVal := getFieldStatus(ideToken, field)
			cliVal := getFieldStatus(cliToken, field)

			status := "  "
			if ideVal != cliVal {
				if ideVal == "✅ 有" && cliVal == "❌ 无" {
					status = "⚠️"
				} else if ideVal == "❌ 无" && cliVal == "✅ 有" {
					status = "📝"
				}
			}

			fmt.Printf("%-20s | %-15s | %-15s %s\n", field, ideVal, cliVal, status)
		}

		// 关键问题检测
		fmt.Println("\n🔍 问题检测:")

		// 检查 clientId/clientSecret
		if hasField(ideToken, "clientId") && !hasField(cliToken, "clientId") {
			fmt.Println("  ⚠️ 问题: CLIProxyAPIPlus 缺少 clientId 字段!")
			fmt.Println("     原因: IdC 认证刷新 token 时需要 clientId")
		}

		if hasField(ideToken, "clientSecret") && !hasField(cliToken, "clientSecret") {
			fmt.Println("  ⚠️ 问题: CLIProxyAPIPlus 缺少 clientSecret 字段!")
			fmt.Println("     原因: IdC 认证刷新 token 时需要 clientSecret")
		}

		// 检查字段名差异
		if hasField(cliToken, "auth_method") && !hasField(cliToken, "authMethod") {
			fmt.Println("  📝 注意: CLIProxy 使用 auth_method (snake_case)")
			fmt.Println("     而官方使用 authMethod (camelCase)")
		}

		if hasField(cliToken, "expires_at") && !hasField(cliToken, "expiresAt") {
			fmt.Println("  📝 注意: CLIProxy 使用 expires_at (snake_case)")
			fmt.Println("     而官方使用 expiresAt (camelCase)")
		}
	}

	// Step 4: 测试使用完整格式的 token
	fmt.Println("\n[4] 测试完整格式 Token (带 clientId/clientSecret)")
	fmt.Println("-" + strings.Repeat("-", 59))

	if ideToken != nil {
		testWithFullToken(ideToken)
	}

	fmt.Println("\n" + strings.Repeat("=", 60))
	fmt.Println("  分析完成")
	fmt.Println(strings.Repeat("=", 60))

	// 给出建议
	fmt.Println("\n💡 修复建议:")
	fmt.Println("  1. CLIProxyAPIPlus 导入 token 时需要保留 clientId 和 clientSecret")
	fmt.Println("  2. IdC 认证刷新 token 必须使用这两个字段")
	fmt.Println("  3. 检查 CLIProxyAPIPlus 的 token 导入逻辑:")
	fmt.Println("     - internal/auth/kiro/aws.go LoadKiroIDEToken()")
	fmt.Println("     - sdk/auth/kiro.go ImportFromKiroIDE()")
}

func loadAndAnalyze(path, name string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("❌ 无法加载 %s: %v\n", name, err)
		return nil
	}

	var token map[string]interface{}
	if err := json.Unmarshal(data, &token); err != nil {
		fmt.Printf("❌ 无法解析 %s: %v\n", name, err)
		return nil
	}

	fmt.Printf("📄 %s\n", path)
	fmt.Printf("   字段数: %d\n", len(token))

	// 列出所有字段
	fmt.Printf("   字段列表: ")
	keys := make([]string, 0, len(token))
	for k := range token {
		keys = append(keys, k)
	}
	fmt.Printf("%v\n", keys)

	return token
}

func getFieldStatus(token map[string]interface{}, field string) string {
	if token == nil {
		return "N/A"
	}
	if v, ok := token[field]; ok && v != nil && v != "" {
		return "✅ 有"
	}
	return "❌ 无"
}

func hasField(token map[string]interface{}, field string) bool {
	if token == nil {
		return false
	}
	v, ok := token[field]
	return ok && v != nil && v != ""
}

func testWithFullToken(token map[string]interface{}) {
	accessToken, _ := token["accessToken"].(string)
	refreshToken, _ := token["refreshToken"].(string)
	clientId, _ := token["clientId"].(string)
	clientSecret, _ := token["clientSecret"].(string)
	region, _ := token["region"].(string)

	if region == "" {
		region = "us-east-1"
	}

	// 测试当前 accessToken
	fmt.Println("\n测试当前 accessToken...")
	if testAPICall(accessToken, region) {
		fmt.Println("✅ 当前 accessToken 有效")
		return
	}

	fmt.Println("⚠️ 当前 accessToken 无效，尝试刷新...")

	// 检查是否有完整的刷新所需字段
	if clientId == "" || clientSecret == "" {
		fmt.Println("❌ 缺少 clientId 或 clientSecret，无法刷新")
		fmt.Println("   这就是问题所在！")
		return
	}

	// 尝试刷新
	fmt.Println("\n使用完整字段刷新 token...")
	url := fmt.Sprintf("https://oidc.%s.amazonaws.com/token", region)

	requestBody := map[string]interface{}{
		"refreshToken": refreshToken,
		"clientId":     clientId,
		"clientSecret": clientSecret,
		"grantType":    "refresh_token",
	}

	body, _ := json.Marshal(requestBody)
	req, _ := http.NewRequest("POST", url, bytes.NewBuffer(body))
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Printf("❌ 请求失败: %v\n", err)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode == 200 {
		var refreshResp map[string]interface{}
		json.Unmarshal(respBody, &refreshResp)

		newAccessToken, _ := refreshResp["accessToken"].(string)
		fmt.Println("✅ Token 刷新成功!")

		// 验证新 token
		if testAPICall(newAccessToken, region) {
			fmt.Println("✅ 新 Token 验证成功!")
			fmt.Println("\n✅ 结论: 使用完整格式 (含 clientId/clientSecret) 可以正常工作")
		}
	} else {
		fmt.Printf("❌ 刷新失败: HTTP %d\n", resp.StatusCode)
		fmt.Printf("   响应: %s\n", string(respBody))
	}
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
		return false
	}
	defer resp.Body.Close()

	return resp.StatusCode == 200
}
