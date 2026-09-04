package handlers

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestModelListPolicyFormats(t *testing.T) {
	for _, tc := range []struct{ name, payload, policy, want string }{
		{"openai", `{"object":"list","data":[{"id":"a","created":9007199254740993},{"id":"b"}]}`, `["a"]`, `{"object":"list","data":[{"id":"a","created":9007199254740993}]}`},
		{"codex", `{"models":[{"slug":"a","extra":{"x":true}},{"slug":"b"}]}`, `["a"]`, `{"models":[{"slug":"a","extra":{"x":true}}]}`},
		{"gemini", `{"models":[{"name":"models/a"},{"name":"models/b"}]}`, `["a"]`, `{"models":[{"name":"models/a"}]}`},
		{"gemini-resource", `{"models":[{"name":"models/a"}]}`, `["models/a"]`, `{"models":[{"name":"models/a"}]}`},
		{"claude-boundaries", `{"data":[{"id":"a"},{"id":"b"},{"id":"c"}],"first_id":"a","last_id":"c","has_more":false}`, `["b"]`, `{"data":[{"id":"b"}],"first_id":"b","last_id":"b","has_more":false}`},
		{"empty", `{"data":[{"id":"a"}],"first_id":"a","last_id":"a"}`, `[]`, `{"data":[],"first_id":null,"last_id":null}`},
		{"exact-no-wildcard", `{"data":[{"id":"abc"},{"id":"A"}]}`, `["a","*"]`, `{"data":[]}`},
		{"duplicates", `{"data":[{"id":"a"},{"id":"b"}]}`, `["a","a"]`, `{"data":[{"id":"a"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := filterModelList(json.RawMessage(tc.payload), tc.policy)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := json.Marshal(got)
			if err != nil {
				t.Fatal(err)
			}
			var expected map[string]json.RawMessage
			if err := json.Unmarshal([]byte(tc.want), &expected); err != nil {
				t.Fatal(err)
			}
			want, _ := json.Marshal(expected)
			if string(raw) != string(want) {
				t.Fatalf("got %s, want %s", raw, want)
			}
		})
	}
}

func TestModelListPolicyFailClosed(t *testing.T) {
	for _, policy := range []string{"", "null", "{}", `[1]`, `[null]`, `[""]`, `[" a"]`, `["a"] trailing`} {
		t.Run(policy, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Set("accessMetadata", map[string]string{pluginapi.ModelListAllowedIDsMetadataKey: policy})
			WriteModelList(c, gin.H{"data": []gin.H{{"id": "private-model"}}})
			if w.Code != 500 || strings.Contains(w.Body.String(), "private-model") {
				t.Fatalf("unexpected response: %d %s", w.Code, w.Body)
			}
		})
	}
}

func TestModelListPolicyIdentityIsolation(t *testing.T) {
	payload := gin.H{"data": []gin.H{{"id": "a"}, {"id": "b"}}}
	for _, policy := range []string{`["a"]`, `["b"]`, ""} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("GET", "/v1/models?model_list_allowed_ids=[]", nil)
		c.Request.Header.Set("model_list_allowed_ids", "[]")
		if policy != "" {
			c.Set("accessMetadata", map[string]string{pluginapi.ModelListAllowedIDsMetadataKey: policy})
		}
		WriteModelList(c, payload)
		if w.Code != 200 {
			t.Fatal(w.Code)
		}
		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if policy == "" {
			if len(response.Data) != 2 {
				t.Fatal("client headers/query changed unrestricted catalog")
			}
		} else {
			if len(response.Data) != 1 || !strings.Contains(policy, `"`+response.Data[0].ID+`"`) {
				t.Fatalf("cross-identity response: %s", w.Body)
			}
			if w.Header().Get("Cache-Control") != "private, no-store" {
				t.Fatal("missing cache protection")
			}
		}
	}
}

func TestModelListPolicyInvalidCatalog(t *testing.T) {
	for _, raw := range []string{`null`, `{}`, `{"data":{}}`, `{"data":[1]}`} {
		if _, err := filterModelList(json.RawMessage(raw), `["a"]`); err == nil {
			t.Fatalf("accepted invalid catalog %s", raw)
		}
	}
}
