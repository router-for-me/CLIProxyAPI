package translator

import (
	"context"
	"errors"
	"testing"
)

// fakeMiddlewareVerifier records calls to verify middleware ordering.
type fakeMiddlewareVerifier struct {
	calls   []string
	orderID int
}

func TestNewPipeline_NilRegistryUsesDefault(t *testing.T) {
	p := NewPipeline(nil)
	if p.registry != Default() {
		t.Error("NewPipeline(nil) should use Default() registry")
	}
}

func TestNewPipeline_WithCustomRegistry(t *testing.T) {
	r := NewRegistry()
	p := NewPipeline(r)
	if p.registry != r {
		t.Error("NewPipeline(custom) should use the provided registry")
	}
}

func TestPipeline_RequestSingleMiddleware(t *testing.T) {
	p := NewPipeline(nil)
	var order []string

	p.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		order = append(order, "mw")
		if req.Format != Format("from") {
			t.Errorf("middleware saw Format=%q, want %q", req.Format, "from")
		}
		return next(ctx, req)
	})

	result, err := p.TranslateRequest(context.Background(), "from", "to", RequestEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{"original":true}`),
	})
	if err != nil {
		t.Fatalf("TranslateRequest() error = %v", err)
	}
	if len(order) != 1 || order[0] != "mw" {
		t.Fatalf("middleware not called, order=%v", order)
	}
	if result.Format != "to" {
		t.Errorf("Format = %q, want %q", result.Format, "to")
	}
}

func TestPipeline_ResponseSingleMiddleware(t *testing.T) {
	p := NewPipeline(nil)
	var order []string

	p.UseResponse(func(ctx context.Context, resp ResponseEnvelope, next ResponseHandler) (ResponseEnvelope, error) {
		order = append(order, "mw")
		if resp.Format != Format("from") {
			t.Errorf("middleware saw Format=%q, want %q", resp.Format, "from")
		}
		return next(ctx, resp)
	})

	result, err := p.TranslateResponse(context.Background(), "from", "to", ResponseEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{"original":true}`),
		Stream: false,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("TranslateResponse() error = %v", err)
	}
	if len(order) != 1 || order[0] != "mw" {
		t.Fatalf("middleware not called, order=%v", order)
	}
	if result.Format != "to" {
		t.Errorf("Format = %q, want %q", result.Format, "to")
	}
}

func TestPipeline_MultipleRequestMiddlewaresExecuteInOrder(t *testing.T) {
	p := NewPipeline(nil)
	var order []string

	p.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		order = append(order, "mw1-before")
		res, err := next(ctx, req)
		order = append(order, "mw1-after")
		return res, err
	})
	p.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		order = append(order, "mw2-before")
		res, err := next(ctx, req)
		order = append(order, "mw2-after")
		return res, err
	})

	_, err := p.TranslateRequest(context.Background(), "from", "to", RequestEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("TranslateRequest() error = %v", err)
	}

	// Middleware registered first (mw1) is innermost — runs closer to terminal.
	// Execution: mw2-before → mw1-before → terminal → mw1-after → mw2-after
	want := []string{"mw1-before", "mw2-before", "mw2-after", "mw1-after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, order[i], want[i])
		}
	}
}

