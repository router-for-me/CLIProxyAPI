package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// A claim whose directory sync failed sent no exchange, so it must not stay
// behind and lock the grant into an unknown outcome.
func TestFileRefreshClaimSyncFailureLeavesGrantClaimable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory fsync is unavailable on Windows")
	}
	dir := filepath.Join(t.TempDir(), "auths")
	if errMkdir := os.Mkdir(dir, 0o300); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })
	if handle, errOpen := os.Open(dir); errOpen == nil {
		_ = handle.Close()
		t.Skip("test requires directory read permissions to be enforced")
	}
	journal := &FileRefreshJournal{Dir: dir}
	key := strings.Repeat("d", 64)
	if claimed, errClaim := journal.RefreshClaim(context.Background(), key); claimed || errClaim == nil {
		t.Fatalf("first claim must fail on the unreadable ancestor: claimed=%v, error=%v", claimed, errClaim)
	}
	if errChmod := os.Chmod(dir, 0o700); errChmod != nil {
		t.Fatal(errChmod)
	}
	claimed, errClaim := journal.RefreshClaim(context.Background(), key)
	if !claimed || errClaim != nil {
		t.Fatalf("grant stayed locked after a claim that sent nothing: claimed=%v, error=%v", claimed, errClaim)
	}
}

type ctxCheckingJournal struct{ memoryRefreshJournal }

func (j *ctxCheckingJournal) RefreshComplete(ctx context.Context, key string, receipt []byte) error {
	if errContext := ctx.Err(); errContext != nil {
		return errContext
	}
	return j.memoryRefreshJournal.RefreshComplete(ctx, key, receipt)
}

type failingCompleteJournal struct{ memoryRefreshJournal }

func (j *failingCompleteJournal) RefreshComplete(context.Context, string, []byte) error {
	return errors.New("disk full")
}

func decodeTestReceipt(value ojson.Result) (string, error) { return value.String(), nil }

// An exchange that succeeds as the refresh deadline expires must still record
// its receipt and return the rotated value.
func TestJournalRefreshReceiptOutlivesRefreshDeadline(t *testing.T) {
	journal := &ctxCheckingJournal{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	value, errRefresh := journalRefresh(ctx, journal, ClientID, "grant-deadline", func() (string, error) {
		cancel()
		return "rotated", nil
	}, decodeTestReceipt)
	if errRefresh != nil || value != "rotated" {
		t.Fatalf("rotated value lost at the deadline: value=%q, error=%v", value, errRefresh)
	}
	receipt, _ := journal.RefreshRead(context.Background(), refreshGrantKey(ClientID, "grant-deadline"))
	if ojson.ParseBytes(receipt).Get("value").String() != "rotated" {
		t.Fatalf("receipt not written: %s", receipt)
	}
}

// A failed receipt write after a successful exchange must not discard the
// rotated tokens: the old grant is already spent upstream.
func TestJournalRefreshReceiptWriteFailureKeepsRotatedValue(t *testing.T) {
	journal := &failingCompleteJournal{}
	value, errRefresh := journalRefresh(context.Background(), journal, ClientID, "grant-write-fail", func() (string, error) {
		return "rotated", nil
	}, decodeTestReceipt)
	if errRefresh != nil || value != "rotated" {
		t.Fatalf("rotated value discarded on receipt write failure: value=%q, error=%v", value, errRefresh)
	}
	claimed, _ := journal.RefreshClaim(context.Background(), refreshGrantKey(ClientID, "grant-write-fail"))
	if claimed {
		t.Fatal("old grant must stay claimed so it is never replayed")
	}
}
