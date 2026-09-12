package htmlsanitize

import (
	"bytes"
	"encoding/json"
	"html"
	"testing"
)

func TestJSONBodyEscapesStringValues(t *testing.T) {
	t.Parallel()

	got, ok := JSONBody([]byte(`{"title":"<script>alert(1)</script>","items":["safe & sound",{"description":"<b>mode</b>"}],"count":1}`))
	if !ok {
		t.Fatal("JSONBody() ok = false, want true")
	}

	var body map[string]any
	if errUnmarshal := json.Unmarshal(got, &body); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errUnmarshal, string(got))
	}
	if body["title"] != html.EscapeString("<script>alert(1)</script>") {
		t.Fatalf("title = %q, want escaped", body["title"])
	}
	items, okItems := body["items"].([]any)
	if !okItems || len(items) != 2 {
		t.Fatalf("items = %#v, want two items", body["items"])
	}
	if items[0] != html.EscapeString("safe & sound") {
		t.Fatalf("items[0] = %q, want escaped", items[0])
	}
	nested, okNested := items[1].(map[string]any)
	if !okNested {
		t.Fatalf("items[1] = %#v, want object", items[1])
	}
	if nested["description"] != html.EscapeString("<b>mode</b>") {
		t.Fatalf("description = %q, want escaped", nested["description"])
	}
	if body["count"] != float64(1) {
		t.Fatalf("count = %#v, want unchanged number", body["count"])
	}
}

func TestJSONBodyIfLikelySkipsNonJSONHTML(t *testing.T) {
	t.Parallel()

	body := []byte("<!doctype html><title>plugin</title>")
	got, ok := JSONBodyIfLikely(body, "text/html; charset=utf-8")
	if ok {
		t.Fatal("JSONBodyIfLikely() ok = true, want false")
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %q, want unchanged %q", string(got), string(body))
	}
}
