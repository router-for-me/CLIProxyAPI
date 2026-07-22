package main

import "testing"

func TestPrettyJSON(t *testing.T) {
	got, err := prettyJSON([]byte(`{"models":[{"slug":"gpt-5.6-sol"}]}`))
	if err != nil {
		t.Fatalf("prettyJSON() error = %v", err)
	}
	want := "{\n  \"models\": [\n    {\n      \"slug\": \"gpt-5.6-sol\"\n    }\n  ]\n}\n"
	if string(got) != want {
		t.Fatalf("prettyJSON() = %q, want %q", string(got), want)
	}
}
