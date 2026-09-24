package session

import (
	"fmt"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

func TestSessionFieldIndexMatchesGJSON(t *testing.T) {
	paths := []string{"session_id", "request", "contents", "metadata", "metadata.session_id", "metadata.parent_id", "metadata.user_id", "extra_body.forkSource.sessionId", "request.metadata.session_id", "missing", "request.missing"}
	payloads := []string{
		`{}`, `null`, `[]`, `"text"`,
		`{"session_id":"first","session_id":"second","metadata":{"session_id":"child","parent_id":"parent"}}`,
		`{"metadata":null,"metadata":{"session_id":"must-not-win"},"session_id":0}`,
		`{"metadata":{},"metadata":{"session_id":"later-child"}}`,
		`{"metadata":{"session_id":"first","session_id":"second"},"request":{"metadata":{"session_id":"nested"}}}`,
		`{"metadata.session_id":"literal-dot","metadata":{"session_id":"nested-dot"},"extra_body":{"forkSource":{"sessionId":"fork"}}}`,
		`{"metadat\u0061":{"session_id":"escaped-key","user_id":false},"contents":null}`,
		`{"metadata":[{"session_id":"array-value"}],"request":"not-an-object"}`,
		fmt.Sprintf(`{"contents":[{"parts":[{"inlineData":{"data":%q}}]}],"metadata":{"user_id":"after-image"}}`, strings.Repeat("A", 1<<20)),
	}
	for _, raw := range payloads {
		root := gjson.Parse(raw)
		idx := indexSessionFields(root)
		for repeat := 0; repeat < 2; repeat++ {
			for _, path := range paths {
				got, want := idx.Get(path), root.Get(path)
				if got.Type != want.Type || got.Raw != want.Raw || got.String() != want.String() || got.Exists() != want.Exists() {
					t.Fatalf("path %q differs: got %#v, want %#v", path, got, want)
				}
			}
		}
	}
}

func TestSessionExtractionLargeRequestIsolation(t *testing.T) {
	padding := strings.Repeat("x", 1<<20)
	for _, name := range []string{"alpha", "beta", "alpha"} {
		payload := []byte(fmt.Sprintf(`{"input":[{"content":%q}],"metadata":{"session_id":%q,"parent_session_id":"parent"}}`, padding, name))
		info, ok := ExtractSessionInfo(nil, payload, nil)
		if !ok || info.SessionID != "session:"+name || info.ParentSessionID != "session:parent" {
			t.Fatalf("session lookup changed identity: %#v, found=%v", info, ok)
		}
		if gjson.GetBytes(payload, "input.0.content").String() != padding {
			t.Fatal("session extraction modified request contents")
		}
	}
}
