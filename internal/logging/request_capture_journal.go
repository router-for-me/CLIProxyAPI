package logging

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"

	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

var captureEvents = []byte("events")
var captureActive = []byte("active")
var captureMeta = []byte("meta")

func (s *CaptureSpool) open() error {
	if s.db != nil {
		return nil
	}
	if s.dir == "" || len(s.key) != 32 {
		return errors.New("encrypted capture storage is not configured")
	}
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return errors.New("invalid capture encryption key")
	}
	s.aead, err = cipher.NewGCM(block)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(s.dir, 0700); err != nil {
		return errors.New("capture storage unavailable")
	}
	s.db, err = bolt.Open(filepath.Join(s.dir, "captures.db"), 0600, nil)
	if err != nil {
		return errors.New("capture storage unavailable")
	}
	err = s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{captureEvents, captureActive, captureMeta} {
			if _, errBucket := tx.CreateBucketIfNotExists(name); errBucket != nil {
				return errBucket
			}
		}
		meta := tx.Bucket(captureMeta)
		if meta.Get([]byte("epoch")) == nil {
			if errEpoch := meta.Put([]byte("epoch"), []byte(uuid.NewString())); errEpoch != nil {
				return errEpoch
			}
		}
		s.epoch = string(meta.Get([]byte("epoch")))
		// A changed profile key must never replace queued data.
		if check := meta.Get([]byte("key-check")); check != nil {
			if _, errKey := s.decrypt(check, []byte("key-check")); errKey != nil {
				return errKey
			}
		} else {
			sealed, errSeal := s.encrypt([]byte(s.epoch), []byte("key-check"))
			if errSeal != nil {
				return errSeal
			}
			if errPut := meta.Put([]byte("key-check"), sealed); errPut != nil {
				return errPut
			}
		}
		return nil
	})
	if err == nil {
		err = s.recoverActive("proxy-restarted-before-finalization")
	}
	if err != nil {
		_ = s.db.Close()
		s.db = nil
		return errors.New("capture journal could not be recovered")
	}
	return nil
}
func (s *CaptureSpool) encrypt(data, aad []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(nonce, nonce, data, aad), nil
}
func (s *CaptureSpool) decrypt(data, aad []byte) ([]byte, error) {
	n := s.aead.NonceSize()
	if len(data) < n {
		return nil, errors.New("invalid capture ciphertext")
	}
	return s.aead.Open(nil, data[:n], data[n:], aad)
}
func captureSequence(n uint64) []byte { return binary.BigEndian.AppendUint64(nil, n) }
func (s *CaptureSpool) publish(tx *bolt.Tx, e CaptureEvent) error {
	b := tx.Bucket(captureEvents)
	seq, err := b.NextSequence()
	if err != nil {
		return err
	}
	e.Sequence, e.Epoch = seq, s.epoch
	data, err := encodeCaptureEvent(e)
	if err != nil {
		return err
	}
	sealed, err := s.encrypt(data, captureSequence(seq))
	if err != nil {
		return err
	}
	return b.Put(captureSequence(seq), sealed)
}
func (s *CaptureSpool) recoverActive(reason string) error {
	return s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket(captureActive)
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			data, err := s.decrypt(value, key)
			if err != nil {
				return err
			}
			var e CaptureEvent
			if err = decodeCaptureEvent(data, &e); err != nil {
				return err
			}
			e.Event, e.State, e.Reason = "end", "partial", reason
			if err = s.publish(tx, e); err != nil {
				return err
			}
			if err = cursor.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
}
func (s *CaptureSpool) appendBatch(events []CaptureEvent, generation uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.appendBatchLocked(events, generation)
}
func (s *CaptureSpool) appendBatchLocked(events []CaptureEvent, generation uint64) error {
	if s.db == nil {
		return errors.New("capture storage unavailable")
	}
	if generation != s.generation {
		return nil
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		for _, event := range events {
			if errPublish := s.publish(tx, event); errPublish != nil {
				return errPublish
			}
		}
		e := events[len(events)-1]
		active := tx.Bucket(captureActive)
		if e.Event == "end" {
			return active.Delete([]byte(e.ID))
		}
		e.Chunk = nil
		data, errMarshal := encodeCaptureEvent(e)
		if errMarshal != nil {
			return errMarshal
		}
		sealed, errSeal := s.encrypt(data, []byte(e.ID))
		if errSeal != nil {
			return errSeal
		}
		return active.Put([]byte(e.ID), sealed)
	})
	if err != nil {
		s.error = "Capture storage write failed; a transaction may be incomplete"
	}
	if err == nil && events[len(events)-1].Event == "end" {
		delete(s.streams, events[len(events)-1].ID)
	}
	return err
}
func (s *CaptureSpool) retryFinalizations() {
	s.mu.Lock()
	streams := make([]*CaptureStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.mu.Unlock()
	healthy, retried := true, false
	for _, stream := range streams {
		ok, attempted := stream.retryFinalization()
		healthy = healthy && ok
		retried = retried || attempted
	}
	if healthy && retried {
		s.mu.Lock()
		s.error = ""
		s.mu.Unlock()
	}
}
func (s *CaptureSpool) Page(after uint64) CapturePage {
	s.retryFinalizations()
	s.mu.Lock()
	defer s.mu.Unlock()
	p := CapturePage{Version: 2, Epoch: s.epoch, Error: s.error, Records: []CaptureEvent{}}
	if s.db == nil {
		return p
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		cursor := tx.Bucket(captureEvents).Cursor()
		for key, value := cursor.Seek(captureSequence(after)); key != nil; key, value = cursor.Next() {
			if binary.BigEndian.Uint64(key) <= after {
				continue
			}
			data, errDecrypt := s.decrypt(value, key)
			if errDecrypt != nil {
				return errDecrypt
			}
			var e CaptureEvent
			if errDecode := decodeCaptureEvent(data, &e); errDecode != nil {
				return errDecode
			}
			p.Records = append(p.Records, e)
			if len(p.Records) == CapturePageRecords {
				break
			}
		}
		return nil
	})
	if err != nil {
		p.Error = "Capture journal read failed"
		p.Records = []CaptureEvent{}
	}
	return p
}
func (s *CaptureSpool) Acknowledge(epoch string, sequence uint64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || epoch != s.epoch {
		return errors.New("capture acknowledgement epoch mismatch")
	}
	return s.db.Update(func(tx *bolt.Tx) error {
		b := tx.Bucket(captureEvents)
		if sequence > b.Sequence() {
			return errors.New("invalid capture acknowledgement")
		}
		cursor := b.Cursor()
		for key, _ := cursor.First(); key != nil && binary.BigEndian.Uint64(key) <= sequence; key, _ = cursor.Next() {
			if err := cursor.Delete(); err != nil {
				return err
			}
		}
		return nil
	})
}

// Clear is explicit user deletion, distinct from stopping capture or acknowledging committed bytes.
func (s *CaptureSpool) Clear() error {
	s.policyMu.Lock()
	defer s.policyMu.Unlock()
	s.mu.Lock()
	s.enabled = false
	s.generation++
	if s.db == nil {
		if s.dir == "" {
			s.mu.Unlock()
			return nil
		}
		if err := s.open(); err != nil {
			s.mu.Unlock()
			return err
		}
	}
	epoch := uuid.NewString()
	if err := s.db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{captureEvents, captureActive} {
			if err := tx.DeleteBucket(name); err != nil {
				return err
			}
			if _, err := tx.CreateBucket(name); err != nil {
				return err
			}
		}
		return tx.Bucket(captureMeta).Put([]byte("epoch"), []byte(epoch))
	}); err != nil {
		s.mu.Unlock()
		return errors.New("capture journal could not be cleared")
	}
	streams := make([]*CaptureStream, 0, len(s.streams))
	for _, stream := range s.streams {
		streams = append(streams, stream)
	}
	s.epoch, s.error = epoch, ""
	s.streams = make(map[string]*CaptureStream)
	s.mu.Unlock()
	for _, stream := range streams {
		stream.abandon()
	}
	return nil
}
