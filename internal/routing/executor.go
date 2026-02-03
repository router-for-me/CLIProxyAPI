package routing

import (
	"context"
	"errors"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

// Executor handles request execution with fallback support.
type Executor struct {
	router *Router
}

// NewExecutor creates a new executor with the given router.
func NewExecutor(router *Router) *Executor {
	return &Executor{router: router}
}

// Execute sends the request through the routing decision.
func (e *Executor) Execute(ctx context.Context, req executor.Request) (executor.Response, error) {
	decision := e.router.Resolve(req.Model)
	
	log.Debugf("routing: %s -> %s (%d candidates)", 
		decision.RequestedModel, 
		decision.ResolvedModel, 
		len(decision.Candidates))

	var lastErr error
	tried := make(map[string]struct{})

	for i, candidate := range decision.Candidates {
		key := candidate.Provider.Name() + "/" + candidate.Model
		if _, ok := tried[key]; ok {
			continue
		}
		tried[key] = struct{}{}

		log.Debugf("routing: trying candidate %d/%d: %s with model %s",
			i+1, len(decision.Candidates), candidate.Provider.Name(), candidate.Model)

		req.Model = candidate.Model
		resp, err := candidate.Provider.Execute(ctx, candidate.Model, req)
		if err == nil {
			return resp, nil
		}

		lastErr = err
		log.Debugf("routing: candidate failed: %v", err)

		// Check if it's a fatal error (not retryable)
		if isFatalError(err) {
			break
		}
	}

	if lastErr != nil {
		return executor.Response{}, lastErr
	}
	return executor.Response{}, errors.New("no available providers")
}

// ExecuteStream sends a streaming request through the routing decision.
func (e *Executor) ExecuteStream(ctx context.Context, req executor.Request) (<-chan executor.StreamChunk, error) {
	decision := e.router.Resolve(req.Model)

	log.Debugf("routing stream: %s -> %s (%d candidates)",
		decision.RequestedModel,
		decision.ResolvedModel,
		len(decision.Candidates))

	var lastErr error
	tried := make(map[string]struct{})

	for i, candidate := range decision.Candidates {
		key := candidate.Provider.Name() + "/" + candidate.Model
		if _, ok := tried[key]; ok {
			continue
		}
		tried[key] = struct{}{}

		log.Debugf("routing stream: trying candidate %d/%d: %s with model %s",
			i+1, len(decision.Candidates), candidate.Provider.Name(), candidate.Model)

		req.Model = candidate.Model
		chunks, err := candidate.Provider.ExecuteStream(ctx, candidate.Model, req)
		if err == nil {
			return chunks, nil
		}

		lastErr = err
		log.Debugf("routing stream: candidate failed: %v", err)

		if isFatalError(err) {
			break
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("no available providers")
}

// isFatalError returns true if the error is not retryable.
func isFatalError(err error) bool {
	// TODO: implement based on error type
	// For now, all errors are retryable
	return false
}
