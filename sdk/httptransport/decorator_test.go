package httptransport

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type wrappedRoundTripper struct {
	http.RoundTripper
	tag string
}

func TestDecoratorPreservesSelectionAndClientPolicy(t *testing.T) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ForceAttemptHTTP2 = false
	tr.MaxIdleConnsPerHost = 37
	c := &http.Client{
		Transport: tr,
		Timeout:   time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	if Decorate(context.Background(), c) != c {
		t.Fatal("absent decorator changed client identity")
	}
	if Decorate(WithDecorator(context.Background(), nil), c) != c {
		t.Fatal("nil decorator changed client identity")
	}

	calls := 0
	ctx := WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		calls++
		if next != tr {
			t.Fatal("selected transport replaced")
		}
		return &wrappedRoundTripper{RoundTripper: next, tag: "first"}
	})
	got := Decorate(ctx, c)
	if got == c || c.Transport != tr || got.Transport.(*wrappedRoundTripper).RoundTripper != tr || got.Timeout != c.Timeout || got.CheckRedirect == nil {
		t.Fatal("client or transport policy changed")
	}
	if Decorate(WithoutDecorator(ctx), c) != c || calls != 1 {
		t.Fatal("intermediate suppression failed")
	}
	if Decorate(WithDecorator(ctx, func(http.RoundTripper) http.RoundTripper { return nil }), c) != c {
		t.Fatal("nil decorator result changed behavior")
	}

	defaultClient := &http.Client{}
	gotDefault := Decorate(WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		if next != http.DefaultTransport {
			t.Fatalf("nil client.Transport resolved to %T, want http.DefaultTransport", next)
		}
		return &wrappedRoundTripper{RoundTripper: next, tag: "default"}
	}), defaultClient)
	if gotDefault == defaultClient || defaultClient.Transport != nil {
		t.Fatal("default client mutated or not decorated")
	}
}

func TestInheritDecoratorPreservesExplicitParentAndPropagatesFallback(t *testing.T) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	c := &http.Client{Transport: tr}

	reqCtx := WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		return &wrappedRoundTripper{RoundTripper: next, tag: "request"}
	})

	inherited := InheritDecorator(context.Background(), reqCtx)
	gotInherited := Decorate(inherited, c)
	if gotInherited == c || gotInherited.Transport.(*wrappedRoundTripper).tag != "request" {
		t.Fatal("expected decorator inherited from fallback request context")
	}

	parentCtx := WithDecorator(context.Background(), func(next http.RoundTripper) http.RoundTripper {
		return &wrappedRoundTripper{RoundTripper: next, tag: "parent"}
	})
	keptParent := InheritDecorator(parentCtx, reqCtx)
	gotParent := Decorate(keptParent, c)
	if gotParent == c || gotParent.Transport.(*wrappedRoundTripper).tag != "parent" {
		t.Fatal("expected explicit parent decorator to take precedence")
	}

	suppressedParent := InheritDecorator(WithoutDecorator(context.Background()), reqCtx)
	if Decorate(suppressedParent, c) != c {
		t.Fatal("expected WithoutDecorator on parent context to remain suppressed")
	}
}