func TestPipeline_MultipleResponseMiddlewaresExecuteInOrder(t *testing.T) {
	p := NewPipeline(nil)
	var order []string

	p.UseResponse(func(ctx context.Context, resp ResponseEnvelope, next ResponseHandler) (ResponseEnvelope, error) {
		order = append(order, "mw1-before")
		res, err := next(ctx, resp)
		order = append(order, "mw1-after")
		return res, err
	})
	p.UseResponse(func(ctx context.Context, resp ResponseEnvelope, next ResponseHandler) (ResponseEnvelope, error) {
		order = append(order, "mw2-before")
		res, err := next(ctx, resp)
		order = append(order, "mw2-after")
		return res, err
	})

	_, err := p.TranslateResponse(context.Background(), "from", "to", ResponseEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{}`),
		Stream: true,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("TranslateResponse() error = %v", err)
	}

	// Middleware registered first (mw1) is innermost — runs closer to terminal.
	// Execution: mw2-before → mw1-before → terminal → mw1-after → mw2-after
	want := []string{"mw1-before", "mw2-before", "mw2-after", "mw1-after"}
	if len(order) != len(want) {
		t.Fatalf("order = %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, order[i], want[i])
		}
	}
}

func TestPipeline_NilMiddlewareIsSkipped(t *testing.T) {
	p := NewPipeline(nil)
	var order []string

	p.UseRequest(nil)
	p.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		order = append(order, "mw")
		return next(ctx, req)
	})
	p.UseRequest(nil)

	_, err := p.TranslateRequest(context.Background(), "from", "to", RequestEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("TranslateRequest() error = %v", err)
	}
	if len(order) != 1 {
		t.Fatalf("expected 1 middleware call, got %d: %v", len(order), order)
	}
}

func TestPipeline_RequestMiddlewareCanModifyEnvelope(t *testing.T) {
	p := NewPipeline(nil)

	p.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		req.Body = []byte(`{"modified":true}`)
		req.Model = "modified-model"
		return next(ctx, req)
	})

	result, err := p.TranslateRequest(context.Background(), "from", "to", RequestEnvelope{
		Format: "from",
		Model:  "original-model",
		Body:   []byte(`{"original":true}`),
	})
	if err != nil {
		t.Fatalf("TranslateRequest() error = %v", err)
	}
	if result.Model != "modified-model" {
		t.Errorf("Model = %q, want %q", result.Model, "modified-model")
	}
	if string(result.Body) != `{"modified":true,"model":"modified-model"}` {
		t.Errorf("Body = %s, want %s", result.Body, `{"modified":true,"model":"modified-model"}`)
	}
	if result.Format != "to" {
		t.Errorf("Format = %q, want %q", result.Format, "to")
	}
}

func TestPipeline_ResponseMiddlewareCanModifyEnvelope(t *testing.T) {
	p := NewPipeline(nil)

	p.UseResponse(func(ctx context.Context, resp ResponseEnvelope, next ResponseHandler) (ResponseEnvelope, error) {
		resp.Body = []byte(`{"modified":true}`)
		return next(ctx, resp)
	})

	reg := NewRegistry()
	reg.Register("from", "to", nil, ResponseTransform{
		NonStream: func(ctx context.Context, model string, originalRequestRawJSON, requestRawJSON, rawJSON []byte, param *any) []byte {
			return []byte(`{"from-transformer":true}`)
		},
	})
	p.registry = reg

	result, err := p.TranslateResponse(context.Background(), "from", "to", ResponseEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{"original":true}`),
		Stream: false,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("TranslateResponse() error = %v", err)
	}
	if string(result.Body) != `{"modified":true}` {
		t.Errorf("Body = %s, want %s", result.Body, `{"modified":true}`)
	}
}

func TestPipeline_MiddlewareCanShortCircuit(t *testing.T) {
	p := NewPipeline(nil)

	p.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		return RequestEnvelope{Body: []byte(`{"short-circuited":true}`), Format: "result"}, nil
	})

	result, err := p.TranslateRequest(context.Background(), "from", "to", RequestEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{}`),
	})
	if err != nil {
		t.Fatalf("TranslateRequest() error = %v", err)
	}
	if string(result.Body) != `{"short-circuited":true}` {
		t.Errorf("Body = %s, want %s", result.Body, `{"short-circuited":true}`)
	}
	if result.Format != "result" {
		t.Errorf("Format = %q, want %q", result.Format, "result")
	}
}

func TestPipeline_MiddlewareCanReturnError(t *testing.T) {
	p := NewPipeline(nil)
	expectedErr := errors.New("middleware error")

	p.UseRequest(func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error) {
		return RequestEnvelope{}, expectedErr
	})

	_, err := p.TranslateRequest(context.Background(), "from", "to", RequestEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{}`),
	})
	if err != expectedErr {
		t.Fatalf("TranslateRequest() error = %v, want %v", err, expectedErr)
	}
}

func TestPipeline_EmptyMiddlewareChain(t *testing.T) {
	p := NewPipeline(nil)
	// No middleware registered

	result, err := p.TranslateRequest(context.Background(), "from", "to", RequestEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{"model":"original"}`),
	})
	if err != nil {
		t.Fatalf("TranslateRequest() error = %v", err)
	}
	if result.Format != "to" {
		t.Errorf("Format = %q, want %q", result.Format, "to")
	}
	// Registry fallback normalizes model field when no transform is registered
	if string(result.Body) != `{"model":"model"}` {
		t.Errorf("Body = %s, want %s", result.Body, `{"model":"model"}`)
	}

	respResult, err := p.TranslateResponse(context.Background(), "from", "to", ResponseEnvelope{
		Format: "from",
		Model:  "model",
		Body:   []byte(`{"resp":true}`),
		Stream: false,
	}, nil, nil, nil)
	if err != nil {
		t.Fatalf("TranslateResponse() error = %v", err)
	}
	if respResult.Format != "to" {
		t.Errorf("Format = %q, want %q", respResult.Format, "to")
	}
}
