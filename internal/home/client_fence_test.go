package home

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executionregistry"
)

func TestFenceDispatchRejectsRequestPausedBeforeRegistration(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, balanceTestRedisReply)
	client.heartbeatOK.Store(true)
	registry := executionregistry.New()
	checkedHeartbeat := make(chan struct{})
	resume := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		if !client.HeartbeatOK() {
			result <- errors.New("initial heartbeat was unavailable")
			return
		}
		close(checkedHeartbeat)
		<-resume
		pending, errBegin := registry.BeginDispatch()
		if errBegin != nil {
			result <- errBegin
			return
		}
		defer pending.End()
		_, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1)
		result <- errDispatch
	}()
	select {
	case <-checkedHeartbeat:
	case <-time.After(time.Second):
		t.Fatal("request did not reach the heartbeat barrier")
	}
	client.FenceDispatch()
	if errPending := registry.WaitPending(context.Background()); errPending != nil {
		t.Fatal(errPending)
	}
	if !client.HeartbeatOK() || client.AmbiguousDispatch() {
		t.Fatal("admission fencing changed heartbeat or ambiguity")
	}
	close(resume)
	select {
	case errDispatch := <-result:
		if !errors.Is(errDispatch, ErrDispatchFenced) {
			t.Fatalf("late dispatch = %v, want fenced", errDispatch)
		}
	case <-time.After(time.Second):
		t.Fatal("late dispatch did not finish")
	}
	if len(commands.All()) != 0 {
		t.Fatalf("fenced dispatch sent commands: %#v", commands.All())
	}
}

func TestFenceDispatchPreservesIssuedRPopTransport(t *testing.T) {
	issued := make(chan struct{})
	reply := make(chan struct{}, 1)
	t.Cleanup(func() { close(reply) })
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "RPOP") {
			close(issued)
			<-reply
			return "$2\r\n{}\r\n"
		}
		return balanceTestRedisReply(args)
	})
	client.testOperationTimeout = time.Second
	client.closeBootstrapPools()
	if errEnsure := client.ensureClients(); errEnsure != nil {
		t.Fatal(errEnsure)
	}
	client.heartbeatOK.Store(true)
	commandPool, subscriptionPool := client.cmd, client.sub
	result := make(chan error, 1)
	go func() {
		raw, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1)
		if errDispatch == nil && string(raw) != "{}" {
			errDispatch = errors.New("issued response was lost")
		}
		result <- errDispatch
	}()
	select {
	case <-issued:
	case <-time.After(time.Second):
		t.Fatal("RPOP did not reach Home")
	}
	client.FenceDispatch()
	client.FenceDispatch()
	if client.cmd != commandPool || client.sub != subscriptionPool || !client.HeartbeatOK() || client.AmbiguousDispatch() {
		t.Fatal("fencing changed transport, heartbeat, or ambiguity")
	}
	select {
	case errDispatch := <-result:
		t.Fatalf("fencing interrupted the issued RPOP: %v", errDispatch)
	default:
	}
	if _, errLate := client.RPopAuth(context.Background(), "late-model", "", nil, 1); !errors.Is(errLate, ErrDispatchFenced) {
		t.Fatalf("late RPOP = %v, want fenced", errLate)
	}
	reply <- struct{}{}
	select {
	case errDispatch := <-result:
		if errDispatch != nil {
			t.Fatal(errDispatch)
		}
	case <-time.After(time.Second):
		t.Fatal("issued RPOP did not finish after its response arrived")
	}
	if len(findRedisCommand(commands.All(), "RPOP")) == 0 {
		t.Fatal("issued RPOP was not recorded")
	}
}
