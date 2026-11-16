package auth

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"time"

	"github.com/MoeForge/moego/json"
	log "github.com/sirupsen/logrus"
)

const (
	quotaHistoryFormatVersion = 1
	quotaHistoryWarnEvery     = time.Hour
)

// quotaHistoryLine is one point as stored in the history file.
type quotaHistoryLine struct {
	V  int     `json:"v"`           // format version
	ID string  `json:"id"`          // Auth.ID
	K  string  `json:"k"`           // window kind
	S  string  `json:"s,omitempty"` // scope
	T  int64   `json:"t"`           // unix seconds
	U  float64 `json:"u"`           // used 0..1, 4 dp
	R  int64   `json:"r,omitempty"` // reset unix seconds, 0/absent = unknown
	P  int64   `json:"p,omitempty"` // period seconds, 0/absent = unknown
}

func newQuotaHistoryLine(authID string, series *QuotaHistorySeries, point QuotaHistoryPoint) quotaHistoryLine {
	line := quotaHistoryLine{
		V:  quotaHistoryFormatVersion,
		ID: authID,
		K:  series.Kind,
		S:  series.Scope,
		T:  point.At.Unix(),
		U:  point.Used,
		P:  int64(series.Period / time.Second),
	}
	if !point.ResetAt.IsZero() {
		line.R = point.ResetAt.Unix()
	}
	return line
}

