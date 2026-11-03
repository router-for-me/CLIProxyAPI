package home

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDispatchFencePreservesConcurrencyReleaseUntilClose(t *testing.T) {
	client, commands := newRedisCommandTestClient(t, func(args []string) string {
		if strings.EqualFold(args[0], "LPUSH") {
			return ":1\r\n"
		}
		return balanceTestRedisReply(args)
	})
	client.FenceDispatch()
	frame := ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}
	if errRelease := client.PushConcurrencyRelease(context.Background(), frame); errRelease != nil {
		t.Fatalf("admission fence blocked final release: %v", errRelease)
	}
	if _, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1); !errors.Is(errDispatch, ErrDispatchFenced) {
		t.Fatalf("fenced dispatch = %v", errDispatch)
	}
	client.Close()
	if errRelease := client.PushConcurrencyRelease(context.Background(), frame); !errors.Is(errRelease, ErrDispatchFenced) {
		t.Fatalf("closed lifetime accepted release: %v", errRelease)
	}
	if commands.CountCommandKey("LPUSH", redisKeyConcurrencyRelease) != 1 {
		t.Fatal("closed lifetime sent another release")
	}
}

func TestPreparedMigrationKeepsReleaseAtSourceUntilTakeover(t *testing.T) {
	target, targetCommands := newRedisCommandTestClient(t, balanceTestRedisReply)
	source, sourceCommands := newRedisCommandTestClient(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "CLUSTER":
			return "$11\r\n{\"ok\":true}\r\n"
		case "LPUSH":
			return ":1\r\n"
		default:
			return balanceTestRedisReply(args)
		}
	})
	enableBalanceTestClient(source)
	if errProbe := source.rebuildCommandPoolAndProbe(context.Background()); errProbe != nil {
		t.Fatal(errProbe)
	}
	payload, errJSON := json.Marshal(rebalanceInstruction{ID: "release-plan", Term: 1, Target: clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port}})
	if errJSON != nil {
		t.Fatal(errJSON)
	}
	if errExecute := source.executeClusterRebalance(context.Background(), payload); !errors.Is(errExecute, ErrRebalanceRequested) {
		t.Fatal(errExecute)
	}
	source.FenceDispatch()
	source.heartbeatOK.Store(false)
	next := source.NewLifetime()
	t.Cleanup(next.Close)
	frame := ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}
	if errRelease := source.PushConcurrencyRelease(context.Background(), frame); errRelease != nil {
		t.Fatalf("prepared source lost release ownership: %v", errRelease)
	}
	if errRelease := next.PushConcurrencyRelease(context.Background(), frame); !errors.Is(errRelease, ErrNotConnected) {
		t.Fatalf("unacknowledged target accepted release: %v", errRelease)
	}
	// Cancellation before the subscriber reports the plan also rolls back balance state.
	source.failClusterRebalance()
	frame.ReleaseSeq++
	if errRelease := source.PushConcurrencyRelease(context.Background(), frame); errRelease != nil {
		t.Fatalf("canceling the prepared plan lost source release ownership: %v", errRelease)
	}
	if sourceCommands.CountCommandKey("LPUSH", redisKeyConcurrencyRelease) != 2 || targetCommands.CountCommandKey("LPUSH", redisKeyConcurrencyRelease) != 0 {
		t.Fatal("release was not restricted to the source membership")
	}
	source.AbortAmbiguousDispatch()
	if errRelease := source.PushConcurrencyRelease(context.Background(), frame); !errors.Is(errRelease, ErrDispatchFenced) {
		t.Fatalf("ambiguous source accepted release: %v", errRelease)
	}
}

func TestSourceReleaseOwnershipDoesNotSurviveAddressSwitch(t *testing.T) {
	target, commands := newRedisCommandTestClient(t, balanceTestRedisReply)
	source, _ := newRedisCommandTestClient(t, balanceTestRedisReply)
	if errProbe := source.rebuildCommandPoolAndProbe(context.Background()); errProbe != nil {
		t.Fatal(errProbe)
	}
	source.markMembershipTakeoverEligible()
	source.mu.Lock()
	switched := source.switchToNodeLocked(clusterNode{IP: target.homeCfg.Host, Port: target.homeCfg.Port})
	source.mu.Unlock()
	if !switched {
		t.Fatal("source address was not switched")
	}
	frame := ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}
	if errRelease := source.PushConcurrencyRelease(context.Background(), frame); !errors.Is(errRelease, ErrNotConnected) {
		t.Fatalf("switched address retained source release ownership: %v", errRelease)
	}
	if len(commands.All()) != 0 {
		t.Fatal("unacknowledged target received release traffic")
	}
}

func TestPluginStatusCancellationPreservesDispatchAndReleaseTransports(t *testing.T) {
	statusStarted := make(chan struct{})
	dispatchStarted := make(chan struct{})
	statusReply := make(chan struct{})
	dispatchReply := make(chan struct{}, 1)
	t.Cleanup(func() { close(statusReply); close(dispatchReply) })
	client, _ := newRedisCommandTestClient(t, func(args []string) string {
		switch strings.ToUpper(args[0]) {
		case "RPUSH":
			close(statusStarted)
			<-statusReply
			return ":1\r\n"
		case "RPOP":
			close(dispatchStarted)
			<-dispatchReply
			return "$2\r\n{}\r\n"
		case "LPUSH":
			return ":1\r\n"
		default:
			return balanceTestRedisReply(args)
		}
	})
	client.testOperationTimeout = 5 * time.Second
	client.closeBootstrapPools()
	if errEnsure := client.ensureClients(); errEnsure != nil {
		t.Fatal(errEnsure)
	}
	dispatchResult := make(chan error, 1)
	go func() {
		_, errDispatch := client.RPopAuth(context.Background(), "test-model", "", nil, 1)
		dispatchResult <- errDispatch
	}()
	select {
	case <-dispatchStarted:
	case <-time.After(time.Second):
		t.Fatal("dispatch did not start")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	statusResult := make(chan error, 1)
	go func() { statusResult <- client.RPushPluginStatus(ctx, []byte(`{"ok":true}`)) }()
	select {
	case <-statusStarted:
	case <-time.After(time.Second):
		t.Fatal("plugin status did not start")
	}
	cancel()
	select {
	case errStatus := <-statusResult:
		if !errors.Is(errStatus, context.Canceled) {
			t.Fatalf("plugin cancellation error = %v", errStatus)
		}
	case <-time.After(time.Second):
		t.Fatal("plugin status transport was not canceled")
	}
	if client.dispatchFenced.Load() || client.releaseFenced.Load() || client.AmbiguousDispatch() {
		t.Fatal("plugin cancellation ended the Home lifetime")
	}
	select {
	case errDispatch := <-dispatchResult:
		t.Fatalf("plugin cancellation interrupted the dispatch: %v", errDispatch)
	default:
	}
	if errRelease := client.PushConcurrencyRelease(context.Background(), ConcurrencyReleaseFrame{CredentialID: "credential-a", Model: "test-model", ReleaseSeq: 1}); errRelease != nil {
		t.Fatalf("plugin cancellation blocked release: %v", errRelease)
	}
	dispatchReply <- struct{}{}
	select {
	case errDispatch := <-dispatchResult:
		if errDispatch != nil {
			t.Fatal(errDispatch)
		}
	case <-time.After(time.Second):
		t.Fatal("issued dispatch did not finish")
	}
}
