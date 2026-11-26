package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type issue7745StreamExecutor struct {
	identifier string
	streamFn   func(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error)
	refreshFn  func(ctx context.Context, auth *Auth) (*Auth, error)
}

func (e *issue7745StreamExecutor) Identifier() string {
	if e.identifier != "" {
		return e.identifier
	}
	return "issue7745"
}

func (e *issue7745StreamExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7745StreamExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	if e.streamFn != nil {
		return e.streamFn(ctx, auth, req, opts)
	}
	return nil, errors.New("not implemented")
}

func (e *issue7745StreamExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.refreshFn != nil {
		return e.refreshFn(ctx, auth)
	}
	return auth.Clone(), nil
}

func (e *issue7745StreamExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (e *issue7745StreamExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func waitClosedIssue7745(t *testing.T, ch <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("timed out waiting for %s", label)
	}
}

func TestIssue7745_ExecuteStreamWithModelPool_CancelsAbandonedAttemptOnModelFailover(t *testing.T) {
	outerCtx, cancelOuter := context.WithCancel(context.Background())
	t.Cleanup(cancelOuter)

	attempt1ProducerDone := make(chan struct{})
	attempt2Started := make(chan struct{})
	releaseAttempt2 := make(chan struct{})
	var releaseOnce sync.Once
	release2 := func() {
		releaseOnce.Do(func() { close(releaseAttempt2) })
	}
	t.Cleanup(release2)

	var (
		mu          sync.Mutex
		attempt1Ctx context.Context
		attempt2Ctx context.Context
		calls       atomic.Int32
	)

	exec := &issue7745StreamExecutor{
		identifier: "issue7745-pool",
		streamFn: func(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			n := calls.Add(1)
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			if n == 1 {
				mu.Lock()
				attempt1Ctx = ctx
				mu.Unlock()
				ch := make(chan cliproxyexecutor.StreamChunk)
				result := &cliproxyexecutor.StreamResult{Chunks: ch}
				go func() {
					defer close(attempt1ProducerDone)
					defer close(ch)
					select {
					case ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: http.StatusServiceUnavailable, msg: "transient_503"}}:
					case <-ctx.Done():
						return
					}
					// Keep the stream open until ctx is canceled, simulating an
					// executor or plugin producer waiting on ctx.Done(). Also read
					// result.Chunks after cancel to ensure the conductor never mutates
					// the executor-owned StreamResult struct, then emit one trailing
					// chunk on the unbuffered channel to prove discardStreamChunks drains it.
					<-ctx.Done()
					if result.Chunks == nil {
						return
					}
					ch <- cliproxyexecutor.StreamChunk{Payload: []byte("trailing")}
				}()
				return result, nil
			}
			mu.Lock()
			attempt2Ctx = ctx
			mu.Unlock()
			close(attempt2Started)
			select {
			case <-releaseAttempt2:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok-model-b\n\n")}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		},
	}

	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-7745-pool", Provider: "issue7745-pool", Status: StatusActive}

	type callOutcome struct {
		res *cliproxyexecutor.StreamResult
		err error
	}
	outcomeCh := make(chan callOutcome, 1)
	go func() {
		res, errStream := m.executeStreamWithModelPool(
			outerCtx,
			exec,
			auth,
			"issue7745-pool",
			cliproxyexecutor.Request{Model: "model-a"},
			cliproxyexecutor.Options{Stream: true},
			"route-model",
			"",
			[]string{"model-a", "model-b"},
			true,
			OAuthModelAliasResult{},
			nil,
			false,
			false,
		)
		outcomeCh <- callOutcome{res: res, err: errStream}
	}()

	waitClosedIssue7745(t, attempt2Started, "attempt 2 to start")

	mu.Lock()
	ctx1 := attempt1Ctx
	mu.Unlock()
	if ctx1 == nil {
		t.Fatal("attempt 1 context was not captured")
	}

	// Attempt 1 MUST be canceled and its producer exited BEFORE attempt 2 completes.
	waitClosedIssue7745(t, ctx1.Done(), "attempt 1 context cancellation upon model failover")
	if !errors.Is(ctx1.Err(), context.Canceled) {
		t.Fatalf("attempt 1 ctx.Err() = %v, want context.Canceled", ctx1.Err())
	}
	waitClosedIssue7745(t, attempt1ProducerDone, "attempt 1 producer exit")
	if outerCtx.Err() != nil {
		t.Fatalf("outer request ctx.Err() = %v, want nil", outerCtx.Err())
	}

	release2()

	var out callOutcome
	select {
	case out = <-outcomeCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for executeStreamWithModelPool to return")
	}
	if out.err != nil {
		t.Fatalf("executeStreamWithModelPool() error = %v, want nil", out.err)
	}
	if out.res == nil || out.res.Chunks == nil {
		t.Fatal("executeStreamWithModelPool() returned nil stream result")
	}

	var gotPayload string
	for chunk := range out.res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
		}
		gotPayload += string(chunk.Payload)
	}
	if gotPayload != "data: ok-model-b\n\n" {
		t.Fatalf("stream payload = %q, want %q", gotPayload, "data: ok-model-b\n\n")
	}

	mu.Lock()
	ctx2 := attempt2Ctx
	mu.Unlock()
	if ctx2 == nil {
		t.Fatal("attempt 2 context was not captured")
	}
	waitClosedIssue7745(t, ctx2.Done(), "attempt 2 context cancellation on wrapStreamResult exit")
	if outerCtx.Err() != nil {
		t.Fatalf("outer request ctx.Err() = %v after completion, want nil", outerCtx.Err())
	}
}