func encodeQuotaHistoryLines(lines []quotaHistoryLine) ([]byte, error) {
	var buf bytes.Buffer
	for _, line := range lines {
		data, errMarshal := json.Marshal(line)
		if errMarshal != nil {
			return nil, fmt.Errorf("encode quota history line: %w", errMarshal)
		}
		buf.Write(data)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// OpenQuotaHistory loads path, drops expired and unreadable lines, compacts the
// file, and returns a store that appends to path. A missing file is empty
// history and is created by the first append. On an I/O error it returns a
// memory-only store holding whatever was loaded, together with the error.
func OpenQuotaHistory(path string, now time.Time) (*QuotaHistory, error) {
	h := NewQuotaHistory()
	existed, errLoad := h.load(path, now)
	if errLoad != nil {
		return h, errLoad
	}
	h.file = &quotaHistoryFile{path: path}
	if !existed {
		return h, nil
	}
	// Rewriting also removes a torn tail, so later appends start on a fresh line.
	if errCompact := h.Compact(now); errCompact != nil {
		h.file = nil
		return h, errCompact
	}
	return h, nil
}

// load reads the history file into h, which must not be shared yet. It
// reports whether the file existed.
func (h *QuotaHistory) load(path string, now time.Time) (bool, error) {
	file, errOpen := os.Open(path)
	if errOpen != nil {
		if errors.Is(errOpen, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("open quota history: %w", errOpen)
	}
	defer func() {
		if errClose := file.Close(); errClose != nil {
			log.WithError(errClose).Warn("quota history: failed to close file after loading")
		}
	}()
	bad, torn := 0, 0
	var errRead error
	reader := bufio.NewReader(file)
	for {
		raw, errLine := reader.ReadBytes('\n')
		if errLine != nil {
			if !errors.Is(errLine, io.EOF) {
				errRead = fmt.Errorf("read quota history: %w", errLine)
			} else if len(raw) > 0 {
				torn++
			}
			break
		}
		if !h.loadLine(raw, now) {
			bad++
		}
	}
	for _, series := range h.series {
		sort.SliceStable(series.Points, func(i, j int) bool { return series.Points[i].At.Before(series.Points[j].At) })
		series.Points = slices.CompactFunc(series.Points, func(a, b QuotaHistoryPoint) bool { return a.At.Equal(b.At) })
	}
	if bad > 0 || torn > 0 {
		log.WithFields(log.Fields{"skipped": bad, "torn": torn}).Warn("quota history: ignored unreadable lines")
	}
	return true, errRead
}

// loadLine adds one stored point and reports whether the line was readable.
// Expired points are dropped without counting as unreadable.
func (h *QuotaHistory) loadLine(raw []byte, now time.Time) bool {
	var line quotaHistoryLine
	if errUnmarshal := json.Unmarshal(bytes.TrimSpace(raw), &line); errUnmarshal != nil {
		return false
	}
	if line.V != quotaHistoryFormatVersion || line.ID == "" || line.K == "" {
		return false
	}
	at := time.Unix(line.T, 0).UTC()
	if at.After(now.Add(quotaHistoryFutureSkew)) {
		return false
	}
	if at.Before(now.Add(-QuotaHistoryRetention)) {
		return true
	}
	key := quotaHistoryKey{authID: line.ID, kind: line.K, scope: line.S}
	series := h.series[key]
	if series == nil {
		series = &QuotaHistorySeries{Kind: line.K, Scope: line.S}
		h.series[key] = series
	}
	if line.P > 0 {
		series.Period = time.Duration(line.P) * time.Second
	}
	point := QuotaHistoryPoint{At: at, Used: roundQuotaUsed(line.U)}
	if line.R > 0 {
		point.ResetAt = time.Unix(line.R, 0).UTC()
	}
	series.Points = append(series.Points, point)
	return true
}

// quotaHistoryFile is the file side of a QuotaHistory, guarded by its ioMu.
type quotaHistoryFile struct {
	path     string
	out      *os.File // append handle, opened on first use
	dirty    bool     // a write failed; appends stop until a rewrite succeeds
	warnedAt time.Time
}

// append writes lines in one write. After a failure the file is dirty and
// appends are dropped (the points stay in memory) until rewrite succeeds.
func (f *quotaHistoryFile) append(lines []quotaHistoryLine) error {
	if f.dirty {
		return nil
	}
	errAppend := f.writeLines(lines)
	if errAppend == nil {
		return nil
	}
	f.dirty = true
	f.discardOut()
	f.warn("quota history: append failed; the file is rewritten on the next tick", len(lines), errAppend)
	return errAppend
}

func (f *quotaHistoryFile) writeLines(lines []quotaHistoryLine) error {
	data, errEncode := encodeQuotaHistoryLines(lines)
	if errEncode != nil {
		return errEncode
	}
	if f.out == nil {
		if errMkdir := os.MkdirAll(filepath.Dir(f.path), 0o700); errMkdir != nil {
			return fmt.Errorf("create quota history directory: %w", errMkdir)
		}
		out, errOpen := os.OpenFile(f.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if errOpen != nil {
			return fmt.Errorf("open quota history for append: %w", errOpen)
		}
		f.out = out
	}
	if _, errWrite := f.out.Write(data); errWrite != nil {
		return fmt.Errorf("append quota history: %w", errWrite)
	}
	return nil
}

// rewrite atomically replaces the file with lines and clears dirty on success.
func (f *quotaHistoryFile) rewrite(lines []quotaHistoryLine) error {
	if errReplace := f.replace(lines); errReplace != nil {
		f.dirty = true
		f.warn("quota history: rewrite failed; retrying on the next tick", len(lines), errReplace)
		return errReplace
	}
	f.dirty = false
	return nil
}

func (f *quotaHistoryFile) replace(lines []quotaHistoryLine) error {
	data, errEncode := encodeQuotaHistoryLines(lines)
	if errEncode != nil {
		return errEncode
	}
	dir := filepath.Dir(f.path)
	if errMkdir := os.MkdirAll(dir, 0o700); errMkdir != nil {
		return fmt.Errorf("create quota history directory: %w", errMkdir)
	}
	tmp, errCreate := os.CreateTemp(dir, filepath.Base(f.path)+".*.tmp")
	if errCreate != nil {
		return fmt.Errorf("create quota history temp file: %w", errCreate)
	}
	tmpPath := tmp.Name()
	if errWrite := writeSyncedQuotaHistory(tmp, data); errWrite != nil {
		_ = os.Remove(tmpPath)
		return errWrite
	}
	// The append handle must not outlive the file it points at.
	f.discardOut()
	if errRename := os.Rename(tmpPath, f.path); errRename != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("replace quota history: %w", errRename)
	}
	return nil
}

func writeSyncedQuotaHistory(file *os.File, data []byte) (err error) {
	defer func() {
		errClose := file.Close()
		if errClose == nil {
			return
		}
		if err == nil {
			err = fmt.Errorf("close quota history temp file: %w", errClose)
			return
		}
		log.WithError(errClose).Debug("quota history: failed to close temp file")
	}()
	if _, errWrite := file.Write(data); errWrite != nil {
		return fmt.Errorf("write quota history temp file: %w", errWrite)
	}
	if errSync := file.Sync(); errSync != nil {
		return fmt.Errorf("sync quota history temp file: %w", errSync)
	}
	return nil
}

func (f *quotaHistoryFile) close() error {
	if f.out == nil {
		return nil
	}
	errClose := f.out.Close()
	f.out = nil
	if errClose != nil {
		return fmt.Errorf("close quota history: %w", errClose)
	}
	return nil
}

// discardOut closes the append handle where a close error cannot change the outcome.
func (f *quotaHistoryFile) discardOut() {
	if errClose := f.close(); errClose != nil {
		log.WithError(errClose).Debug("quota history: failed to close append handle")
	}
}

// warn logs a write failure at most once per quotaHistoryWarnEvery.
func (f *quotaHistoryFile) warn(message string, points int, err error) {
	now := time.Now()
	if !f.warnedAt.IsZero() && now.Sub(f.warnedAt) < quotaHistoryWarnEvery {
		return
	}
	f.warnedAt = now
	log.WithError(err).WithFields(log.Fields{"path": f.path, "points": points}).Warn(message)
}
