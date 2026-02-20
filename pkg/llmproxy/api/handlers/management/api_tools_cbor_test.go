package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/fxamacker/cbor/v2"
	"github.com/gin-gonic/gin"
)

func TestAPICall_CBOR_Support(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Create a test handler
	h := &Handler{}

	// Create test request data
	reqData := apiCallRequest{
		Method: "GET",
		URL:    "https://httpbin.org/get",
		Header: map[string]string{
			"User-Agent": "test-client",
		},
	}

	t.Run("JSON request and response", func(t *testing.T) {
		// Marshal request as JSON
		jsonData, err := json.Marshal(reqData)
		if err != nil {
			t.Fatalf("Failed to marshal JSON: %v", err)
		}

		// Create HTTP request
		req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(jsonData))
		req.Header.Set("Content-Type", "application/json")

		// Create response recorder
		w := httptest.NewRecorder()

		// Create Gin context
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		// Call handler
		h.APICall(c)

		// Verify response
		if w.Code != http.StatusOK && w.Code != http.StatusBadGateway {
			t.Logf("Response status: %d", w.Code)
			t.Logf("Response body: %s", w.Body.String())
		}

		// Check content type
		contentType := w.Header().Get("Content-Type")
		if w.Code == http.StatusOK && !contains(contentType, "application/json") {
			t.Errorf("Expected JSON response, got: %s", contentType)
		}
	})

	t.Run("CBOR request and response", func(t *testing.T) {
		// Marshal request as CBOR
		cborData, err := cbor.Marshal(reqData)
		if err != nil {
			t.Fatalf("Failed to marshal CBOR: %v", err)
		}

		// Create HTTP request
		req := httptest.NewRequest(http.MethodPost, "/v0/management/api-call", bytes.NewReader(cborData))
		req.Header.Set("Content-Type", "application/cbor")

		// Create response recorder
		w := httptest.NewRecorder()

		// Create Gin context
		c, _ := gin.CreateTestContext(w)
		c.Request = req

		// Call handler
		h.APICall(c)

		// Verify response
		if w.Code != http.StatusOK && w.Code != http.StatusBadGateway {
			t.Logf("Response status: %d", w.Code)
			t.Logf("Response body: %s", w.Body.String())
		}

		// Check content type
		contentType := w.Header().Get("Content-Type")
		if w.Code == http.StatusOK && !contains(contentType, "application/cbor") {
			t.Errorf("Expected CBOR response, got: %s", contentType)
		}

		// Try to decode CBOR response
		if w.Code == http.StatusOK {
			var response apiCallResponse
			if err := cbor.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Errorf("Failed to unmarshal CBOR response: %v", err)
			} else {
				t.Logf("CBOR response decoded successfully: status_code=%d", response.StatusCode)
			}
		}
	})

	t.Run("CBOR encoding and decoding consistency", func(t *testing.T) {
		// Test data
		testReq := apiCallRequest{
			Method: "POST",
			URL:    "https://example.com/api",
			Header: map[string]string{
				"Authorization": "Bearer $TOKEN$",
				"Content-Type":  "application/json",
			},
			Data: `{"key":"value"}`,
		}

		// Encode to CBOR
		cborData, err := cbor.Marshal(testReq)
		if err != nil {
			t.Fatalf("Failed to marshal to CBOR: %v", err)
		}

		// Decode from CBOR
		var decoded apiCallRequest
		if err := cbor.Unmarshal(cborData, &decoded); err != nil {
			t.Fatalf("Failed to unmarshal from CBOR: %v", err)
		}

		// Verify fields
		if decoded.Method != testReq.Method {
			t.Errorf("Method mismatch: got %s, want %s", decoded.Method, testReq.Method)
		}
		if decoded.URL != testReq.URL {
			t.Errorf("URL mismatch: got %s, want %s", decoded.URL, testReq.URL)
		}
		if decoded.Data != testReq.Data {
			t.Errorf("Data mismatch: got %s, want %s", decoded.Data, testReq.Data)
		}
		if len(decoded.Header) != len(testReq.Header) {
			t.Errorf("Header count mismatch: got %d, want %d", len(decoded.Header), len(testReq.Header))
		}
	})
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) && s[:len(substr)] == substr || bytes.Contains([]byte(s), []byte(substr)))
}