func TestIssue7745_ExecuteStream_CancelsAbandonedAttemptOnCredentialFailover(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	outerCtx, cancelOuter := context.WithCancel(context.Background())
	t.Cleanup(cancelOuter)

	provider := "issue7745-cred"
	model := "model-7745-cred-" + uuid.NewString()
	m := NewManager(nil, &FillFirstSelector{}, nil)
	m.SetRetryConfig(2, 5*time.Second, 2)

	auth1 := &Auth{ID: "auth1-7745-" + uuid.NewString(), Provider: provider, Status: StatusActive}
	auth2 := &Auth{ID: "auth2-7745-" + uuid.NewString(), Provider: provider, Status: StatusActive}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth1.ID, provider, []*registry.ModelInfo{{ID: model}})
	reg.RegisterClient(auth2.ID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})
	if _, errReg1 := m.Register(context.Background(), auth1); errReg1 != nil {
		t.Fatalf("Register(auth1): %v", errReg1)
	}
	if _, errReg2 := m.Register(context.Background(), auth2); errReg2 != nil {
		t.Fatalf("Register(auth2): %v", errReg2)
	}

	attempt1ProducerDone := make(chan struct{})
	attempt2Started := make(chan struct{})
	releaseAttempt2 := make(chan struct{})
	var releaseOnce sync.Once
	release2 := func() {
		releaseOnce.Do(func() { close(releaseAttempt2) })
	}
	t.Cleanup(release2)

	var (
		mu          sync.Mutex
		attempt1Ctx context.Context
		attempt2Ctx context.Context
		calls       atomic.Int32
	)
	m.RegisterExecutor(&issue7745StreamExecutor{
		identifier: provider,
		streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			n := calls.Add(1)
			cliproxyexecutor.MarkUpstreamAttempt(ctx)
			if n == 1 {
				mu.Lock()
				attempt1Ctx = ctx
				mu.Unlock()
				ch := make(chan cliproxyexecutor.StreamChunk)
				go func() {
					defer close(attempt1ProducerDone)
					defer close(ch)
					select {
					case ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: http.StatusServiceUnavailable, msg: "cred1_503"}}:
					case <-ctx.Done():
						return
					}
					<-ctx.Done()
				}()
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			}
			mu.Lock()
			attempt2Ctx = ctx
			mu.Unlock()
			close(attempt2Started)
			select {
			case <-releaseAttempt2:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			ch := make(chan cliproxyexecutor.StreamChunk, 1)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok-cred-2\n\n")}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		},
	})

	type callOutcome struct {
		res *cliproxyexecutor.StreamResult
		err error
	}
	outcomeCh := make(chan callOutcome, 1)
	go func() {
		res, errStream := m.ExecuteStream(outerCtx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
		outcomeCh <- callOutcome{res: res, err: errStream}
	}()

	waitClosedIssue7745(t, attempt2Started, "credential 2 attempt to start")

	mu.Lock()
	ctx1 := attempt1Ctx
	mu.Unlock()
	if ctx1 == nil {
		t.Fatal("credential 1 context was not captured")
	}

	waitClosedIssue7745(t, ctx1.Done(), "credential 1 context cancellation upon failover")
	waitClosedIssue7745(t, attempt1ProducerDone, "credential 1 producer exit")
	if outerCtx.Err() != nil {
		t.Fatalf("outer ctx.Err() = %v, want nil", outerCtx.Err())
	}

	release2()

	var out callOutcome
	select {
	case out = <-outcomeCh:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ExecuteStream to return")
	}
	if out.err != nil {
		t.Fatalf("ExecuteStream() error = %v, want nil", out.err)
	}
	for chunk := range out.res.Chunks {
		if chunk.Err != nil {
			t.Fatalf("unexpected chunk error: %v", chunk.Err)
		}
	}

	mu.Lock()
	ctx2 := attempt2Ctx
	mu.Unlock()
	waitClosedIssue7745(t, ctx2.Done(), "credential 2 context cancellation on stream close")
}

