package redisqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"unicode"

	log "github.com/sirupsen/logrus"
)

// The journal is independent of the legacy destructive queue. A single local
// accounting consumer acknowledges event IDs only after committing its inbox.
// Unacknowledged events do not expire and survive core restarts.
type usageJournal struct {
	mu      sync.Mutex
	dir     string
	lastErr error
}

var journal usageJournal

func ConfigureUsageJournal(dir string) {
	journal.mu.Lock()
	defer journal.mu.Unlock()
	journal.dir = dir
	journal.lastErr = nil
}
func validEventID(id string) bool {
	return id != ""
}

// Public IDs are opaque; they must never be used as filesystem paths. The dot
// in this prefix separates new names from the legacy filename alphabet.
func journalEventFilename(id string) string {
	sum := sha256.Sum256([]byte(id))
	return "sha256." + hex.EncodeToString(sum[:]) + ".json"
}

func legacyJournalEventID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if !unicode.IsLetter(c) && !unicode.IsDigit(c) && c != '-' && c != '_' {
			return false
		}
	}
	// On Windows this also rejects reserved device names such as CON.json.
	// Those names could never have been ordinary legacy journal files.
	return filepath.IsLocal(id + ".json")
}

// Check payload identity as well as the legacy filename. Case-insensitive or
// Unicode-normalizing filesystems can resolve a different public ID to the
// same legacy path; that event must not be deduplicated or acknowledged.
func (j *usageJournal) matchingLegacyEventPath(id string) (string, error) {
	if !legacyJournalEventID(id) {
		return "", nil
	}
	path := filepath.Join(j.dir, id+".json")
	payload, errRead := os.ReadFile(path)
	if os.IsNotExist(errRead) {
		return "", nil
	}
	if errRead != nil {
		return "", errRead
	}
	var event struct {
		EventID string `json:"event_id"`
	}
	if json.Unmarshal(payload, &event) != nil || event.EventID != id {
		return "", nil
	}
	return path, nil
}

func (j *usageJournal) append(payload []byte) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.dir == "" {
		return nil
	}
	var event struct {
		EventID string `json:"event_id"`
	}
	if err := json.Unmarshal(payload, &event); err != nil {
		return err
	}
	// The legacy wire still carries the key for old consumers. Durable storage
	// only needs a fingerprint for attribution, never the credential itself.
	var fields map[string]json.RawMessage
	if errDecode := json.Unmarshal(payload, &fields); errDecode != nil {
		return errDecode
	}
	var key string
	if errKey := json.Unmarshal(fields["api_key"], &key); errKey == nil && key != "" {
		sum := sha256.Sum256([]byte(key))
		fingerprint := hex.EncodeToString(sum[:])
		fields["api_key_hash"], _ = json.Marshal(fingerprint)
		fields["api_key_display"], _ = json.Marshal("sha256:" + fingerprint[:12])
	}
	var source, authType string
	_ = json.Unmarshal(fields["source"], &source)
	_ = json.Unmarshal(fields["auth_type"], &authType)
	if source != "" && (source == key || authType == "api_key" || authType == "apikey") {
		sum := sha256.Sum256([]byte(source))
		fields["source"], _ = json.Marshal("sha256:" + hex.EncodeToString(sum[:]))
	}
	delete(fields, "api_key")
	delete(fields, "response_headers")
	var errEncode error
	payload, errEncode = json.Marshal(fields)
	if errEncode != nil {
		return errEncode
	}
	if !validEventID(event.EventID) {
		return errors.New("usage event requires a valid event_id")
	}
	if err := os.MkdirAll(j.dir, 0700); err != nil {
		return err
	}
	target := filepath.Join(j.dir, journalEventFilename(event.EventID))
	if _, err := os.Stat(target); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	if legacy, errLegacy := j.matchingLegacyEventPath(event.EventID); errLegacy != nil {
		return errLegacy
	} else if legacy != "" {
		return nil
	}
	file, err := os.CreateTemp(j.dir, ".pending-")
	if err != nil {
		return err
	}
	name := file.Name()
	defer func() {
		if errRemove := os.Remove(name); errRemove != nil && !os.IsNotExist(errRemove) {
			log.WithError(errRemove).Warn("remove pending usage event")
		}
	}()
	_, errWrite := file.Write(payload)
	if errWrite == nil {
		errWrite = file.Sync()
	}
	errClose := file.Close()
	if errWrite != nil {
		return errWrite
	}
	if errClose != nil {
		return errClose
	}
	if errRename := os.Rename(name, target); errRename != nil {
		return errRename
	}
	return syncJournalDirectory(j.dir)
}
func (j *usageJournal) read(count int) ([][]byte, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.dir == "" {
		return nil, errors.New("durable usage journal is not configured")
	}
	if j.lastErr != nil {
		return nil, j.lastErr
	}
	entries, err := os.ReadDir(j.dir)
	if os.IsNotExist(err) {
		return [][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(entries, func(i, k int) bool { return entries[i].Name() < entries[k].Name() })
	result := make([][]byte, 0)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		payload, errRead := os.ReadFile(filepath.Join(j.dir, entry.Name()))
		if errRead != nil {
			return nil, errRead
		}
		result = append(result, payload)
		if len(result) >= count {
			break
		}
	}
	return result, nil
}
func (j *usageJournal) ack(ids []string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.dir == "" {
		return errors.New("durable usage journal is not configured")
	}
	for _, id := range ids {
		if !validEventID(id) {
			return errors.New("invalid usage event_id")
		}
	}
	for _, id := range ids {
		if err := os.Remove(filepath.Join(j.dir, journalEventFilename(id))); err != nil && !os.IsNotExist(err) {
			return err
		}
		legacy, errLegacy := j.matchingLegacyEventPath(id)
		if errLegacy != nil {
			return errLegacy
		}
		if legacy != "" {
			if errRemove := os.Remove(legacy); errRemove != nil && !os.IsNotExist(errRemove) {
				return errRemove
			}
		}
	}
	if len(ids) == 0 {
		return nil
	}
	return syncJournalDirectory(j.dir)
}
func syncJournalDirectory(dir string) error {
	// Windows does not support fsync on directory handles; file contents were
	// already flushed before the atomic rename.
	if runtime.GOOS == "windows" {
		return nil
	}
	file, errOpen := os.Open(dir)
	if errOpen != nil {
		return errOpen
	}
	errSync := file.Sync()
	errClose := file.Close()
	if errSync != nil {
		return errSync
	}
	return errClose
}
func ReadUsageJournal(count int) ([][]byte, error) { return journal.read(count) }
func AckUsageJournal(ids []string) error           { return journal.ack(ids) }
func journalUsage(payload []byte) {
	if err := journal.append(payload); err != nil {
		journal.mu.Lock()
		journal.lastErr = err
		journal.mu.Unlock()
		log.WithError(err).Error("durable usage journal write failed; accounting requires attention")
	}
}
