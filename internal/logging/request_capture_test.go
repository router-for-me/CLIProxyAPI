package logging

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCaptureSpool(t *testing.T) *CaptureSpool {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	s := NewDurableCaptureSpool(t.TempDir(), key)
	if err := s.Lease(true); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

type failOnceCaptureJournal struct {
	captureJournal
	fail bool
}

func (j *failOnceCaptureJournal) Update(fn func(*bolt.Tx) error) error {
	if j.fail {
		j.fail = false
		return errors.New("injected commit failure")
	}
	return j.captureJournal.Update(fn)
}
func TestCaptureFailedFinalizationRetriesWithoutRestart(t *testing.T) {
	for _, disable := range []bool{false, true} {
		t.Run(fmt.Sprintf("disabled=%t", disable), func(t *testing.T) {
			s := testCaptureSpool(t)
			r := captureBase("retry-finalization")
			stream := s.StartCapture(r)
			stream.Write("response", []byte("retained through recovery"))
			s.db = &failOnceCaptureJournal{captureJournal: s.db, fail: true}
			if disable {
				if err := s.Lease(false); err != nil {
					t.Fatal(err)
				}
			} else {
				stream.Finish(r)
			}
			if s.error == "" || stream.finalRecord == nil {
				t.Fatal("write failure was not retained")
			}
			events := captureEventsAfter(t, s, 0)
			last := events[len(events)-1]
			if len(events) != 3 || last.State != "partial" || last.Reason != "capture-storage-write-failed" || last.ResponseBytes != uint64(len("retained through recovery")) {
				t.Fatal("failed finalization remained pending")
			}
			if string(events[1].Chunk.Data) != "retained through recovery" {
				t.Fatal("retry lost buffered bytes")
			}
			if len(s.streams) != 0 || s.Page(0).Error != "" {
				t.Fatal("recovered finalization retained an active stream or warning")
			}
		})
	}
}
func TestCaptureSmallWritesUseBoundedGroupCommits(t *testing.T) {
	s := testCaptureSpool(t)
	var before, after int
	_ = s.db.View(func(tx *bolt.Tx) error { before = tx.ID(); return nil })
	r := captureBase("small-writes")
	stream := s.StartCapture(r)
	for i := 0; i < 1000; i++ {
		stream.Write("response", []byte("12345678"))
	}
	stream.Finish(r)
	_ = s.db.View(func(tx *bolt.Tx) error { after = tx.ID(); return nil })
	// Start, one coalesced body batch, and terminal metadata each synchronize once.
	if after-before != 3 {
		t.Fatalf("1000 small writes required %d commits", after-before)
	}
	events := captureEventsAfter(t, s, 0)
	if len(events) != 3 || events[2].ResponseBytes != 8000 {
		t.Fatal("coalescing lost body bytes")
	}
}
func TestCaptureInterruptedCopiedMessageRemainsPartial(t *testing.T) {
	s := testCaptureSpool(t)
	r := captureBase("copied-message")
	r.Format = "websocket"
	stream := s.StartCapture(r)
	observer := stream.MessageWriter("response", 2)
	_, _ = io.WriteString(observer, "partial bytes")
	observer.(interface{ Abort() }).Abort()
	stream.Finish(r)
	events := captureEventsAfter(t, s, 0)
	last := events[len(events)-1]
	if last.State != "partial" || last.Reason != "websocket-copy-interrupted" || last.ResponseBytes != uint64(len("partial bytes")) {
		t.Fatal("interrupted message called complete")
	}
	for _, e := range events {
		if e.Chunk != nil && e.Chunk.End {
			t.Fatal("interrupted message has a successful boundary")
		}
	}
}
func captureEventsAfter(t *testing.T, s *CaptureSpool, after uint64) []CaptureEvent {
	t.Helper()
	var all []CaptureEvent
	for {
		p := s.Page(after)
		if p.Error != "" {
			t.Fatal(p.Error)
		}
		if len(p.Records) == 0 {
			return all
		}
		all = append(all, p.Records...)
		after = p.Records[len(p.Records)-1].Sequence
	}
}
func captureBase(id string) CaptureRecord {
	return CaptureRecord{ID: id, TraceID: "trace-" + id, StartedAt: time.Now().UTC().Format(time.RFC3339Nano), State: "complete", Format: "http"}
}
func TestCaptureJournalCompleteBodiesAndMessages(t *testing.T) {
	s := testCaptureSpool(t)
	r := captureBase("large-body")
	stream := s.StartCapture(r)
	payload := bytes.Repeat([]byte{0, 0xff, 'x', '\n'}, (CaptureBodyLimit+CaptureChunkBytes)/4)
	stream.Write("request", payload)
	for i := 0; i < 300; i++ {
		stream.Message("response", 2, []byte{byte(i), 0xff, 0})
	}
	stream.Finish(r)
	events := captureEventsAfter(t, s, 0)
	var request, response []byte
	messages := 0
	for _, e := range events {
		if e.Chunk == nil {
			continue
		}
		if len(e.Chunk.Data) > CaptureChunkBytes {
			t.Fatal("unbounded transport chunk")
		}
		if e.Chunk.Direction == "request" {
			request = append(request, e.Chunk.Data...)
		} else {
			response = append(response, e.Chunk.Data...)
			if e.Chunk.End {
				messages++
			}
		}
	}
	if !bytes.Equal(request, payload) || messages != 300 || len(response) != 900 {
		t.Fatal("body or message bytes lost")
	}
	last := events[len(events)-1]
	expected := sha256.Sum256(payload)
	if last.State != "complete" || last.RequestBytes != uint64(len(payload)) || last.RequestSHA256 != hex.EncodeToString(expected[:]) {
		t.Fatal("incorrect completeness marker")
	}
	stored, err := os.ReadFile(filepath.Join(s.dir, "captures.db"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, payload[:CaptureChunkBytes]) {
		t.Fatal("plaintext journal payload")
	}
}
func TestCaptureJournalReplayLeaseRestartAndAcknowledgement(t *testing.T) {
	s := testCaptureSpool(t)
	r := captureBase("unfinished")
	stream := s.StartCapture(r)
	stream.Write("request", []byte("retained before restart"))
	epoch := s.Page(0).Epoch
	if err := s.Lease(true); err != nil {
		t.Fatal(err)
	}
	stream.Write("response", []byte("retained after lease expiry"))
	if s.StartCapture(captureBase("after-expiry")) == nil {
		t.Fatal("consumer stall stopped capture")
	}
	stream.Flush()
	if len(captureEventsAfter(t, s, 0)) != 4 {
		t.Fatal("lease expiry lost backlog")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := s.Lease(true); err != nil {
		t.Fatal(err)
	}
	events := captureEventsAfter(t, s, 0)
	last := events[len(events)-1]
	if last.State != "partial" || last.Reason != "proxy-restarted-before-finalization" || s.Page(0).Epoch != epoch {
		t.Fatal("restart did not preserve journal and finalize interruption")
	}
	if s.Acknowledge("wrong-epoch", last.Sequence) == nil || s.Acknowledge(epoch, last.Sequence+1) == nil {
		t.Fatal("invalid acknowledgement accepted")
	}
	if len(captureEventsAfter(t, s, 0)) != len(events) {
		t.Fatal("invalid acknowledgement removed data")
	}
	if err := s.Acknowledge(epoch, last.Sequence); err != nil {
		t.Fatal(err)
	}
	if len(s.Page(0).Records) != 0 {
		t.Fatal("acknowledged events retained")
	}
	next := s.StartCapture(captureBase("next"))
	next.Finish(captureBase("next"))
	if s.Page(0).Records[0].Sequence <= last.Sequence {
		t.Fatal("sequence reused after acknowledgement")
	}
}
func TestCaptureJournalNoConcurrencyOrFinalizationDrops(t *testing.T) {
	s := testCaptureSpool(t)
	const connections = 40
	streams := make([]*CaptureStream, connections)
	for i := range streams {
		streams[i] = s.StartCapture(captureBase(fmt.Sprint(i)))
		if streams[i] == nil {
			t.Fatal("concurrent capture refused")
		}
	}
	for i, stream := range streams {
		stream.Write("request", []byte{byte(i)})
		stream.Finish(captureBase(fmt.Sprint(i)))
	}
	if len(captureEventsAfter(t, s, 0)) != connections*3 {
		t.Fatal("unacknowledged events dropped")
	}
}
func TestCaptureJournalDisableRetainsExistingBytesAndWrongKeyFails(t *testing.T) {
	s := testCaptureSpool(t)
	r := captureBase("disabled")
	stream := s.StartCapture(r)
	stream.Write("request", []byte("already captured"))
	if err := s.Lease(false); err != nil {
		t.Fatal(err)
	}
	stream.Write("request", []byte("must not be captured"))
	stream.Finish(r)
	events := captureEventsAfter(t, s, 0)
	if len(events) != 3 || events[2].Reason != "capture-disabled" || events[2].RequestBytes != uint64(len("already captured")) {
		t.Fatal("disable lost existing data or retained later data")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	otherKey := make([]byte, 32)
	_, _ = rand.Read(otherKey)
	other := NewDurableCaptureSpool(s.dir, otherKey)
	if other.Lease(true) == nil {
		_ = other.Close()
		t.Fatal("wrong encryption key accepted")
	}
	if err := s.Lease(true); err != nil {
		t.Fatal(err)
	}
	if len(captureEventsAfter(t, s, 0)) != 3 {
		t.Fatal("wrong key destroyed backlog")
	}
}
