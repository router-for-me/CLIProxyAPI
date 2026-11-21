package watcher

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
)

type watchConfigUploadResult struct {
	data []byte
	err  error
}

type verifiedWatchConfigPersister struct {
	path    string
	results chan watchConfigUploadResult
}

func (p *verifiedWatchConfigPersister) PersistConfig(_ context.Context) error {
	data, err := config.PrepareConfigFileForUploadWithOptional(p.path, false)
	p.results <- watchConfigUploadResult{data: data, err: err}
	return err
}

func (*verifiedWatchConfigPersister) PersistAuthFiles(context.Context, string, ...string) error {
	return nil
}

func TestWatcherPersistenceStillRejectsPlaintextAfterUnrelatedConfigSave(t *testing.T) {
	const (
		id     = "e8bd1fce-3d81-4e40-9589-17b8709159eb"
		secret = "local-watch-upload-key"
	)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("port: 8317\nserver: {server-id: " + id + "}\nremote-management: {secret-key: " + secret + "}\n---\ncopy: " + secret + "\n")
	if errCreate := config.CreateConfigFile(path, raw); errCreate != nil {
		t.Fatal(errCreate)
	}
	if prepared, errPrepare := config.PrepareConfigFileForUploadWithOptional(path, false); errPrepare == nil || prepared != nil {
		t.Fatal("startup upload preparation did not reject the plaintext copy")
	}
	cfg, errLoad := config.LoadConfig(path)
	if errLoad != nil || cfg.RemoteManagement.SecretKey == secret {
		t.Fatalf("local startup did not use an in-memory hash: %v", errLoad)
	}
	cfg.Debug = true
	if errSave := config.SaveConfigPreserveComments(path, cfg); errSave == nil {
		t.Fatal("unrelated save replaced the only identifiable plaintext setting")
	}
	before, err := config.ReadConfigFile(path)
	if err != nil || !bytes.Equal(before, raw) {
		t.Fatalf("unsafe save changed the source snapshot: %v", err)
	}
	// An unrelated edit that preserves the identifying key remains safe to
	// store locally. Run the watcher reload path and wait for its upload attempt.
	if errSave := config.SaveConfigPreserveCommentsUpdateNestedScalar(path, []string{"host"}, "localhost"); errSave != nil {
		t.Fatal(errSave)
	}
	w, errNew := NewWatcher(path, t.TempDir(), nil)
	if errNew != nil {
		t.Fatal(errNew)
	}
	t.Cleanup(func() {
		if errStop := w.Stop(); errStop != nil {
			t.Error(errStop)
		}
	})
	w.SetConfig(cfg)
	w.lastConfigHash = cfg.SourceHash
	persister := &verifiedWatchConfigPersister{path: path, results: make(chan watchConfigUploadResult, 1)}
	w.storePersister = persister
	w.ReloadConfigIfChanged()
	select {
	case result := <-persister.results:
		if result.err == nil || result.data != nil {
			t.Fatal("Watcher persistence approved a snapshot containing a plaintext copy")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Watcher did not try to persist the unrelated edit")
	}
}

func TestWatcherStartReloadsServerIDAndTracksCommittedSource(t *testing.T) {
	const (
		initialID = "e8bd1fce-3d81-4e40-9589-17b8709159eb"
		nextID    = "8237fe4a-1419-4a20-8b39-77dbc44168cc"
		suffix    = "---\nunused: 'keep exactly'\n"
	)
	path := filepath.Join(t.TempDir(), "config.yaml")
	authDir := t.TempDir()
	prefix := "auth-dir: " + authDir + "\n"
	if errCreate := config.CreateConfigFile(path, []byte(prefix+"server: {server-id: "+initialID+"}\n"+suffix)); errCreate != nil {
		t.Fatal(errCreate)
	}
	initial, errLoad := config.LoadConfig(path)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	published := make(chan *config.Config, 8)
	w, errNew := NewWatcher(path, authDir, func(cfg *config.Config) {
		published <- cfg.CloneForRuntime()
	})
	if errNew != nil {
		t.Fatal(errNew)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		if errStop := w.Stop(); errStop != nil {
			t.Error(errStop)
		}
	})
	w.SetConfig(initial)
	if errStart := w.Start(ctx); errStart != nil {
		t.Fatal(errStart)
	}
	write := func(data string) {
		t.Helper()
		snapshot, errRead := config.ReadConfigFileSnapshot(path)
		if errRead != nil {
			t.Fatal(errRead)
		}
		if _, errWrite := config.WriteConfigFileCAS(path, snapshot, []byte(data)); errWrite != nil {
			t.Fatal(errWrite)
		}
	}
	awaitID := func(previous string) *config.Config {
		t.Helper()
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case cfg := <-published:
				if cfg.ServerID != previous {
					return cfg
				}
			case <-timer.C:
				t.Fatal("Watcher.Start did not publish changed server identity")
			}
		}
	}
	write(prefix + "server: {server-id: " + nextID + "}\n" + suffix)
	updated := awaitID(initialID)
	if updated.ServerID != nextID || !slices.Contains(diff.BuildConfigChangeDetails(initial, updated), "server.server-id: "+initialID+" -> "+nextID) {
		t.Fatalf("updated ID=%q; diff=%v", updated.ServerID, diff.BuildConfigChangeDetails(initial, updated))
	}
	write(prefix + suffix)
	regenerated := awaitID(nextID)
	parsed, errParse := uuid.Parse(regenerated.ServerID)
	if errParse != nil || parsed.Version() != 4 || regenerated.ServerID == initialID {
		t.Fatalf("unexpected generated ID: %q", regenerated.ServerID)
	}
	onDisk, errRead := config.ReadConfigFileSnapshot(path)
	if errRead != nil {
		t.Fatal(errRead)
	}
	saved, errSaved := config.ParseConfigBytes(onDisk.Bytes())
	if errSaved != nil || saved.ServerID != regenerated.ServerID || regenerated.SourceHash != onDisk.SourceHash() || !bytes.HasSuffix(onDisk.Bytes(), []byte(suffix)) {
		t.Fatalf("reload mismatch: cfg=%q hash=%q, disk=%q, error=%v", regenerated.ServerID, regenerated.SourceHash, onDisk.Bytes(), errSaved)
	}
}
