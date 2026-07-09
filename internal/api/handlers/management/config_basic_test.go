package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestProxyURLsHandlersReadUpdateAndDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := t.TempDir() + "/config.yaml"
	if errWrite := WriteConfig(configPath, []byte("proxy_urls: []\n")); errWrite != nil {
		t.Fatalf("WriteConfig() error = %v", errWrite)
	}
	h := NewHandler(&config.Config{
		SDKConfig: sdkconfig.SDKConfig{
			ProxyURLs: []string{"socks5://proxy-a.example.com:1080"},
		},
	}, configPath, nil)

	assertProxyURLsResponse(t, h.GetProxyURLs, nil, []string{"socks5://proxy-a.example.com:1080"})

	body := map[string]any{
		"value": []string{
			" socks5://proxy-b.example.com:1080 ",
			"",
			"socks5://proxy-c.example.com:1080",
		},
	}
	assertProxyURLsResponse(t, h.PutProxyURLs, body, nil)

	want := []string{"socks5://proxy-b.example.com:1080", "socks5://proxy-c.example.com:1080"}
	if len(h.cfg.ProxyURLs) != len(want) {
		t.Fatalf("ProxyURLs length = %d, want %d: %#v", len(h.cfg.ProxyURLs), len(want), h.cfg.ProxyURLs)
	}
	for i := range want {
		if h.cfg.ProxyURLs[i] != want[i] {
			t.Fatalf("ProxyURLs[%d] = %q, want %q", i, h.cfg.ProxyURLs[i], want[i])
		}
	}

	assertProxyURLsResponse(t, h.DeleteProxyURLs, nil, nil)
	if len(h.cfg.ProxyURLs) != 0 {
		t.Fatalf("ProxyURLs after delete = %#v, want empty", h.cfg.ProxyURLs)
	}
}

func assertProxyURLsResponse(t *testing.T, handler gin.HandlerFunc, body any, want []string) {
	t.Helper()

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	var reqBody []byte
	if body != nil {
		var errMarshal error
		reqBody, errMarshal = json.Marshal(body)
		if errMarshal != nil {
			t.Fatalf("json.Marshal() error = %v", errMarshal)
		}
	}
	req := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	ginCtx.Request = req

	handler(ginCtx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if want == nil {
		return
	}
	var got map[string][]string
	if errDecode := json.Unmarshal(recorder.Body.Bytes(), &got); errDecode != nil {
		t.Fatalf("json.Unmarshal() error = %v; body=%s", errDecode, recorder.Body.String())
	}
	values := got["proxy_urls"]
	if len(values) != len(want) {
		t.Fatalf("response proxy_urls length = %d, want %d: %#v", len(values), len(want), values)
	}
	for i := range want {
		if values[i] != want[i] {
			t.Fatalf("response proxy_urls[%d] = %q, want %q", i, values[i], want[i])
		}
	}
}
