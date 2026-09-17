package runtimebridge

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/util/atomicfile"
)

// fakeRenderer implements snapshotRenderer for unit tests without
// depending on the configsnapshot package.
type fakeRenderer struct {
	yaml         []byte
	failValidate atomic.Bool
}

func (f *fakeRenderer) MarshalYAML() ([]byte, error) { return f.yaml, nil }
func (f *fakeRenderer) Validate() error {
	if f.failValidate.Load() {
		return errValidate
	}
	return nil
}
func (f *fakeRenderer) AtomicWrite(path string, data []byte, mode os.FileMode) error {
	return atomicfile.Write(path, data, mode)
}

var errValidate = errors.New("validation failed")

func TestBuildCreatesBridgeWithCorrectPermissions(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	b, err := Build(context.Background(), &fakeRenderer{yaml: []byte("port: 8317\n")}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer func() { _ = b.Close() }()

	info, err := os.Stat(b.Root())
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("root mode = %v; want 0700", info.Mode().Perm())
	}
	if !strings.HasPrefix(b.Root(), os.TempDir()+"/") && !strings.HasPrefix(b.Root(), os.Getenv("WRITABLE_PATH")) {
		t.Fatalf("root not under expected base: %s", b.Root())
	}
	if filepath.Dir(b.ConfigPath()) != b.Root() {
		t.Fatalf("config.yaml not under bridge root: %s", b.ConfigPath())
	}
	info, err = os.Stat(b.ConfigPath())
	if err != nil {
		t.Fatalf("stat config.yaml: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("config.yaml mode = %v; want 0600", info.Mode().Perm())
	}
	got, err := os.ReadFile(b.ConfigPath())
	if err != nil {
		t.Fatalf("read config.yaml: %v", err)
	}
	if string(got) != "port: 8317\n" {
		t.Fatalf("contents = %q", got)
	}

	authInfo, err := os.Stat(b.AuthDir())
	if err != nil {
		t.Fatalf("stat auths: %v", err)
	}
	if authInfo.Mode().Perm() != 0o700 {
		t.Fatalf("auths mode = %v; want 0700", authInfo.Mode().Perm())
	}
}

func TestBuildRejectsNilSnapshot(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	if _, err := Build(context.Background(), nil, ""); err == nil {
		t.Fatal("expected error for nil snapshot")
	}
}

func TestBuildRejectsInvalidRenderer(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	r := &fakeRenderer{yaml: []byte("port: 1\n")}
	r.failValidate.Store(true)
	if _, err := Build(context.Background(), r, ""); err == nil {
		t.Fatal("expected validation error to abort Build")
	}
}

func TestBuildRejectsCancelledContext(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Build(ctx, &fakeRenderer{yaml: []byte("x")}, ""); err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestCloseRemovesTreeAndIsIdempotent(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	b, err := Build(context.Background(), &fakeRenderer{yaml: []byte("port: 8317\n")}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	root := b.Root()
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("root missing: %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("expected root removed; got %v", err)
	}
	if err := b.Close(); err != nil {
		t.Fatalf("second Close returned error: %v", err)
	}
}

func TestCoordinatorReloadReplacesConfigAtomically(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	b, err := Build(context.Background(), &fakeRenderer{yaml: []byte("v1\n")}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer b.Close()

	var renderCalls atomic.Int32
	src := func(_ context.Context) (snapshotRenderer, error) {
		renderCalls.Add(1)
		return &fakeRenderer{yaml: []byte("v2\n")}, nil
	}
	coord := NewCoordinator(src)
	coord.Bind(b)
	if err := coord.ReloadLatest(context.Background()); err != nil {
		t.Fatalf("ReloadLatest: %v", err)
	}
	if renderCalls.Load() != 1 {
		t.Fatalf("renderCalls = %d; want 1", renderCalls.Load())
	}
	got, err := os.ReadFile(b.ConfigPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "v2\n" {
		t.Fatalf("contents = %q; want v2", got)
	}
}

func TestCoordinatorReloadWithoutBindingIsNoOp(t *testing.T) {
	coord := NewCoordinator(func(_ context.Context) (snapshotRenderer, error) {
		t.Fatal("src must not be called when bridge is not bound")
		return nil, nil
	})
	if err := coord.ReloadLatest(context.Background()); err != nil {
		t.Fatalf("ReloadLatest: %v", err)
	}
}

func TestCoordinatorReloadReturnsSourceError(t *testing.T) {
	coord := NewCoordinator(func(_ context.Context) (snapshotRenderer, error) {
		return nil, errValidate
	})
	b, err := Build(context.Background(), &fakeRenderer{yaml: []byte("v1\n")}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer b.Close()
	coord.Bind(b)
	if err := coord.ReloadLatest(context.Background()); err == nil {
		t.Fatal("expected source error")
	}
}

func TestCoordinatorReloadValidationErrorLeavesFileIntact(t *testing.T) {
	t.Setenv("WRITABLE_PATH", t.TempDir())
	b, err := Build(context.Background(), &fakeRenderer{yaml: []byte("initial\n")}, "")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer b.Close()
	r := &fakeRenderer{yaml: []byte("replacement\n")}
	r.failValidate.Store(true)
	src := func(_ context.Context) (snapshotRenderer, error) {
		return r, nil
	}
	coord := NewCoordinator(src)
	coord.Bind(b)
	if err := coord.ReloadLatest(context.Background()); err == nil {
		t.Fatal("expected validation error")
	}
	got, err := os.ReadFile(b.ConfigPath())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "initial\n" {
		t.Fatalf("contents = %q; want initial (unmodified)", got)
	}
}
