package auth

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"
)

func testAccessTokenJWT(exp time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, exp.Unix())))
	return header + "." + payload + ".sig"
}

func TestJWTExpiry(t *testing.T) {
	t.Parallel()

	exp := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	got, ok := jwtExpiry(testAccessTokenJWT(exp))
	if !ok {
		t.Fatal("jwtExpiry() ok = false, want true")
	}
	if !got.Equal(exp) {
		t.Fatalf("jwtExpiry() = %s, want %s", got, exp)
	}

	if _, ok := jwtExpiry("opaque-token"); ok {
		t.Fatal("jwtExpiry(opaque) ok = true, want false")
	}
	if _, ok := jwtExpiry(""); ok {
		t.Fatal("jwtExpiry(empty) ok = true, want false")
	}
	if _, ok := jwtExpiry("a.b"); ok {
		t.Fatal("jwtExpiry(two-parts) ok = true, want false")
	}
}

func TestExpirationTime_PrefersAccessTokenJWTExp(t *testing.T) {
	t.Parallel()

	jwtExp := time.Date(2026, 8, 30, 6, 0, 0, 0, time.UTC)
	staleMeta := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	auth := &Auth{
		Metadata: map[string]any{
			"access_token": testAccessTokenJWT(jwtExp),
			"expired":      staleMeta.Format(time.RFC3339),
		},
	}

	got, ok := auth.ExpirationTime()
	if !ok {
		t.Fatal("ExpirationTime() ok = false, want true")
	}
	if !got.Equal(jwtExp) {
		t.Fatalf("ExpirationTime() = %s, want JWT exp %s", got, jwtExp)
	}
}

func TestExpirationTime_FallsBackToMetadataForOpaqueToken(t *testing.T) {
	t.Parallel()

	metaExp := time.Date(2026, 8, 29, 15, 0, 0, 0, time.UTC)
	auth := &Auth{
		Metadata: map[string]any{
			"access_token": "opaque-access-token",
			"expired":      metaExp.Format(time.RFC3339),
		},
	}

	got, ok := auth.ExpirationTime()
	if !ok {
		t.Fatal("ExpirationTime() ok = false, want true")
	}
	if !got.Equal(metaExp) {
		t.Fatalf("ExpirationTime() = %s, want metadata %s", got, metaExp)
	}
}

func TestAccessTokenStillValid(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 28, 8, 0, 0, 0, time.UTC)
	valid := &Auth{Metadata: map[string]any{"access_token": testAccessTokenJWT(now.Add(38 * time.Hour))}}
	expired := &Auth{Metadata: map[string]any{"access_token": testAccessTokenJWT(now.Add(-time.Minute))}}
	missing := &Auth{Metadata: map[string]any{"email": "x@example.com"}}

	if !accessTokenStillValid(valid, now) {
		t.Fatal("accessTokenStillValid(valid JWT) = false, want true")
	}
	if accessTokenStillValid(expired, now) {
		t.Fatal("accessTokenStillValid(expired JWT) = true, want false")
	}
	if accessTokenStillValid(missing, now) {
		t.Fatal("accessTokenStillValid(missing AT) = true, want false")
	}
}
