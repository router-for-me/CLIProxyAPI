package translator

import "context"

// RequestEnvelope represents a request in the translation pipeline.
type RequestEnvelope struct {
	Format Format
	Model  string
	Stream bool
	Body   []byte
}

// ResponseEnvelope represents a response in the translation pipeline.
type ResponseEnvelope struct {
	Format Format
	Model  string
	Stream bool
	Body   []byte
	Chunks []string
}

// RequestMiddleware decorates request translation.
type RequestMiddleware func(ctx context.Context, req RequestEnvelope, next RequestHandler) (RequestEnvelope, error)

// ResponseMiddleware decorates response translation.
type ResponseMiddleware func(ctx context.Context, resp ResponseEnvelope, next ResponseHandler) (ResponseEnvelope, error)

// RequestHandler performs request translation between formats.
type RequestHandler func(ctx context.Context, req RequestEnvelope) (RequestEnvelope, error)

// ResponseHandler performs response translation between formats.
type ResponseHandler func(ctx context.Context, resp ResponseEnvelope) (ResponseEnvelope, error)

// Pipeline orchestrates request/response transformation with middleware support.
type Pipeline struct {
	registry           *Registry
	requestMiddleware  []RequestMiddleware
	responseMiddleware []ResponseMiddleware
}

// NewPipeline constructs a pipeline bound to the provided registry.
func NewPipeline(registry *Registry) *Pipeline {
	if registry == nil {
		registry = Default()
	}
	return &Pipeline{registry: registry}
}

// UseRequest adds request middleware executed in registration order.
func (p *Pipeline) UseRequest(mw RequestMiddleware) {
	if mw != nil {
		p.requestMiddleware = append(p.requestMiddleware, mw)
	}
}

// UseResponse adds response middleware executed in registration order.
func (p *Pipeline) UseResponse(mw ResponseMiddleware) {
	if mw != nil {
		p.responseMiddleware = append(p.responseMiddleware, mw)
	}
}

// TranslateRequest applies middleware and registry transformations.
func (p *Pipeline) TranslateRequest(ctx context.Context, from, to Format, req RequestEnvelope) (RequestEnvelope, error) {
	terminal := func(ctx context.Context, input RequestEnvelope) (RequestEnvelope, error) {
		translated := p.registry.TranslateRequest(from, to, input.Model, input.Body, input.Stream)
		input.Body = translated
		input.Format = to
		return input, nil
	}

	handler := terminal
	for i := len(p.requestMiddleware) - 1; i >= 0; i-- {
		mw := p.requestMiddleware[i]
		next := handler
		handler = func(ctx context.Context, r RequestEnvelope) (RequestEnvelope, error) {
			return mw(ctx, r, next)
		}
	}

	return handler(ctx, req)
}

// TranslateResponse applies middleware and registry transformations.
func (p *Pipeline) TranslateResponse(ctx context.Context, from, to Format, resp ResponseEnvelope, originalReq, translatedReq []byte, param *any) (ResponseEnvelope, error) {
	terminal := func(ctx context.Context, input ResponseEnvelope) (ResponseEnvelope, error) {
		if input.Stream {
			input.Chunks = p.registry.TranslateStream(ctx, from, to, input.Model, originalReq, translatedReq, input.Body, param)
		} else {
			input.Body = []byte(p.registry.TranslateNonStream(ctx, from, to, input.Model, originalReq, translatedReq, input.Body, param))
		}
		input.Format = to
		return input, nil
	}

	handler := terminal
	for i := len(p.responseMiddleware) - 1; i >= 0; i-- {
		mw := p.responseMiddleware[i]
		next := handler
		handler = func(ctx context.Context, r ResponseEnvelope) (ResponseEnvelope, error) {
			return mw(ctx, r, next)
		}
	}

	return handler(ctx, resp)
}
