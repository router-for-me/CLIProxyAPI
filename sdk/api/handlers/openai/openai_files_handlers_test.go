package openai

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	cliproxyfiles "github.com/router-for-me/CLIProxyAPI/v6/internal/files"
)

func TestOpenAIFilesCRUD(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store, err := cliproxyfiles.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	h := NewOpenAIFilesAPIHandler(store)
	router := gin.New()
	router.POST("/v1/files", h.Create)
	router.GET("/v1/files", h.List)
	router.GET("/v1/files/:id", h.Get)
	router.DELETE("/v1/files/:id", h.Delete)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	_ = writer.WriteField("purpose", "assistants")
	part, err := writer.CreateFormFile("file", "note.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	if _, err := io.WriteString(part, "hello file world"); err != nil {
		t.Fatalf("write upload: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)
	if resp.Code != http.StatusOK {
		t.Fatalf("create status = %d body=%s", resp.Code, resp.Body.String())
	}
	if !strings.Contains(resp.Body.String(), `"object":"file"`) {
		t.Fatalf("unexpected create body: %s", resp.Body.String())
	}
	fileID := extractJSONField(resp.Body.String(), "id")
	if !strings.HasPrefix(fileID, "file-") {
		t.Fatalf("unexpected file id: %q", fileID)
	}

	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/files", nil))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), fileID) {
		t.Fatalf("list failed: status=%d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/files/"+fileID, nil))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"filename":"note.txt"`) {
		t.Fatalf("get failed: status=%d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodDelete, "/v1/files/"+fileID, nil))
	if resp.Code != http.StatusOK || !strings.Contains(resp.Body.String(), `"deleted":true`) {
		t.Fatalf("delete failed: status=%d body=%s", resp.Code, resp.Body.String())
	}

	resp = httptest.NewRecorder()
	router.ServeHTTP(resp, httptest.NewRequest(http.MethodGet, "/v1/files/"+fileID, nil))
	if resp.Code != http.StatusNotFound {
		t.Fatalf("expected not found after delete, got status=%d body=%s", resp.Code, resp.Body.String())
	}
}

func TestPreprocessResponsesInputFilesRewritesInputFile(t *testing.T) {
	store, err := cliproxyfiles.NewStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	meta, err := store.Create(filepath.Base("example.md"), "assistants", "text/markdown", []byte("# Title\nBody text"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	raw := []byte(`{"model":"test-model","input":[{"type":"message","role":"user","content":[{"type":"input_file","file_id":"` + meta.ID + `"}]}]}`)
	updated, errMsg := preprocessResponsesInputFiles(raw, store)
	if errMsg != nil {
		t.Fatalf("preprocess err = %v", errMsg)
	}
	body := string(updated)
	if strings.Contains(body, "input_file") {
		t.Fatalf("expected input_file to be rewritten, got %s", body)
	}
	if !strings.Contains(body, "input_text") || !strings.Contains(body, meta.Filename) || !strings.Contains(body, "Body text") {
		t.Fatalf("unexpected rewritten body: %s", body)
	}
}

func extractJSONField(body string, field string) string {
	needle := `"` + field + `":"`
	idx := strings.Index(body, needle)
	if idx < 0 {
		return ""
	}
	start := idx + len(needle)
	end := strings.Index(body[start:], `"`)
	if end < 0 {
		return ""
	}
	return body[start : start+end]
}
