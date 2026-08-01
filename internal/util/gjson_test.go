package util

import "testing"

func TestGetGJSONBytesNoCopy(t *testing.T) {
	input := []byte(`{"request":{"contents":[{"role":"user"}]}}`)
	contents := GetGJSONBytesNoCopy(input, "request.contents")
	if !contents.IsArray() || contents.Get("0.role").String() != "user" {
		t.Fatalf("request.contents = %s, want user content array", contents.Raw)
	}
}

func TestGetGJSONBytesNoCopyEmptyInput(t *testing.T) {
	if result := GetGJSONBytesNoCopy(nil, "contents"); result.Exists() {
		t.Fatalf("empty input result = %s, want missing", result.Raw)
	}
}
