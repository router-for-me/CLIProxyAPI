package codex

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

func TestExpiredSameGrantReceiptRequiresRecovery(t *testing.T) {
	resetCodexRefreshGroupForTest()
	t.Cleanup(resetCodexRefreshGroupForTest)
	journal := &memoryRefreshJournal{}
	calls := 0
	auth := &CodexAuth{refreshJournal: journal, httpClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Request: req, Body: io.NopCloser(strings.NewReader(`{"access_token":"new-access","refresh_token":"same-grant","expires_in":3600}`))}, nil
	})}}
	ctx := context.Background()
	if _, errRefresh := auth.RefreshTokens(ctx, "same-grant"); errRefresh != nil {
		t.Fatal(errRefresh)
	}
	value, errRefresh := auth.RefreshTokens(ctx, "same-grant")
	if errRefresh != nil || value == nil || value.AccessToken != "new-access" || calls != 1 {
		t.Fatalf("unexpired receipt was not reused: value=%v, error=%v, calls=%d", value, errRefresh, calls)
	}
	key := refreshGrantKey(ClientID, "same-grant")
	receipt, errRead := journal.RefreshRead(ctx, key)
	if errRead != nil {
		t.Fatal(errRead)
	}
	// Advance receipt age deterministically without a wall-clock sleep.
	doc := ojson.NewDoc(receipt)
	if errSet := doc.Set("value.expired", "2000-01-01T00:00:00Z"); errSet != nil {
		t.Fatal(errSet)
	}
	if errComplete := journal.RefreshComplete(ctx, key, doc.Bytes()); errComplete != nil {
		t.Fatal(errComplete)
	}
	for attempt := 0; attempt < 2; attempt++ {
		value, errRefresh := auth.RefreshTokens(ctx, "same-grant")
		if !IsRefreshOutcomeUnknown(errRefresh) || value != nil {
			t.Fatalf("expired receipt returned success: value=%v, error=%v", value, errRefresh)
		}
		if calls != 1 {
			t.Fatalf("replayed consumed grant: upstream calls=%d", calls)
		}
	}
}
