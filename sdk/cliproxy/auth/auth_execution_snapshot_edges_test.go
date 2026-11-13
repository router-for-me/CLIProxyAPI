package auth

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAuthPolicySnapshotPreservesEmptyCollections(t *testing.T) {
	a := &Auth{Metadata: map[string]any{"strings": []string{}, "bytes": []byte{}, "array": []any{}, "object": map[string]any{}}}
	snapshot, err := snapshotAuthForPolicy(a)
	if err != nil || snapshot == nil || !reflect.DeepEqual(snapshot.Metadata, a.Metadata) {
		t.Fatal("policy snapshot changed empty collection identity")
	}
}

func TestAuthPolicySnapshotNumberSyntax(t *testing.T) {
	for _, number := range []string{"-1.25e3", "true", "null", `"1"`, "01"} {
		t.Run(number, func(t *testing.T) {
			a := &Auth{Metadata: map[string]any{"number": json.Number(number)}}
			snapshot, err := snapshotAuthForPolicy(a)
			if number == "-1.25e3" {
				if err != nil || snapshot == nil || snapshot.Metadata["number"] != json.Number(number) {
					t.Fatal("valid JSON number changed identity")
				}
			} else if err == nil || snapshot != nil {
				t.Fatal("invalid JSON number reached policy snapshot")
			}
		})
	}
}

func TestAuthPolicySnapshotRejectsOversizedMap(t *testing.T) {
	metadata := make(map[string]any, 10000)
	for i := 0; i < 10000; i++ {
		metadata[strconv.Itoa(i)] = true
	}
	if snapshot, err := snapshotAuthForPolicy(&Auth{Metadata: metadata}); err == nil || snapshot != nil {
		t.Fatal("oversized metadata map reached policy snapshot")
	}
}

func TestAuthExecutionPolicyCancellationDuringCheck(t *testing.T) {
	for _, kind := range authUsePaths {
		t.Run(string(kind), func(t *testing.T) {
			f := newAuthUseFixture(t)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			f.ctx = ctx
			checks := 0
			f.manager.SetAuthExecutionPolicy(authUsePolicyFunc(func(context.Context, AuthUse) error { checks++; cancel(); return nil }))
			err := f.invoke(kind, cliproxyexecutor.Options{})
			prepares, inputs := f.exec.observed()
			if !errors.Is(err, context.Canceled) || checks != 1 || prepares != 0 || len(inputs) != 0 {
				t.Fatal("canceled policy check reached provider IO")
			}
			f.requireNoHealthResult(t)
		})
	}
}