func TestIssue7745_ExecuteStreamWithModelPool_CancelsAbandonedAttemptOn401RefreshRetry(t *testing.T) {
	t.Run("after_bootstrap_error", func(t *testing.T) {
		outerCtx, cancelOuter := context.WithCancel(context.Background())
		t.Cleanup(cancelOuter)

		provider := "issue7745-401-boot"
		m := NewManager(nil, nil, nil)
		auth := &Auth{
			ID:       "auth-7745-401-boot-" + uuid.NewString(),
			Provider: provider,
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-old",
				"refresh_token": "ref-1",
			},
		}
		if _, errReg := m.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register(auth): %v", errReg)
		}

		attempt1ProducerDone := make(chan struct{})
		attempt2Started := make(chan struct{})
		releaseAttempt2 := make(chan struct{})
		var releaseOnce sync.Once
		release2 := func() {
			releaseOnce.Do(func() { close(releaseAttempt2) })
		}
		t.Cleanup(release2)

		var (
			mu                        sync.Mutex
			attempt1Ctx               context.Context
			attempt2Ctx               context.Context
			refreshErr                error
			refreshSawAttempt1Cancel  bool
			refreshSawProducerStopped bool
			calls                     atomic.Int32
		)

		exec := &issue7745StreamExecutor{
			identifier: provider,
			refreshFn: func(ctx context.Context, a *Auth) (*Auth, error) {
				mu.Lock()
				ctx1 := attempt1Ctx
				refreshErr = ctx.Err()
				mu.Unlock()
				attempt1Canceled := false
				if ctx1 != nil {
					select {
					case <-ctx1.Done():
						attempt1Canceled = true
					case <-time.After(500 * time.Millisecond):
					}
				}
				producerStopped := false
				select {
				case <-attempt1ProducerDone:
					producerStopped = true
				case <-time.After(500 * time.Millisecond):
				}
				mu.Lock()
				refreshSawAttempt1Cancel = attempt1Canceled
				refreshSawProducerStopped = producerStopped
				mu.Unlock()
				cloned := a.Clone()
				if cloned.Metadata == nil {
					cloned.Metadata = make(map[string]any)
				}
				cloned.Metadata["access_token"] = "tok-new"
				return cloned, nil
			},
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				n := calls.Add(1)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				if n == 1 {
					mu.Lock()
					attempt1Ctx = ctx
					mu.Unlock()
					ch := make(chan cliproxyexecutor.StreamChunk)
					go func() {
						defer close(attempt1ProducerDone)
						defer close(ch)
						select {
						case ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: http.StatusUnauthorized, msg: "expired_token"}}:
						case <-ctx.Done():
							return
						}
						<-ctx.Done()
					}()
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				}
				mu.Lock()
				attempt2Ctx = ctx
				mu.Unlock()
				close(attempt2Started)
				select {
				case <-releaseAttempt2:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok-after-refresh\n\n")}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		}
		m.RegisterExecutor(exec)

		type callOutcome struct {
			res *cliproxyexecutor.StreamResult
			err error
		}
		outcomeCh := make(chan callOutcome, 1)
		go func() {
			res, errStream := m.executeStreamWithModelPool(
				outerCtx,
				exec,
				auth,
				provider,
				cliproxyexecutor.Request{Model: "model-a"},
				cliproxyexecutor.Options{Stream: true},
				"model-a",
				"",
				[]string{"model-a"},
				false,
				OAuthModelAliasResult{},
				nil,
				true,
				false,
			)
			outcomeCh <- callOutcome{res: res, err: errStream}
		}()

		waitClosedIssue7745(t, attempt2Started, "401 retry attempt 2 to start")

		mu.Lock()
		ctx1 := attempt1Ctx
		refCtxErr := refreshErr
		canceledBeforeRefresh := refreshSawAttempt1Cancel
		stoppedBeforeRefresh := refreshSawProducerStopped
		mu.Unlock()
		if refCtxErr != nil {
			t.Fatalf("refresh ctx.Err() = %v, want nil (must not inherit canceled attempt 1 ctx)", refCtxErr)
		}
		if !canceledBeforeRefresh {
			t.Fatal("attempt 1 context was not canceled before Refresh began")
		}
		if !stoppedBeforeRefresh {
			t.Fatal("attempt 1 producer did not exit before Refresh completed")
		}
		waitClosedIssue7745(t, ctx1.Done(), "attempt 1 context cancellation on 401 bootstrap retry")
		waitClosedIssue7745(t, attempt1ProducerDone, "attempt 1 producer exit on 401 bootstrap retry")

		release2()

		var out callOutcome
		select {
		case out = <-outcomeCh:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for executeStreamWithModelPool")
		}
		if out.err != nil {
			t.Fatalf("executeStreamWithModelPool() error = %v, want nil", out.err)
		}
		for chunk := range out.res.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
			}
		}

		mu.Lock()
		ctx2 := attempt2Ctx
		mu.Unlock()
		waitClosedIssue7745(t, ctx2.Done(), "attempt 2 context cancellation after completion")
	})

	t.Run("after_execute_stream_error", func(t *testing.T) {
		outerCtx, cancelOuter := context.WithCancel(context.Background())
		t.Cleanup(cancelOuter)

		provider := "issue7745-401-exec"
		m := NewManager(nil, nil, nil)
		auth := &Auth{
			ID:       "auth-7745-401-exec-" + uuid.NewString(),
			Provider: provider,
			Status:   StatusActive,
			Metadata: map[string]any{
				"access_token":  "tok-old",
				"refresh_token": "ref-1",
			},
		}
		if _, errReg := m.Register(context.Background(), auth); errReg != nil {
			t.Fatalf("Register(auth): %v", errReg)
		}

		attempt2Started := make(chan struct{})
		releaseAttempt2 := make(chan struct{})
		var releaseOnce sync.Once
		release2 := func() {
			releaseOnce.Do(func() { close(releaseAttempt2) })
		}
		t.Cleanup(release2)

		var (
			mu                       sync.Mutex
			attempt1Ctx              context.Context
			attempt2Ctx              context.Context
			refreshErr               error
			refreshSawAttempt1Cancel bool
			calls                    atomic.Int32
		)

		exec := &issue7745StreamExecutor{
			identifier: provider,
			refreshFn: func(ctx context.Context, a *Auth) (*Auth, error) {
				mu.Lock()
				ctx1 := attempt1Ctx
				refreshErr = ctx.Err()
				mu.Unlock()
				attempt1Canceled := false
				if ctx1 != nil {
					select {
					case <-ctx1.Done():
						attempt1Canceled = true
					case <-time.After(500 * time.Millisecond):
					}
				}
				mu.Lock()
				refreshSawAttempt1Cancel = attempt1Canceled
				mu.Unlock()
				cloned := a.Clone()
				if cloned.Metadata == nil {
					cloned.Metadata = make(map[string]any)
				}
				cloned.Metadata["access_token"] = "tok-new"
				return cloned, nil
			},
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				n := calls.Add(1)
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				if n == 1 {
					mu.Lock()
					attempt1Ctx = ctx
					mu.Unlock()
					return nil, customStatusError{code: http.StatusUnauthorized, msg: "expired_token"}
				}
				mu.Lock()
				attempt2Ctx = ctx
				mu.Unlock()
				close(attempt2Started)
				select {
				case <-releaseAttempt2:
				case <-ctx.Done():
					return nil, ctx.Err()
				}
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok-after-exec-refresh\n\n")}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		}
		m.RegisterExecutor(exec)

		type callOutcome struct {
			res *cliproxyexecutor.StreamResult
			err error
		}
		outcomeCh := make(chan callOutcome, 1)
		go func() {
			res, errStream := m.executeStreamWithModelPool(
				outerCtx,
				exec,
				auth,
				provider,
				cliproxyexecutor.Request{Model: "model-a"},
				cliproxyexecutor.Options{Stream: true},
				"model-a",
				"",
				[]string{"model-a"},
				false,
				OAuthModelAliasResult{},
				nil,
				true,
				false,
			)
			outcomeCh <- callOutcome{res: res, err: errStream}
		}()

		waitClosedIssue7745(t, attempt2Started, "401 retry attempt 2 to start")

		mu.Lock()
		ctx1 := attempt1Ctx
		refCtxErr := refreshErr
		canceledBeforeRefresh := refreshSawAttempt1Cancel
		mu.Unlock()
		if refCtxErr != nil {
			t.Fatalf("refresh ctx.Err() = %v, want nil", refCtxErr)
		}
		if !canceledBeforeRefresh {
			t.Fatal("attempt 1 context was not canceled before Refresh began")
		}
		waitClosedIssue7745(t, ctx1.Done(), "attempt 1 context cancellation after ExecuteStream 401 error")

		release2()

		var out callOutcome
		select {
		case out = <-outcomeCh:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for executeStreamWithModelPool")
		}
		if out.err != nil {
			t.Fatalf("executeStreamWithModelPool() error = %v, want nil", out.err)
		}
		for chunk := range out.res.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected stream chunk error: %v", chunk.Err)
			}
		}

		mu.Lock()
		ctx2 := attempt2Ctx
		mu.Unlock()
		waitClosedIssue7745(t, ctx2.Done(), "attempt 2 context cancellation after completion")
	})
}

