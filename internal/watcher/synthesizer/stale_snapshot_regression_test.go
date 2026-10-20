package synthesizer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestFileSnapshotCannotOverwriteLaterPersistedPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "synthetic.json")
	write := func(prefix string) []byte {
		data := []byte(`{"type":"codex","prefix":"` + prefix + `"}`)
		if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		return data
	}
	snapshot := func(data []byte) *coreauth.Auth {
		items, errSynthesize := SynthesizeAuthFile(&SynthesisContext{
			Config:      &config.Config{},
			AuthDir:     dir,
			Now:         time.Now(),
			IDGenerator: NewStableIDGenerator(),
		}, path, data)
		if errSynthesize != nil || len(items) != 1 {
			t.Fatalf("snapshot: %v count=%d", errSynthesize, len(items))
		}
		return items[0]
	}
	ctx := coreauth.WithSkipPersist(context.Background())
	manager := coreauth.NewManager(nil, nil, nil)
	initial := snapshot(write("pfx-8"))
	if _, errRegister := manager.Register(ctx, initial); errRegister != nil {
		t.Fatal(errRegister)
	}
	stale := snapshot(write("pfx-8"))
	latest, _ := manager.GetByID(initial.ID)
	latest.Prefix = "pfx-9"
	latest.Metadata["prefix"] = "pfx-9"
	write("pfx-9")
	if _, errUpdate := manager.Update(ctx, latest); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	before, _ := manager.GetByID(initial.ID)
	if _, errUpdate := manager.Update(ctx, stale); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	after, _ := manager.GetByID(initial.ID)
	if after.Prefix != "pfx-9" || after.Generation != before.Generation {
		t.Fatalf("stale snapshot overwrote prefix: got=%s gen=%d want=pfx-9 gen=%d", after.Prefix, after.Generation, before.Generation)
	}
	manual := snapshot(write("manual-edit"))
	if _, errUpdate := manager.Update(ctx, manual); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	after, _ = manager.GetByID(initial.ID)
	if after.Prefix != "manual-edit" {
		t.Fatal("legitimate file edit was lost")
	}
	// A deleted or unreadable source must not be replayed into live state.
	deleted := snapshot(write("deleted-edit"))
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	before = after.Clone()
	if _, errUpdate := manager.Update(ctx, deleted); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	after, _ = manager.GetByID(initial.ID)
	if after.Prefix != before.Prefix || after.Generation != before.Generation {
		t.Fatal("deleted file snapshot changed runtime state")
	}
	if errMkdir := os.Mkdir(path, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	if _, errUpdate := manager.Update(ctx, deleted); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	after, _ = manager.GetByID(initial.ID)
	if after.Prefix != before.Prefix || after.Generation != before.Generation {
		t.Fatal("unreadable file snapshot changed runtime state")
	}
}

func TestPluginFileSnapshotCannotOverwriteLaterPersistedPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plugin.json")
	write := func(prefix string) []byte {
		data := []byte(`{"type":"gemini-cli","prefix":"` + prefix + `"}`)
		if errWrite := os.WriteFile(path, data, 0o600); errWrite != nil {
			t.Fatal(errWrite)
		}
		return data
	}
	parser := multiAuthParserFunc(func(_ context.Context, _ pluginapi.AuthParseRequest) ([]*coreauth.Auth, bool, error) {
		return []*coreauth.Auth{
			{
				ID:       "plugin-project-a.json",
				Provider: "gemini-cli",
				Metadata: map[string]any{"type": "gemini-cli", "project_id": "project-a"},
			},
			{
				ID:       "plugin-project-b.json",
				Provider: "gemini-cli",
				Metadata: map[string]any{"type": "gemini-cli", "project_id": "project-b"},
			},
		}, true, nil
	})
	snapshot := func(data []byte) []*coreauth.Auth {
		items, errSynthesize := SynthesizeAuthFile(&SynthesisContext{
			Config:           &config.Config{},
			AuthDir:          dir,
			Now:              time.Now(),
			IDGenerator:      NewStableIDGenerator(),
			PluginAuthParser: parser,
		}, path, data)
		if errSynthesize != nil || len(items) != 2 {
			t.Fatalf("snapshot: %v count=%d", errSynthesize, len(items))
		}
		return items
	}
	ctx := coreauth.WithSkipPersist(context.Background())
	manager := coreauth.NewManager(nil, nil, nil)
	initial := snapshot(write("pfx-8"))
	for _, item := range initial {
		if _, errRegister := manager.Register(ctx, item); errRegister != nil {
			t.Fatal(errRegister)
		}
	}
	stale := snapshot(write("pfx-8"))
	latest, _ := manager.GetByID(initial[0].ID)
	latest.Prefix = "pfx-9"
	latest.Metadata["prefix"] = "pfx-9"
	write("pfx-9")
	if _, errUpdate := manager.Update(ctx, latest); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	before, _ := manager.GetByID(initial[0].ID)
	if _, errUpdate := manager.Update(ctx, stale[0]); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	after, _ := manager.GetByID(initial[0].ID)
	if after.Prefix != "pfx-9" || after.Generation != before.Generation {
		t.Fatalf("stale plugin snapshot overwrote prefix: got=%s gen=%d want=pfx-9 gen=%d", after.Prefix, after.Generation, before.Generation)
	}
	manual := snapshot(write("manual-edit"))
	if _, errUpdate := manager.Update(ctx, manual[0]); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	after, _ = manager.GetByID(initial[0].ID)
	if after.Prefix != "manual-edit" {
		t.Fatalf("legitimate plugin file edit was lost: got=%s", after.Prefix)
	}
}
