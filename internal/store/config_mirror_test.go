package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const mirrorTestServer = "server:\n  server-id: aaaaaaaa-1111-4444-8888-bbbbbbbbbbbb\n"
const mirrorTestInitial = "port: 8317\n" + mirrorTestServer

func TestRemoteConfigSharedMirrorSeed(t *testing.T) {
	const input = "port: 8317\n---\r\n# Keep the exact trailing document.\r\npreserve: true\r\n"
	const trailingEdit = "# User edit in the trailing document.\n"
	for _, tc := range []struct {
		name     string
		existing bool
		edit     bool
	}{
		{name: "peer creates mirror"},
		{name: "peer updates mirror", existing: true},
		{name: "peer creates mirror then user edits it", edit: true},
		{name: "peer updates mirror then user edits it", existing: true, edit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			path, example := filepath.Join(dir, "config.yaml"), filepath.Join(dir, "template.yaml")
			writeSeedTestFile(t, example, input)
			if tc.existing {
				writeSeedTestFile(t, path, input)
			}
			var first, second remoteConfigMirror
			var remote *remoteConfigVersion
			read := func(context.Context) (*remoteConfigVersion, error) { return remote, nil }
			write := func(_ context.Context, expected *remoteConfigVersion, data []byte) (*remoteConfigVersion, error) {
				if remote != expected {
					return nil, nil
				}
				remote = &remoteConfigVersion{data: data}
				return remote, nil
			}
			var winner *config.Config
			attempts := 0
			loaded, errSync := first.sync(ctx, path, example, false, read, func(ctx context.Context, expected *remoteConfigVersion, data []byte) (*remoteConfigVersion, error) {
				attempts++
				if attempts != 1 || expected != nil {
					t.Fatal("expected one seed attempt against the missing remote")
				}
				var errPeer error
				winner, errPeer = second.sync(ctx, path, example, false, read, write)
				if errPeer != nil {
					t.Fatal(errPeer)
				}
				if tc.edit {
					snapshot, errRead := config.ReadConfigFileSnapshot(path)
					if errRead != nil {
						t.Fatal(errRead)
					}
					if _, errWrite := config.WriteConfigFileCAS(path, snapshot, append(snapshot.Bytes(), trailingEdit...)); errWrite != nil {
						t.Fatal(errWrite)
					}
				}
				return write(ctx, expected, data)
			})
			if attempts != 1 || winner == nil {
				t.Fatalf("did not exercise competing seed: %d attempts, %v", attempts, errSync)
			}
			wantLocal := string(remote.data)
			if tc.edit {
				if !errors.Is(errSync, config.ErrConfigFileChanged) || first.baseline != nil {
					t.Fatalf("sync with a concurrent edit: %v", errSync)
				}
				wantLocal += trailingEdit
			} else if errSync != nil || loaded.ServerID != winner.ServerID || loaded.SourceHash != winner.SourceHash || first.baseline != remote {
				t.Fatalf("shared mirror did not converge: %v", errSync)
			}
			if got := string(readSeedTestFile(t, path)); got != wantLocal {
				t.Fatalf("mirror = %q, want %q", got, wantLocal)
			}
		})
	}
}

func TestRemoteConfigPreparationRejectsUnsafeDocumentsBeforeWriting(t *testing.T) {
	for _, tc := range []struct{ name, raw, reason string }{
		{"embedded plaintext", seedTestPlaintextConfig("management") + "---\nAuthorization: Bearer " + seedTestPlaintextSecret + "\n", "present elsewhere"},
		{"shadowed duplicate", mirrorTestInitial + "---\n<<: {nested: {x: one, x: two}}\nnested: ok\n", "document 2: mapping key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			writeSeedTestFile(t, path, mirrorTestInitial)
			mirror := &remoteConfigMirror{}
			write := func(context.Context, *remoteConfigVersion, []byte) (*remoteConfigVersion, error) {
				t.Fatal("unsafe config reached remote publish")
				return nil, nil
			}
			read := func(context.Context) (*remoteConfigVersion, error) {
				return &remoteConfigVersion{data: []byte(tc.raw)}, nil
			}
			if _, err := mirror.sync(context.Background(), path, "", false, read, write); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("sync did not reject unsafe remote bytes: %v", err)
			}
			if got := readSeedTestFile(t, path); string(got) != mirrorTestInitial || mirror.baseline != nil {
				t.Fatal("rejected remote bytes changed the mirror or baseline")
			}
			writeSeedTestFile(t, path, tc.raw)
			if err := mirror.persist(context.Background(), path, false, write); err == nil || !strings.Contains(err.Error(), tc.reason) {
				t.Fatalf("persist did not reject unsafe local bytes: %v", err)
			}
			if got := readSeedTestFile(t, path); string(got) != tc.raw || mirror.baseline != nil {
				t.Fatal("rejected save changed the mirror or baseline")
			}
		})
	}
}

type configTestPersister interface {
	ConfigPath() string
	PersistConfig(context.Context) error
}

func assertSequentialConfigConflict(t *testing.T, a, b configTestPersister) string {
	t.Helper()
	ctx := context.Background()
	const firstEdit = "port: 8881\n" + mirrorTestServer
	const winner = "port: 8882\n" + mirrorTestServer
	const staleEdit = "port: 8883\n" + mirrorTestServer
	for _, edit := range []string{firstEdit, winner} {
		writeSeedTestFile(t, a.ConfigPath(), edit)
		if errPersist := a.PersistConfig(ctx); errPersist != nil {
			t.Fatalf("save with current baseline: %v", errPersist)
		}
	}
	writeSeedTestFile(t, b.ConfigPath(), staleEdit)
	for range 2 {
		if errPersist := b.PersistConfig(ctx); !errors.Is(errPersist, errRemoteConfigChanged) {
			t.Fatalf("save with stale baseline: %v", errPersist)
		}
	}
	if got := string(readSeedTestFile(t, b.ConfigPath())); got != staleEdit {
		t.Fatalf("stale mirror was overwritten: %q", got)
	}
	return winner
}
