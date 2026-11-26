package store

import (
	"bytes"
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	textunicode "golang.org/x/text/encoding/unicode"
)

func integrationPostgresConfigStore(db *sql.DB, dir string) *PostgresStore {
	return &PostgresStore{
		db: db, authDir: filepath.Join(dir, "auths"), configPath: filepath.Join(dir, "config.yaml"),
		cfg: PostgresStoreConfig{ConfigTable: defaultConfigTable, AuthTable: defaultAuthTable, CooldownTable: defaultCooldownTable},
	}
}

func TestPostgresConcurrentConfigBootstrapAndCAS(t *testing.T) {
	db := startIntegrationPostgres(t)
	root := t.TempDir()
	ctx := context.Background()
	stores := make([]*PostgresStore, 2)
	for i, name := range []string{"first", "second"} {
		stores[i] = integrationPostgresConfigStore(db, filepath.Join(root, name))
	}
	examplePath := filepath.Join(root, "template.yaml")
	const tail = "---\r\n# Do not normalize this document.\r\npreserve: true\r\n"
	legacy := seedTestPlaintextConfig("management") + tail
	writeSeedTestFile(t, examplePath, legacy)
	var wg sync.WaitGroup
	start := make(chan struct{})
	loaded := make([]*config.Config, len(stores))
	for i, store := range stores {
		wg.Go(func() {
			<-start
			var errBootstrap error
			loaded[i], errBootstrap = store.BootstrapConfig(ctx, examplePath)
			if errBootstrap != nil {
				t.Errorf("BootstrapConfig %d: %v", i, errBootstrap)
			}
		})
	}
	close(start)
	wg.Wait()
	if t.Failed() {
		return
	}
	a, b := stores[0], stores[1]
	version, errRead := a.readConfigVersion(ctx)
	if errRead != nil || version == nil {
		t.Fatalf("read config version: %v", errRead)
	}
	assertSeededWithHashedSecret(t, version.data, a.ConfigPath(), "management")
	assertSeededWithHashedSecret(t, version.data, b.ConfigPath(), "management")
	if !strings.HasSuffix(string(version.data), tail) || loaded[0].ServerID == "" || loaded[0].ServerID != loaded[1].ServerID || loaded[1].SourceHash != config.ComputeSourceHash(version.data) || loaded[0].SourceHash != loaded[1].SourceHash {
		t.Fatalf("bootstrap did not converge: %q/%q, %q/%q", loaded[0].ServerID, loaded[0].SourceHash, loaded[1].ServerID, loaded[1].SourceHash)
	}

	// Installing a legacy body simulates updating a pre-server-id deployment.
	if written, errWrite := a.writeConfigVersion(ctx, version, []byte(legacy)); errWrite != nil || written == nil {
		t.Fatalf("install legacy config: %v", errWrite)
	}
	legacyVersion, errLegacy := a.readConfigVersion(ctx)
	if errLegacy != nil || legacyVersion == nil || string(legacyVersion.data) != legacy {
		t.Fatalf("read legacy snapshot: %v", errLegacy)
	}
	upgraded, errSync := b.syncConfigFromDatabase(ctx, "")
	if errSync != nil || upgraded.ServerID == "" {
		t.Fatalf("upgrade existing legacy config: %v", errSync)
	}
	if written, errStale := a.writeConfigVersion(ctx, legacyVersion, version.data); errStale != nil || written != nil {
		t.Fatalf("stale CAS from legacy body: %v, %v", written != nil, errStale)
	}
	if written, errInsert := a.writeConfigVersion(ctx, nil, version.data); errInsert != nil || written != nil {
		t.Fatalf("stale first insert: %v, %v", written != nil, errInsert)
	}
	converged, errSync := a.syncConfigFromDatabase(ctx, "")
	if errSync != nil || converged.SourceHash != upgraded.SourceHash || converged.ServerID != upgraded.ServerID {
		t.Fatalf("sync winner: %v", errSync)
	}
}

func TestPostgresPersistConfigRejectsStaleMirror(t *testing.T) {
	ctx := context.Background()
	db := startIntegrationPostgres(t)
	a := integrationPostgresConfigStore(db, t.TempDir())
	b := integrationPostgresConfigStore(db, t.TempDir())
	writeSeedTestFile(t, a.ConfigPath(), mirrorTestInitial)
	for _, replica := range []*PostgresStore{a, b} {
		if _, errBootstrap := replica.BootstrapConfig(ctx, ""); errBootstrap != nil {
			t.Fatal(errBootstrap)
		}
	}
	winner := assertSequentialConfigConflict(t, a, b)
	version, errRead := a.readConfigVersion(ctx)
	if errRead != nil || version == nil || string(version.data) != winner {
		t.Fatalf("stale save changed the remote winner: %v", errRead)
	}
}

func TestPostgresConfigRejectsUTF16BeforePublish(t *testing.T) {
	ctx := context.Background()
	store := integrationPostgresConfigStore(startIntegrationPostgres(t), t.TempDir())
	if errEnsure := store.EnsureSchema(ctx); errEnsure != nil {
		t.Fatal(errEnsure)
	}
	const input = "# UTF-8: café—中文\n" + mirrorTestInitial + "---\r\npreserve: true\r\n"
	encoded, errEncode := textunicode.UTF16(textunicode.LittleEndian, textunicode.UseBOM).NewEncoder().Bytes([]byte(input))
	if errEncode != nil {
		t.Fatal(errEncode)
	}
	if prepared, errPrepare := config.PrepareConfigBytesForUploadWithOptional(encoded, false); errPrepare != nil || !bytes.Equal(prepared, encoded) || bytes.IndexByte(encoded, 0) < 0 {
		t.Fatalf("fixture must be valid UTF-16 YAML containing NUL bytes: %v", errPrepare)
	}
	const encodingError = "PostgreSQL TEXT requires UTF-8 config without NUL bytes"
	writeSeedTestFile(t, store.ConfigPath(), string(encoded))
	if _, errSeed := store.syncConfigFromDatabase(ctx, ""); errSeed == nil || !strings.Contains(errSeed.Error(), encodingError) {
		t.Fatalf("UTF-16 seed error = %v", errSeed)
	}
	if version, errRead := store.readConfigVersion(ctx); errRead != nil || version != nil {
		t.Fatalf("rejected seed created a remote config: %v", errRead)
	}

	writeSeedTestFile(t, store.ConfigPath(), input)
	if _, errSeed := store.syncConfigFromDatabase(ctx, ""); errSeed != nil {
		t.Fatal(errSeed)
	}
	writeSeedTestFile(t, store.ConfigPath(), string(encoded))
	if errPersist := store.PersistConfig(ctx); errPersist == nil || !strings.Contains(errPersist.Error(), encodingError) {
		t.Fatalf("UTF-16 save error = %v", errPersist)
	}
	if version, errRead := store.readConfigVersion(ctx); errRead != nil || version == nil || string(version.data) != input {
		t.Fatalf("rejected save changed the exact UTF-8 config: %v", errRead)
	}
	const corrected = "# Saved as UTF-8\n" + input
	writeSeedTestFile(t, store.ConfigPath(), corrected)
	if errPersist := store.PersistConfig(ctx); errPersist != nil {
		t.Fatalf("save after correcting encoding: %v", errPersist)
	}
	if version, errRead := store.readConfigVersion(ctx); errRead != nil || version == nil || string(version.data) != corrected {
		t.Fatalf("UTF-8 save did not preserve the exact bytes: %v", errRead)
	}
}