func TestIssue7745_ExecuteStreamWithModelPool_CancelsAbandonedAttemptOnEmptyStream(t *testing.T) {
	cases := []struct {
		name         string
		firstAttempt func() *cliproxyexecutor.StreamResult
	}{
		{
			name: "validate_stream_result_nil_chunks",
			firstAttempt: func() *cliproxyexecutor.StreamResult {
				return &cliproxyexecutor.StreamResult{Chunks: nil}
			},
		},
		{
			name: "closed_without_payload",
			firstAttempt: func() *cliproxyexecutor.StreamResult {
				ch := make(chan cliproxyexecutor.StreamChunk)
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outerCtx, cancelOuter := context.WithCancel(context.Background())
			t.Cleanup(cancelOuter)

			attempt2Started := make(chan struct{})
			releaseAttempt2 := make(chan struct{})
			var releaseOnce sync.Once
			release2 := func() {
				releaseOnce.Do(func() { close(releaseAttempt2) })
			}
			t.Cleanup(release2)

			var (
				mu          sync.Mutex
				attempt1Ctx context.Context
				attempt2Ctx context.Context
				calls       atomic.Int32
			)

			exec := &issue7745StreamExecutor{
				identifier: "issue7745-empty",
				streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					n := calls.Add(1)
					cliproxyexecutor.MarkUpstreamAttempt(ctx)
					if n == 1 {
						mu.Lock()
						attempt1Ctx = ctx
						mu.Unlock()
						return tc.firstAttempt(), nil
					}
					mu.Lock()
					attempt2Ctx = ctx
					mu.Unlock()
					close(attempt2Started)
					select {
					case <-releaseAttempt2:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					ch := make(chan cliproxyexecutor.StreamChunk, 1)
					ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: ok-fallback\n\n")}
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				},
			}

			m := NewManager(nil, nil, nil)
			auth := &Auth{ID: "auth-7745-empty", Provider: "issue7745-empty", Status: StatusActive}

			type callOutcome struct {
				res *cliproxyexecutor.StreamResult
				err error
			}
			outcomeCh := make(chan callOutcome, 1)
			go func() {
				res, errStream := m.executeStreamWithModelPool(
					outerCtx,
					exec,
					auth,
					"issue7745-empty",
					cliproxyexecutor.Request{Model: "model-a"},
					cliproxyexecutor.Options{Stream: true},
					"route-model",
					"",
					[]string{"model-a", "model-b"},
					true,
					OAuthModelAliasResult{},
					nil,
					false,
					false,
				)
				outcomeCh <- callOutcome{res: res, err: errStream}
			}()

			waitClosedIssue7745(t, attempt2Started, "attempt 2 to start after empty_stream")

			mu.Lock()
			ctx1 := attempt1Ctx
			mu.Unlock()
			waitClosedIssue7745(t, ctx1.Done(), "attempt 1 context cancellation after empty_stream")

			release2()

			var out callOutcome
			select {
			case out = <-outcomeCh:
			case <-time.After(time.Second):
				t.Fatal("timed out waiting for executeStreamWithModelPool")
			}
			if out.err != nil {
				t.Fatalf("executeStreamWithModelPool() error = %v, want nil", out.err)
			}
			for chunk := range out.res.Chunks {
				if chunk.Err != nil {
					t.Fatalf("unexpected chunk error: %v", chunk.Err)
				}
			}

			mu.Lock()
			ctx2 := attempt2Ctx
			mu.Unlock()
			waitClosedIssue7745(t, ctx2.Done(), "attempt 2 context cancellation after completion")
		})
	}
}

