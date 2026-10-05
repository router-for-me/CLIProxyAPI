package cliproxy

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestServiceAccountingLifecycle(t *testing.T) {
	s := &Service{cfg: &config.Config{AccountingOutbox: config.AccountingOutboxConfig{Enabled: true, DataPath: t.TempDir()}}}
	if err := s.startAccountingOutbox(); err != nil {
		t.Fatal(err)
	}
	defer s.stopAccountingOutbox(context.Background())
	original := s.accountingOutbox
	if err := s.startAccountingOutbox(); err != nil || s.accountingOutbox != original {
		t.Fatalf("duplicate start: %v", err)
	}
	usage.DefaultManager().Publish(context.Background(), usage.Record{RequestID: "lifecycle-event"})
	s.stopAccountingOutbox(context.Background())
	recovered, err := usage.OpenOutbox(s.cfg.AccountingOutbox)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := recovered.Close(context.Background()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := recovered.Event("lifecycle-event"); err != nil {
		t.Fatal(err)
	}
}

func TestServiceAccountingCorruptionDoesNotStopInference(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounting.db"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	s := &Service{cfg: &config.Config{AccountingOutbox: config.AccountingOutboxConfig{Enabled: true, DataPath: dir}}}
	if err := s.startAccountingOutbox(); err != nil {
		t.Fatal(err)
	}
	defer s.stopAccountingOutbox(context.Background())
	usage.DefaultManager().Publish(context.Background(), usage.Record{RequestID: "lost"})
	stats, err := s.AccountingOutboxStats()
	if err != nil || stats.PersistenceFailures != 1 || stats.DroppedEvents != 1 {
		t.Fatalf("%+v %v", stats, err)
	}
}
