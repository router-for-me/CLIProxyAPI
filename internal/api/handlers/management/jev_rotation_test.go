package management

import (
	"context"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeRotator records the keys handed to the live classifier client.
type fakeRotator struct {
	keys []string
}

func (f *fakeRotator) SetAPIKey(key string) { f.keys = append(f.keys, key) }

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
	h.SetJevKeyRotator(rot)

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
	h.SetJevKeyRotator(rot)

	c, _ := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"api_key":""}`)
	h.PutJevSettings(c)

	if len(rot.keys) != 1 || rot.keys[0] != "" {
		t.Fatalf("rotator keys = %v, want exactly one empty key", rot.keys)
	}
}

// A settings change that does not touch the key must not disturb the live
// credential.
func TestPutJevSettingsWithoutKeyLeavesRotationAlone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	stub := &rotatorStore{stubJevStore: &stubJevStore{}, key: "sk-ts-unchanged"}
	rot := &fakeRotator{}
	h := &Handler{pgJev: stub}
	h.SetJevKeyRotator(rot)

	c, _ := newJevTestContext(http.MethodPut, "/v0/management/jev/settings", `{"enabled":true}`)
	h.PutJevSettings(c)

	if len(rot.keys) != 0 {
		t.Fatalf("rotator keys = %v, want none when the key is untouched", rot.keys)
	}
}

// Without a wired rotator the write must still succeed: the key then applies
// from the next server start.
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

var _ jevSettingsStore = (*rotatorStore)(nil)
