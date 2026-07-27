package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
)

func TestManagerBuiltinSchedulerSharedAliasNeverSelectsImageOnlyAuthForChat(t *testing.T) {
	const (
		provider = "shared-endpoint-fast-path"
		modelID  = "shared-endpoint-model"
	)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: provider})
	modelRegistry := registry.GetGlobalRegistry()

	registrations := []struct {
		id   string
		info *registry.ModelInfo
	}{
		{
			id: "shared-endpoint-chat-auth",
			info: &registry.ModelInfo{
				ID:   modelID,
				Type: provider,
			},
		},
		{
			id: "shared-endpoint-image-auth",
			info: &registry.ModelInfo{
				ID:               modelID,
				Type:             registry.OpenAIImageModelType,
				SupportsImageAPI: true,
				ChatDisabled:     true,
			},
		},
	}
	for _, registration := range registrations {
		auth := &Auth{ID: registration.id, Provider: provider, Status: StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s): %v", registration.id, err)
		}
		modelRegistry.RegisterClient(registration.id, provider, []*registry.ModelInfo{registration.info})
		manager.RefreshSchedulerEntry(registration.id)
	}
	t.Cleanup(func() {
		for _, registration := range registrations {
			modelRegistry.UnregisterClient(registration.id)
		}
	})

	for attempt := 0; attempt < 4; attempt++ {
		selected, _, _, errPick := manager.pickNextMixed(context.Background(), []string{provider}, modelID, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickNextMixed() attempt %d: %v", attempt, errPick)
		}
		if selected == nil || selected.ID != "shared-endpoint-chat-auth" {
			t.Fatalf("pickNextMixed() attempt %d selected %#v, want chat auth", attempt, selected)
		}
	}
}

func TestManagerSelectsVideoOnlyAuthForVideoExecution(t *testing.T) {
	const (
		provider = "shared-video-selection"
		modelID  = "shared-video-selection-model"
	)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(schedulerProviderTestExecutor{provider: provider})
	modelRegistry := registry.GetGlobalRegistry()

	registrations := []struct {
		id   string
		info *registry.ModelInfo
	}{
		{
			id:   "shared-video-chat-auth",
			info: &registry.ModelInfo{ID: modelID, Type: provider},
		},
		{
			id: "shared-video-endpoint-auth",
			info: &registry.ModelInfo{
				ID:               modelID,
				Type:             provider,
				SupportsVideoAPI: true,
				ChatDisabled:     true,
			},
		},
	}
	for _, registration := range registrations {
		auth := &Auth{ID: registration.id, Provider: provider, Status: StatusActive}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%s): %v", registration.id, err)
		}
		modelRegistry.RegisterClient(registration.id, provider, []*registry.ModelInfo{registration.info})
		manager.RefreshSchedulerEntry(registration.id)
	}
	t.Cleanup(func() {
		for _, registration := range registrations {
			modelRegistry.UnregisterClient(registration.id)
		}
	})

	opts := cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai-video"),
		Metadata: map[string]any{
			cliproxyexecutor.VideoExecutionMetadataKey: true,
		},
	}
	selected, _, _, errPick := manager.pickNextMixed(context.Background(), []string{provider}, modelID, opts, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed(video): %v", errPick)
	}
	if selected == nil || selected.ID != "shared-video-endpoint-auth" {
		t.Fatalf("pickNextMixed(video) selected %#v, want video auth", selected)
	}

	selected, _, _, errPick = manager.pickNextMixed(context.Background(), []string{provider}, modelID, cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNextMixed(chat): %v", errPick)
	}
	if selected == nil || selected.ID != "shared-video-chat-auth" {
		t.Fatalf("pickNextMixed(chat) selected %#v, want chat auth", selected)
	}
}
