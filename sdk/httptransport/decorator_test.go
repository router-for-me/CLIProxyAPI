package httptransport

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type wrapped struct{ http.RoundTripper }

func TestDecoratorPreservesSelectionAndClientPolicy(t *testing.T) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = false
	tr.MaxIdleConnsPerHost = 37
	c := &http.Client{Transport: tr, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	if Decorate(context.Background(), c) != c {
		t.Fatal("absent decorator changed client")
	}
	calls := 0
	ctx := WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		calls++
		if next != tr {
			t.Fatal("selected transport replaced")
		}
		return &wrapped{next}
	})
	got := Decorate(ctx, c)
	if got == c || c.Transport != tr || got.Transport.(*wrapped).RoundTripper != tr || got.Timeout != c.Timeout || got.CheckRedirect == nil {
		t.Fatal("client/transport policy changed")
	}
	if Decorate(WithoutDecorator(ctx), c) != c || calls != 1 {
		t.Fatal("intermediate suppression failed")
	}
	if Decorate(WithDecorator(ctx, func(http.RoundTripper) http.RoundTripper { return nil }), c) != c {
		t.Fatal("nil decorator result changed behavior")
	}
}
