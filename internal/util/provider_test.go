package util

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestGetProviderNameFallsBackForMiniMaxClaudeCompatibleModel(t *testing.T) {
	got := GetProviderName("MiniMax-M2.7")
	want := []string{"claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProviderName() = %v, want %v", got, want)
	}
}

func TestGetProviderNameFallsBackForPrefixedMiniMaxModel(t *testing.T) {
	got := GetProviderName("MiniMax-国内/MiniMax-M2.7")
	want := []string{"claude"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProviderName() = %v, want %v", got, want)
	}
}

func TestGetProviderNamePrefersRegistryOverFallback(t *testing.T) {
	const clientID = "test-minimax-registry-provider"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{{ID: "MiniMax-M2.7"}})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	got := GetProviderName("MiniMax-M2.7")
	want := []string{"openai-compatibility"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProviderName() = %v, want %v", got, want)
	}
}
