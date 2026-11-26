package helps

import (
	"github.com/MoeForge/moego/base64"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func testVertexBareTinkInteractionsSignature() string {
	payload := []byte{0x01, 0x8f, 0x3d, 0x6b, 0x5f}
	for len(payload) < 405 {
		payload = append(payload, byte(0xA0+len(payload)%0x20))
	}
	return base64.StdEncoding.EncodeToString(payload)
}

func TestSanitizeGeminiInteractionsUnsupportedInputIDs_ScopesVertexBareTink(t *testing.T) {
	sig := testVertexBareTinkInteractionsSignature()
	body := []byte(`{"input":[{"type":"user_input","content":[{"type":"text","text":"hi"}]},{"type":"thought","signature":"` + sig + `"}]}`)

	public := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
	if ojson.GetBytes(public, `input.#(type=="thought")`).Exists() {
		t.Fatalf("public Interactions endpoint must drop the Vertex bare-Tink thought: %s", public)
	}

	vertex := SanitizeGeminiInteractionsUnsupportedInputIDs(body, true)
	if got := ojson.GetBytes(vertex, `input.#(type=="thought").signature`).String(); got != sig {
		t.Fatalf("Vertex Interactions endpoint must keep the bare-Tink signature, got %q", got)
	}
}

func TestSanitizeGeminiInteractionsUnsupportedInputIDs_NestedContentParts(t *testing.T) {
	const validGeminiThoughtSig = "ErUDCrIDCAISrQMBEU0yD9ECvDhSY1DQJNUGafArdfd2mDfO8VQq7XjLx/91zESuo0QPSdkRFWkLeVIocSQmQULonYMOJcs6XDLV2LTRC9myb3MCCP9CUoWbEeqhAvXKTScyS3nwBDDVJYuDDbY3YvR4V86T/DnU3qufpaVZ3wQOiJVyBVZ515dYTN+XGq7SuUc3RpfAqVU06jgxaCM0WKV4Df5mGMJWb25e/aFG2Jc7upSqpf3n6aElj+4c/eWr4GdKd0TUIElXBZ0HEN/vNcWzD3F0S4MeVbk1LDakL6HG6oyaSS2gocxYNYxqm9mdMHaXYa4mIYqWqmqBEnbgcHp8H4fgqBxc3Cx8C3otV8IarO5OALaVDA3NaXB1zjLet1587kEpkCNr9OvrYOES2nCl/i4EgbPK01nlXo+Wwm5jsZU5nEG4/Z0bErzqC5TKwOsqpJ7afL2sPWI0IGrXhXL+QCumWCS5iUtwybSkL7CYSk9GC+iY+ev6FAmC4V5JEc4OaWOc9+m/29LniN/iPTSxtUQSZT94pUa3/irIIdH7ReAS3cpeM6OTvumR1PwNxXx3XM1mEGc="

	t.Run("preserves nested function_call id and strips call_id and sibling ids", func(t *testing.T) {
		body := []byte(`{"input":[{"role":"model","id":"turn_1","content":[{"type":"text","id":"txt_1","text":"hi"},{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":{"q":"x"}}]},{"role":"user","content":[{"type":"function_result","id":"res_1","call_id":"fc_1","result":"ok"}]}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		if ojson.GetBytes(got, "input.0.id").Exists() {
			t.Fatalf("turn id must be stripped: %s", got)
		}
		if ojson.GetBytes(got, "input.0.content.0.id").Exists() {
			t.Fatalf("nested text id must be stripped: %s", got)
		}
		if id := ojson.GetBytes(got, "input.0.content.1.id").String(); id != "fc_1" {
			t.Fatalf("nested function_call id = %q, want fc_1; body=%s", id, got)
		}
		if ojson.GetBytes(got, "input.0.content.1.call_id").Exists() {
			t.Fatalf("nested function_call call_id must be stripped: %s", got)
		}
		if ojson.GetBytes(got, "input.1.content.0.id").Exists() {
			t.Fatalf("nested function_result id must be stripped: %s", got)
		}
		if callID := ojson.GetBytes(got, "input.1.content.0.call_id").String(); callID != "fc_1" {
			t.Fatalf("nested function_result call_id = %q, want fc_1; body=%s", callID, got)
		}
	})

	t.Run("promotes non-empty call_id when function_call id is missing empty or whitespace", func(t *testing.T) {
		body := []byte(`{"input":[` +
			`{"type":"function_call","id":"","call_id":"call_top_empty","name":"a","arguments":{}},` +
			`{"type":"function_call","id":"   ","call_id":"call_top_ws","name":"b","arguments":{}},` +
			`{"type":"function_call","id":"   ","call_id":"  ","name":"c","arguments":{}},` +
			`{"role":"model","content":[` +
			`{"type":"function_call","id":"","call_id":"call_nested_empty","name":"d","arguments":{}},` +
			`{"type":"function_call","id":"  ","call_id":"call_nested_ws","name":"e","arguments":{}},` +
			`{"type":"function_call","call_id":"call_nested_missing","name":"f","arguments":{}}` +
			`]}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		checks := []struct {
			idPath     string
			callIDPath string
			wantID     string
		}{
			{"input.0.id", "input.0.call_id", "call_top_empty"},
			{"input.1.id", "input.1.call_id", "call_top_ws"},
			{"input.3.content.0.id", "input.3.content.0.call_id", "call_nested_empty"},
			{"input.3.content.1.id", "input.3.content.1.call_id", "call_nested_ws"},
			{"input.3.content.2.id", "input.3.content.2.call_id", "call_nested_missing"},
		}
		for _, tc := range checks {
			if id := ojson.GetBytes(got, tc.idPath).String(); id != tc.wantID {
				t.Fatalf("%s = %q, want %q; body=%s", tc.idPath, id, tc.wantID, got)
			}
			if ojson.GetBytes(got, tc.callIDPath).Exists() {
				t.Fatalf("%s must be stripped; body=%s", tc.callIDPath, got)
			}
		}
		if ojson.GetBytes(got, "input.2.call_id").Exists() {
			t.Fatalf("input.2.call_id must be stripped; body=%s", got)
		}
	})

	t.Run("validates nested thought parts drops invalid ones and strips unsupported fields", func(t *testing.T) {
		body := []byte(`{"input":[{"role":"model","content":[` +
			`{"type":"thought","id":"th_1","text":"internal","content":[{"type":"text","text":"legacy"}],"thought_signature":"` + validGeminiThoughtSig + `","summary":[{"type":"text","text":"kept summary"}]},` +
			`{"type":"thought","signature":"not-a-valid-gemini-signature"},` +
			`{"type":"thought","signature":"skip_thought_signature_validator"},` +
			`{"type":"text","id":"txt_2","text":"answer"}` +
			`]}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		parts := ojson.GetBytes(got, "input.0.content").Array()
		if len(parts) != 2 {
			t.Fatalf("nested content len = %d, want 2; body=%s", len(parts), got)
		}
		if parts[0].Get("type").String() != "thought" {
			t.Fatalf("parts[0].type = %q, want thought; body=%s", parts[0].Get("type").String(), got)
		}
		if sig := parts[0].Get("signature").String(); sig != validGeminiThoughtSig {
			t.Fatalf("parts[0].signature = %q, want %q; body=%s", sig, validGeminiThoughtSig, got)
		}
		if summary := parts[0].Get("summary.0.text").String(); summary != "kept summary" {
			t.Fatalf("parts[0].summary.0.text = %q, want kept summary; body=%s", summary, got)
		}
		for _, field := range []string{"id", "content", "text", "thought_signature", "thoughtSignature"} {
			if parts[0].Get(field).Exists() {
				t.Fatalf("nested thought unsupported field %q must be stripped; body=%s", field, got)
			}
		}
		if parts[1].Get("type").String() != "text" || parts[1].Get("text").String() != "answer" || parts[1].Get("id").Exists() {
			t.Fatalf("parts[1] = %s, want text part without id", parts[1].Raw)
		}
	})

	t.Run("scopes Vertex bare-Tink filtering for nested thought parts", func(t *testing.T) {
		sig := testVertexBareTinkInteractionsSignature()
		body := []byte(`{"input":[{"role":"model","content":[{"type":"thought","id":"th_v","text":"raw","thoughtSignature":"` + sig + `"},{"type":"text","text":"answer"}]}]}`)

		public := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		publicParts := ojson.GetBytes(public, "input.0.content").Array()
		if len(publicParts) != 1 || publicParts[0].Get("type").String() != "text" {
			t.Fatalf("public endpoint must drop nested Vertex bare-Tink thought; body=%s", public)
		}

		vertex := SanitizeGeminiInteractionsUnsupportedInputIDs(body, true)
		vertexParts := ojson.GetBytes(vertex, "input.0.content").Array()
		if len(vertexParts) != 2 {
			t.Fatalf("Vertex endpoint must keep nested bare-Tink thought, got len=%d; body=%s", len(vertexParts), vertex)
		}
		if gotSig := vertexParts[0].Get("signature").String(); gotSig != sig {
			t.Fatalf("Vertex nested thought signature = %q, want %q; body=%s", gotSig, sig, vertex)
		}
		for _, field := range []string{"id", "text", "thoughtSignature"} {
			if vertexParts[0].Get(field).Exists() {
				t.Fatalf("Vertex nested thought unsupported field %q must be stripped; body=%s", field, vertex)
			}
		}
	})

	t.Run("compacts both top-level and nested dropped thought parts accurately", func(t *testing.T) {
		body := []byte(`{"input":[` +
			`{"type":"thought","signature":"bad-top"},` +
			`{"role":"model","content":[` +
			`{"type":"thought","signature":"bad-nested"},` +
			`{"type":"thought","thoughtSignature":"` + validGeminiThoughtSig + `","text":"strip-me"},` +
			`{"type":"function_call","id":" ","call_id":"call_mix","name":"run","arguments":{}}` +
			`]}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		items := ojson.GetBytes(got, "input").Array()
		if len(items) != 1 {
			t.Fatalf("top-level input len = %d, want 1; body=%s", len(items), got)
		}
		parts := items[0].Get("content").Array()
		if len(parts) != 2 {
			t.Fatalf("nested content len = %d, want 2; body=%s", len(parts), got)
		}
		if parts[0].Get("type").String() != "thought" || parts[0].Get("signature").String() != validGeminiThoughtSig || parts[0].Get("text").Exists() {
			t.Fatalf("parts[0] = %s, want sanitized valid thought", parts[0].Raw)
		}
		if parts[1].Get("type").String() != "function_call" || parts[1].Get("id").String() != "call_mix" || parts[1].Get("call_id").Exists() {
			t.Fatalf("parts[1] = %s, want function_call with promoted id call_mix", parts[1].Raw)
		}
	})

	t.Run("does not recreate deleted content array on kept top-level thought step", func(t *testing.T) {
		body := []byte(`{"input":[{"type":"thought","signature":"` + validGeminiThoughtSig + `","content":[{"type":"thought","signature":"bad-nested"},{"type":"function_call","id":"","call_id":"c1","name":"fn","arguments":{}}]}]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		if ojson.GetBytes(got, "input.0.content").Exists() {
			t.Fatalf("kept top-level thought must not retain or recreate content: %s", got)
		}
		if sig := ojson.GetBytes(got, "input.0.signature").String(); sig != validGeminiThoughtSig {
			t.Fatalf("input.0.signature = %q, want %q; body=%s", sig, validGeminiThoughtSig, got)
		}
	})

	t.Run("preserves verbatim call_id value when promoting to function_call id", func(t *testing.T) {
		const rawCallID = " call_1 "
		body := []byte(`{"input":[` +
			`{"type":"function_call","call_id":"` + rawCallID + `","name":"top_fn","arguments":{}},` +
			`{"type":"function_result","call_id":"` + rawCallID + `","result":"ok"},` +
			`{"role":"model","content":[{"type":"function_call","id":"  ","call_id":"` + rawCallID + `","name":"nested_fn","arguments":{}}]},` +
			`{"role":"user","content":[{"type":"function_result","call_id":"` + rawCallID + `","result":"ok"}]}` +
			`]}`)

		got := SanitizeGeminiInteractionsUnsupportedInputIDs(body, false)
		topCallID := ojson.GetBytes(got, "input.0.id").String()
		topResultCallID := ojson.GetBytes(got, "input.1.call_id").String()
		if topCallID != rawCallID || topCallID != topResultCallID {
			t.Fatalf("top-level call/result correlation mismatch: call.id=%q result.call_id=%q want=%q; body=%s", topCallID, topResultCallID, rawCallID, got)
		}
		if ojson.GetBytes(got, "input.0.call_id").Exists() {
			t.Fatalf("input.0.call_id must be stripped; body=%s", got)
		}

		nestedCallID := ojson.GetBytes(got, "input.2.content.0.id").String()
		nestedResultCallID := ojson.GetBytes(got, "input.3.content.0.call_id").String()
		if nestedCallID != rawCallID || nestedCallID != nestedResultCallID {
			t.Fatalf("nested call/result correlation mismatch: call.id=%q result.call_id=%q want=%q; body=%s", nestedCallID, nestedResultCallID, rawCallID, got)
		}
		if ojson.GetBytes(got, "input.2.content.0.call_id").Exists() {
			t.Fatalf("input.2.content.0.call_id must be stripped; body=%s", got)
		}
	})
}
