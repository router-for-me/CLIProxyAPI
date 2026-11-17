package oauthflow

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadAndDecodeBounded(t *testing.T) {
	if b, e := ReadBounded(strings.NewReader("abcd"), 4); e != nil || string(b) != "abcd" {
		t.Fatal(b, e)
	}
	if _, e := ReadBounded(strings.NewReader("abcde"), 4); e == nil {
		t.Fatal("oversized accepted")
	}
	if _, e := ReadBounded(nil, 4); e == nil {
		t.Fatal("nil accepted")
	}
	if _, e := ReadBounded(strings.NewReader("a"), 0); e == nil {
		t.Fatal("invalid limit accepted")
	}
	for _, raw := range []string{"{", `{} {}`, `{"secret":"do-not-log",`} {
		var v any
		if e := DecodeJSON(strings.NewReader(raw), &v, 100); e == nil || strings.Contains(e.Error(), "do-not-log") {
			t.Fatal(e)
		}
	}
	var v map[string]int
	if e := DecodeJSON(strings.NewReader(`{"x":1}`), &v, 100); e != nil || v["x"] != 1 {
		t.Fatal(e)
	}
}
func TestClientBoundsWithoutMutation(t *testing.T) {
	original := &http.Client{}
	clone := AcquisitionClient(original)
	if clone == original || original.Timeout != 0 || original.CheckRedirect != nil || clone.Timeout != AcquisitionTimeout {
		t.Fatal("mutated injected client or missing timeout")
	}
	original.Timeout = time.Second
	if AcquisitionClient(original).Timeout != time.Second {
		t.Fatal("weakened timeout")
	}
	if AcquisitionClient(nil).Timeout <= 0 {
		t.Fatal("nil client unbounded")
	}
}
func TestCredentialClientRefusesRedirect(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(200) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 307) }))
	defer source.Close()
	c := AcquisitionClient(source.Client())
	req, _ := http.NewRequest("POST", source.URL, strings.NewReader("secret"))
	req.Header.Set("Authorization", "Bearer secret")
	resp, e := c.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 307 || targetCalls.Load() != 0 {
		t.Fatal("followed credential redirect")
	}
}
func TestLifetimeArithmetic(t *testing.T) {
	for _, v := range []int64{-1, 0, 1<<63 - 1} {
		if ValidLifetime(v) {
			t.Fatal(v)
		}
	}
	if !ValidLifetime(3600) {
		t.Fatal("valid lifetime rejected")
	}
}
