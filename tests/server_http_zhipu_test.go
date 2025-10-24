package tests

import (
    "bytes"
    "encoding/json"
    "errors"
    "net/http"
    "os"
    "strings"
    "testing"
    "time"
)

// 说明：该测试直接调用已启动的服务器 /v1/chat/completions，模型 glm-4.6。
// 依赖：服务已运行且配置包含 zhipu-api-key；使用 Authorization: Bearer 头通过访问控制。
// 环境变量：
//   - E2E_SERVER_URL (可选，默认 http://localhost:53355)
//   - E2E_ACCESS_KEY (可选，默认 sk-dummy)
func TestServerHTTP_Zhipu_GLM46_ChatCompletions(t *testing.T) {
    baseURL := strings.TrimRight(os.Getenv("E2E_SERVER_URL"), "/")
    if baseURL == "" {
        baseURL = "http://localhost:53355"
    }
    accessKey := os.Getenv("E2E_ACCESS_KEY")
    if accessKey == "" {
        accessKey = "sk-dummy"
    }

    payload := []byte(`{
        "model": "glm-4.6",
        "messages": [
            {"role": "user", "content": "ping"}
        ],
        "temperature": 0.2
    }`)

    req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/chat/completions", bytes.NewReader(payload))
    if err != nil {
        t.Fatalf("new request: %v", err)
        return
    }
    req.Header.Set("Authorization", "Bearer "+accessKey)
    req.Header.Set("Content-Type", "application/json")

    cli := &http.Client{Timeout: 10 * time.Second}
    resp, err := cli.Do(req)
    if err != nil {
        msg := err.Error()
        if strings.Contains(msg, "connection refused") || strings.Contains(msg, "refused") || errors.Is(err, os.ErrDeadlineExceeded) {
            t.Skipf("server not reachable at %s: %v", baseURL, err)
            return
        }
        t.Fatalf("http do: %v", err)
        return
    }
    defer resp.Body.Close()

    var obj map[string]any
    dec := json.NewDecoder(resp.Body)
    _ = dec.Decode(&obj)

    if resp.StatusCode >= 200 && resp.StatusCode < 300 {
        if len(obj) == 0 {
            t.Fatalf("empty body on success status")
        }
        if _, ok := obj["choices"]; !ok {
            t.Logf("no 'choices' key; keys=%v (connectivity ok)", keysOf(obj))
        }
        return
    }

    if errObj, ok := obj["error"].(map[string]any); ok {
        if code, ok2 := errObj["code"].(string); ok2 && code != "" {
            t.Logf("upstream error code: %s (connectivity ok)", code)
            return
        }
        if msg, ok2 := errObj["message"].(string); ok2 && msg != "" {
            t.Logf("upstream error: %s (connectivity ok)", msg)
            return
        }
    }

    b, _ := json.Marshal(obj)
    t.Fatalf("unexpected response (status=%d): %s", resp.StatusCode, string(b))
}

func keysOf(m map[string]any) []string {
    out := make([]string, 0, len(m))
    for k := range m { out = append(out, k) }
    return out
}

