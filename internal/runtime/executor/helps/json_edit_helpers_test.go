package helps

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestEditJSONBytesAppliesOrderedMutations(t *testing.T) {
	body := []byte(`{"model":"old","stream":false,"previous_response_id":"resp_1","prompt_cache_retention":"keep"}`)

	got := EditJSONBytes(body,
		SetJSONEdit("model", "gpt-5"),
		SetJSONEdit("stream", true),
		DeleteJSONEdit("previous_response_id"),
		DeleteJSONEdit("prompt_cache_retention"),
	)

	if model := gjson.GetBytes(got, "model").String(); model != "gpt-5" {
		t.Fatalf("model = %q, want %q", model, "gpt-5")
	}
	if stream := gjson.GetBytes(got, "stream").Bool(); !stream {
		t.Fatal("stream = false, want true")
	}
	if gjson.GetBytes(got, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id still exists in %s", string(got))
	}
	if gjson.GetBytes(got, "prompt_cache_retention").Exists() {
		t.Fatalf("prompt_cache_retention still exists in %s", string(got))
	}
}

func TestDeleteJSONBytesSkipsMissingPath(t *testing.T) {
	body := []byte(`{"model":"gpt-5"}`)

	got, err := DeleteJSONBytes(body, "stream_options")
	if err != nil {
		t.Fatalf("DeleteJSONBytes error: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("DeleteJSONBytes changed body on missing path\n got: %s\nwant: %s", string(got), string(body))
	}
}