func TestIssue7745_WrapStreamResult_CancelsAttemptContextOnHandoffExit(t *testing.T) {
	t.Run("downstream_cancel", func(t *testing.T) {
		outerCtx, cancelOuter := context.WithCancel(context.Background())
		t.Cleanup(cancelOuter)

		producerDone := make(chan struct{})
		var (
			mu         sync.Mutex
			attemptCtx context.Context
		)

		exec := &issue7745StreamExecutor{
			identifier: "issue7745-handoff-cancel",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				mu.Lock()
				attemptCtx = ctx
				mu.Unlock()
				ch := make(chan cliproxyexecutor.StreamChunk)
				go func() {
					defer close(producerDone)
					defer close(ch)
					select {
					case ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: first\n\n")}:
					case <-ctx.Done():
						return
					}
					<-ctx.Done()
				}()
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		}

		m := NewManager(nil, nil, nil)
		auth := &Auth{ID: "auth-7745-handoff-cancel", Provider: "issue7745-handoff-cancel", Status: StatusActive}
		res, errStream := m.executeStreamWithModelPool(
			outerCtx,
			exec,
			auth,
			"issue7745-handoff-cancel",
			cliproxyexecutor.Request{Model: "model-a"},
			cliproxyexecutor.Options{Stream: true},
			"model-a",
			"",
			[]string{"model-a"},
			false,
			OAuthModelAliasResult{},
			nil,
			false,
			false,
		)
		if errStream != nil {
			t.Fatalf("executeStreamWithModelPool() error = %v", errStream)
		}

		select {
		case chunk, ok := <-res.Chunks:
			if !ok || string(chunk.Payload) != "data: first\n\n" {
				t.Fatalf("first chunk = (%q, ok=%v), want %q", string(chunk.Payload), ok, "data: first\n\n")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for first chunk")
		}

		cancelOuter()

		mu.Lock()
		gotCtx := attemptCtx
		mu.Unlock()
		waitClosedIssue7745(t, gotCtx.Done(), "attempt context cancellation on downstream cancel")
		waitClosedIssue7745(t, producerDone, "producer exit on downstream cancel")

		select {
		case _, ok := <-res.Chunks:
			if ok {
				t.Fatal("expected output stream channel to close after downstream cancel")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for output channel to close after downstream cancel")
		}
	})

	t.Run("normal_completion", func(t *testing.T) {
		outerCtx, cancelOuter := context.WithCancel(context.Background())
		t.Cleanup(cancelOuter)

		var (
			mu         sync.Mutex
			attemptCtx context.Context
		)

		exec := &issue7745StreamExecutor{
			identifier: "issue7745-handoff-ok",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				mu.Lock()
				attemptCtx = ctx
				mu.Unlock()
				ch := make(chan cliproxyexecutor.StreamChunk, 1)
				ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: done\n\n")}
				close(ch)
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		}

		m := NewManager(nil, nil, nil)
		auth := &Auth{ID: "auth-7745-handoff-ok", Provider: "issue7745-handoff-ok", Status: StatusActive}
		res, errStream := m.executeStreamWithModelPool(
			outerCtx,
			exec,
			auth,
			"issue7745-handoff-ok",
			cliproxyexecutor.Request{Model: "model-a"},
			cliproxyexecutor.Options{Stream: true},
			"model-a",
			"",
			[]string{"model-a"},
			false,
			OAuthModelAliasResult{},
			nil,
			false,
			false,
		)
		if errStream != nil {
			t.Fatalf("executeStreamWithModelPool() error = %v", errStream)
		}

		for chunk := range res.Chunks {
			if chunk.Err != nil {
				t.Fatalf("unexpected chunk error: %v", chunk.Err)
			}
		}

		mu.Lock()
		gotCtx := attemptCtx
		mu.Unlock()
		waitClosedIssue7745(t, gotCtx.Done(), "attempt context cancellation when wrapStreamResult exits")
		if outerCtx.Err() != nil {
			t.Fatalf("outer ctx.Err() = %v, want nil", outerCtx.Err())
		}
	})

	t.Run("terminal_error_chunk", func(t *testing.T) {
		outerCtx, cancelOuter := context.WithCancel(context.Background())
		t.Cleanup(cancelOuter)

		producerDone := make(chan struct{})
		var (
			mu         sync.Mutex
			attemptCtx context.Context
		)

		exec := &issue7745StreamExecutor{
			identifier: "issue7745-handoff-err",
			streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				mu.Lock()
				attemptCtx = ctx
				mu.Unlock()
				ch := make(chan cliproxyexecutor.StreamChunk)
				go func() {
					defer close(producerDone)
					defer close(ch)
					select {
					case ch <- cliproxyexecutor.StreamChunk{Payload: []byte("data: first\n\n")}:
					case <-ctx.Done():
						return
					}
					select {
					case ch <- cliproxyexecutor.StreamChunk{Err: customStatusError{code: http.StatusBadGateway, msg: "midstream_err"}}:
					case <-ctx.Done():
						return
					}
					// Wait on ctx.Done() before closing ch, simulating a producer
					// that relies on cancellation after emitting a terminal error chunk.
					<-ctx.Done()
					ch <- cliproxyexecutor.StreamChunk{Payload: []byte("trailing")}
				}()
				return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
			},
		}

		m := NewManager(nil, nil, nil)
		auth := &Auth{ID: "auth-7745-handoff-err", Provider: "issue7745-handoff-err", Status: StatusActive}
		res, errStream := m.executeStreamWithModelPool(
			outerCtx,
			exec,
			auth,
			"issue7745-handoff-err",
			cliproxyexecutor.Request{Model: "model-a"},
			cliproxyexecutor.Options{Stream: true},
			"model-a",
			"",
			[]string{"model-a"},
			false,
			OAuthModelAliasResult{},
			nil,
			false,
			false,
		)
		if errStream != nil {
			t.Fatalf("executeStreamWithModelPool() error = %v", errStream)
		}

		select {
		case chunk, ok := <-res.Chunks:
			if !ok || string(chunk.Payload) != "data: first\n\n" {
				t.Fatalf("first chunk = (%q, ok=%v), want %q", string(chunk.Payload), ok, "data: first\n\n")
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for first payload chunk")
		}

		select {
		case chunk, ok := <-res.Chunks:
			if !ok || chunk.Err == nil {
				t.Fatalf("second chunk = (%+v, ok=%v), want terminal error chunk", chunk, ok)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for terminal error chunk")
		}

		// While outerCtx is still live, forwarding the terminal error chunk must
		// cancel attemptCtx, let the producer exit, and close res.Chunks.
		mu.Lock()
		gotCtx := attemptCtx
		mu.Unlock()
		waitClosedIssue7745(t, gotCtx.Done(), "attempt context cancellation after terminal error chunk")
		waitClosedIssue7745(t, producerDone, "producer exit after terminal error chunk")
		select {
		case chunk, ok := <-res.Chunks:
			if ok {
				t.Fatalf("unexpected chunk after terminal error chunk: %+v", chunk)
			}
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for output channel to close after terminal error chunk")
		}
		if outerCtx.Err() != nil {
			t.Fatalf("outer ctx.Err() = %v, want nil", outerCtx.Err())
		}
	})
}
