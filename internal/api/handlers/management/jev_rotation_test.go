package management

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeRotator records the endpoint settings handed to the live classifier
// client. Both a key change and a base-URL change go through the same sink, so
// the two are recorded separately to assert which one fired.
type fakeRotator struct {
	keys []string
	urls []string
}

func (f *fakeRotator) SetAPIKey(key string)  { f.keys = append(f.keys, key) }
func (f *fakeRotator) SetBaseURL(url string) { f.urls = append(f.urls, url) }

// rotatorStore adds the optional APIKey reader the rotation path uses to read
// the stored key back after a write.
type rotatorStore struct {
	*stubJevStore
	key string
}

func (r *rotatorStore) APIKey(_ context.Context) (string, error) { return r.key, nil }

// Saving a key must push the plaintext straight to the live client, so the
// operator does not have to restart the server for classification to start.
func TestPutJevSettingsRotatesLiveKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &rotatorStore{stubJevStore: &stubJevStore{}, key: "sk-ts-freshsecret"}
	rot := &fakeRotator{}
	h := &Handler{pgJev: stub}
	h.SetJevConfigRotator(rot)

	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"api_key":"sk-ts-freshsecret"}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(rot.keys) != 1 || rot.keys[0] != "sk-ts-freshsecret" {
		t.Fatalf("rotator keys = %v, want the newly stored key", rot.keys)
	}
}

// Clearing the key must clear it on the live client too, otherwise a removed
// credential would keep working until the next restart.
func TestPutJevSettingsClearRotatesEmptyKey(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &rotatorStore{stubJevStore: &stubJevStore{}, key: ""}
	rot := &fakeRotator{}
	h := &Handler{pgJev: stub}
	h.SetJevConfigRotator(rot)

	c, _ := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"api_key":""}`)
	h.PutJevSettings(c)

	if len(rot.keys) != 1 || rot.keys[0] != "" {
		t.Fatalf("rotator keys = %v, want exactly one empty key", rot.keys)
	}
}

// A settings change that does not touch the endpoint must not disturb the live
// credential or the live base URL.
func TestPutJevSettingsWithoutKeyLeavesRotationAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &rotatorStore{stubJevStore: &stubJevStore{}, key: "sk-ts-unchanged"}
	rot := &fakeRotator{}
	h := &Handler{pgJev: stub}
	h.SetJevConfigRotator(rot)

	c, _ := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"enabled":true}`)
	h.PutJevSettings(c)

	if len(rot.keys) != 0 {
		t.Fatalf("rotator keys = %v, want none when the endpoint is untouched", rot.keys)
	}
	if len(rot.urls) != 0 {
		t.Fatalf("rotator urls = %v, want none when the endpoint is untouched", rot.urls)
	}
}

// A saved base URL must reach the live client, otherwise pointing the feature
// at a self-hosted endpoint would silently no-op until the next restart.
func TestPutJevSettingsRotatesLiveBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &rotatorStore{stubJevStore: &stubJevStore{}, key: "sk-ts-existing"}
	rot := &fakeRotator{}
	h := &Handler{pgJev: stub}
	h.SetJevConfigRotator(rot)

	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings",
		`{"base_url":"https://jev.internal.example"}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(rot.urls) != 1 || rot.urls[0] != "https://jev.internal.example" {
		t.Fatalf("rotator urls = %v, want the newly stored base URL", rot.urls)
	}
	// The key is re-pushed alongside, from the store, unchanged.
	if len(rot.keys) != 1 || rot.keys[0] != "sk-ts-existing" {
		t.Fatalf("rotator keys = %v, want the stored key re-applied", rot.keys)
	}
}

// Clearing the base URL must reset the live client to the public endpoint
// rather than leaving the previous override in place.
func TestPutJevSettingsClearResetsBaseURL(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &rotatorStore{stubJevStore: &stubJevStore{}, key: "sk-ts-existing"}
	rot := &fakeRotator{}
	h := &Handler{pgJev: stub}
	h.SetJevConfigRotator(rot)

	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"base_url":""}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(rot.urls) != 1 || rot.urls[0] == "" {
		t.Fatalf("rotator urls = %v, want the default endpoint, not blank", rot.urls)
	}
	if rot.urls[0] != "https://api.typesafe.ai" {
		t.Fatalf("rotator url = %q, want the public default", rot.urls[0])
	}
}

// Without a wired rotator the write must still succeed: the endpoints then
// apply from the next server start.
func TestPutJevSettingsWithoutRotatorStillSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &stubJevStore{}
	h := &Handler{pgJev: stub}

	c, rec := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"api_key":"sk-ts-orphan"}`)
	h.PutJevSettings(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if stub.seen == nil || *stub.seen != "sk-ts-orphan" {
		t.Fatalf("store did not receive the key: %v", stub.seen)
	}
}

var (
	_ jevSettingsStore = (*rotatorStore)(nil)
	_ JevConfigRotator = (*fakeRotator)(nil)
)
