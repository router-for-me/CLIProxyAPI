package multiuser

import (
	"bufio"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	"golang.org/x/sync/singleflight"
	_ "modernc.org/sqlite"
)

var ErrJournalBuilding = errors.New("usage history index is being prepared; retry shortly")

const journalTimeFormat = "2006-01-02T15:04:05.000000000Z"

const journalSchema = `
CREATE TABLE IF NOT EXISTS sources (
 name TEXT PRIMARY KEY, offset INTEGER NOT NULL, modified INTEGER NOT NULL, signature TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS events (
 source TEXT NOT NULL, position INTEGER NOT NULL, length INTEGER NOT NULL, kind INTEGER NOT NULL,
 at TEXT NOT NULL, day TEXT NOT NULL, hour INTEGER NOT NULL,
 consumer TEXT NOT NULL, owner TEXT NOT NULL, channel_id TEXT NOT NULL, credential_id TEXT NOT NULL,
 key_id TEXT NOT NULL, channel TEXT NOT NULL, model TEXT NOT NULL, model_lower TEXT NOT NULL, serving_lower TEXT NOT NULL,
 actor TEXT NOT NULL, action TEXT NOT NULL, trace TEXT NOT NULL,
 failed INTEGER NOT NULL, known INTEGER NOT NULL, input INTEGER NOT NULL, output INTEGER NOT NULL,
 reasoning INTEGER NOT NULL, cache_read INTEGER NOT NULL, cache_create INTEGER NOT NULL, total INTEGER NOT NULL,
 latency INTEGER NOT NULL, ttft INTEGER NOT NULL,
 balanced INTEGER NOT NULL, superseded INTEGER NOT NULL,
 PRIMARY KEY (source, position, kind)
);
`

const journalIndexes = `
CREATE INDEX IF NOT EXISTS events_time ON events(kind, at DESC, source, position);
CREATE INDEX IF NOT EXISTS usage_consumer ON events(consumer, at DESC, source, position) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_owner ON events(owner, at DESC, source, position) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_key ON events(key_id, at DESC, source, position) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_channel ON events(channel_id, at DESC, source, position) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_credential ON events(credential_id, at DESC, source, position) WHERE kind=1;
CREATE INDEX IF NOT EXISTS audit_actor ON events(actor, at DESC, source, position) WHERE kind=3;
CREATE INDEX IF NOT EXISTS usage_trace ON events(trace) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_consumer_trace ON events(consumer,trace) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_owner_trace ON events(owner,trace) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_performance ON events(hour,model,failed,latency,ttft,output) WHERE kind=1;
CREATE INDEX IF NOT EXISTS usage_summary ON events(day,consumer,owner,channel,model,trace,failed,superseded,known,input,output,reasoning,cache_read,cache_create,total,at,channel_id,credential_id,key_id,model_lower,serving_lower) WHERE kind=1;
`

const insertEventSQL = `INSERT INTO events VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`

// supersedeAttemptsSQL hides earlier failed attempts of an even-load-balanced
// request once a later attempt of the same trace exists.
const supersedeAttemptsSQL = `UPDATE events SET superseded=1 WHERE kind=1 AND trace=? AND balanced=1 AND failed=1 AND superseded=0`

type journalIndex struct {
	store    *Store
	db       *sql.DB
	ctx      context.Context
	cancel   context.CancelFunc
	ready    chan struct{}
	initErr  error
	syncMu   sync.Mutex
	syncing  singleflight.Group
	version  uint64
	cacheMu  sync.Mutex
	cache    map[string]journalCacheEntry
	flights  map[string]*journalFlight
	weight   int
	verified map[string]os.FileInfo
}

type journalSource struct {
	name      string
	size      int64
	modified  int64
	offset    int64
	signature string
	info      os.FileInfo
}

// PrepareJournal starts shared index construction without delaying authentication or writes.
func (s *Store) PrepareJournal() {
	_, _ = s.startJournal()
}

func (s *Store) startJournal() (*journalIndex, error) {
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	if s.indexClosed {
		return nil, sql.ErrConnDone
	}
	if s.index == nil {
		ctx, cancel := context.WithCancel(context.Background())
		index := &journalIndex{store: s, ctx: ctx, cancel: cancel, ready: make(chan struct{}), cache: map[string]journalCacheEntry{}, flights: map[string]*journalFlight{}, verified: map[string]os.FileInfo{}}
		s.index = index
		go func() {
			defer close(index.ready)
			started := time.Now()
			log.Info("multi-user: preparing journal query index")
			defer func() {
				if index.initErr != nil {
					if !errors.Is(index.initErr, context.Canceled) {
						log.WithError(index.initErr).Error("multi-user: journal query index failed")
					}
					return
				}
				log.WithField("elapsed", time.Since(started)).Info("multi-user: journal query index ready")
			}()
			index.initErr = index.open()
			if index.initErr == nil {
				index.initErr = index.sync()
			}
			if index.initErr == nil {
				_, index.initErr = index.db.ExecContext(index.ctx, journalIndexes)
			}
		}()
	}
	return s.index, nil
}

// JournalReady never returns partial totals while the initial history import is running.
func (s *Store) JournalReady() error {
	index, err := s.startJournal()
	if err != nil {
		return err
	}
	select {
	case <-index.ready:
		return index.initErr
	default:
		return ErrJournalBuilding
	}
}

func (s *Store) queryJournal(ctx context.Context) (*journalIndex, error) {
	index, err := s.startJournal()
	if err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-index.ready:
	}
	if index.initErr != nil {
		return nil, index.initErr
	}
	result := index.syncing.DoChan("tail", func() (any, error) { return nil, index.sync() })
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case value := <-result:
		return index, value.Err
	}
}

func (s *Store) Close() error {
	s.indexMu.Lock()
	s.indexClosed = true
	index := s.index
	s.indexMu.Unlock()
	if index == nil {
		return nil
	}
	index.cancel()
	<-index.ready
	index.syncMu.Lock()
	defer index.syncMu.Unlock()
	if index.db != nil {
		return index.db.Close()
	}
	return nil
}

func (j *journalIndex) open() error {
	path, err := filepath.Abs(filepath.Join(j.store.dir, "journal-index.sqlite"))
	if err != nil {
		return err
	}
	uri := &url.URL{Scheme: "file", Path: "/" + strings.TrimPrefix(filepath.ToSlash(path), "/")}
	values := url.Values{}
	for _, pragma := range []string{"journal_mode(WAL)", "busy_timeout(5000)", "synchronous(NORMAL)", "cache_size(-8192)", "temp_store(FILE)", "journal_size_limit(16777216)"} {
		values.Add("_pragma", pragma)
	}
	uri.RawQuery = values.Encode()
	j.db, err = sql.Open("sqlite", uri.String())
	if err != nil {
		return err
	}
	j.db.SetMaxOpenConns(6)
	j.db.SetMaxIdleConns(6)
	if err = j.resetOutdatedSchema(); err != nil {
		return fmt.Errorf("open journal query index: %w", err)
	}
	if _, err = j.db.ExecContext(j.ctx, journalSchema); err != nil {
		return fmt.Errorf("open journal query index: %w", err)
	}
	return os.Chmod(path, 0o600)
}

// resetOutdatedSchema drops a derived index built by an older column layout so
// the journal is reimported with the current one.
func (j *journalIndex) resetOutdatedSchema() error {
	var count int
	if err := j.db.QueryRowContext(j.ctx, "SELECT COUNT(*) FROM pragma_table_info('events')").Scan(&count); err != nil {
		return err
	}
	var current int
	if err := j.db.QueryRowContext(j.ctx, "SELECT COUNT(*) FROM pragma_table_info('events') WHERE name='superseded'").Scan(&current); err != nil {
		return err
	}
	if count == 0 || current == 1 {
		return nil
	}
	log.Info("multi-user: rebuilding journal query index for a new schema")
	_, err := j.db.ExecContext(j.ctx, "DROP TABLE IF EXISTS events; DROP TABLE IF EXISTS sources;")
	return err
}

// snapshot holds the append lock only long enough to establish complete-record boundaries.
func (j *journalIndex) snapshot() ([]journalSource, error) {
	j.store.logMu.Lock()
	defer j.store.logMu.Unlock()
	entries, err := os.ReadDir(filepath.Join(j.store.dir, "journal"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]journalSource, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		if _, errDate := time.Parse("2006-01-02", strings.TrimSuffix(entry.Name(), ".jsonl")); errDate != nil {
			continue
		}
		info, errInfo := entry.Info()
		if errInfo != nil {
			return nil, errInfo
		}
		out = append(out, journalSource{name: entry.Name(), size: info.Size(), modified: info.ModTime().UnixNano(), info: info})
	}
	return out, nil
}

func (j *journalIndex) sync() error {
	j.syncMu.Lock()
	defer j.syncMu.Unlock()
	if err := j.ctx.Err(); err != nil {
		return err
	}
	files, err := j.snapshot()
	if err != nil {
		return err
	}
	rows, err := j.db.QueryContext(j.ctx, "SELECT name, offset, modified, signature FROM sources")
	if err != nil {
		return err
	}
	stored := map[string]journalSource{}
	for rows.Next() {
		var source journalSource
		if err = rows.Scan(&source.name, &source.offset, &source.modified, &source.signature); err != nil {
			_ = rows.Close()
			return err
		}
		stored[source.name] = source
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return err
	}
	changed := false
	defer func() {
		if changed {
			j.cacheMu.Lock()
			j.version++
			j.cache = map[string]journalCacheEntry{}
			j.weight = 0
			j.cacheMu.Unlock()
		}
	}()
	for _, file := range files {
		previous, exists := stored[file.name]
		delete(stored, file.name)
		verified := j.verified[file.name]
		if exists && verified != nil && os.SameFile(file.info, verified) && file.size == previous.offset && file.modified == previous.modified {
			continue
		}
		changed = true
		if verified != nil && !os.SameFile(file.info, verified) {
			exists = false
		}
		if err = j.importFile(file, previous, exists); err != nil {
			return err
		}
		j.verified[file.name] = file.info
	}
	for name := range stored {
		delete(j.verified, name)
		changed = true
		tx, errBegin := j.db.BeginTx(j.ctx, nil)
		if errBegin != nil {
			return errBegin
		}
		if _, err = tx.ExecContext(j.ctx, "DELETE FROM events WHERE source=?", name); err == nil {
			_, err = tx.ExecContext(j.ctx, "DELETE FROM sources WHERE name=?", name)
		}
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// boundarySignature detects replacement and truncate/regrow without rereading history.
func boundarySignature(file *os.File, offset int64) (string, error) {
	hash := sha256.New()
	for _, start := range []int64{0, max(0, offset-4096)} {
		length := min(int64(4096), offset-start)
		if length == 0 {
			continue
		}
		data := make([]byte, length)
		if _, err := file.ReadAt(data, start); err != nil {
			return "", err
		}
		_, _ = hash.Write(data)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (j *journalIndex) importFile(source, previous journalSource, exists bool) error {
	file, err := os.Open(filepath.Join(j.store.dir, "journal", source.name))
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	offset := previous.offset
	reset := !exists || source.size < offset || (source.size == offset && source.modified != previous.modified)
	if !reset {
		signature, errSignature := boundarySignature(file, offset)
		if errSignature != nil {
			return errSignature
		}
		reset = signature != previous.signature
	}
	if reset {
		offset = 0
	}
	reader := bufio.NewReaderSize(io.NewSectionReader(file, offset, source.size-offset), 128*1024)
	for {
		tx, errBegin := j.db.BeginTx(j.ctx, nil)
		if errBegin != nil {
			return errBegin
		}
		err = func() error {
			defer func() { _ = tx.Rollback() }()
			if reset {
				if _, errDelete := tx.ExecContext(j.ctx, "DELETE FROM events WHERE source=?", source.name); errDelete != nil {
					return errDelete
				}
				reset = false
			}
			statement, errPrepare := tx.PrepareContext(j.ctx, insertEventSQL)
			if errPrepare != nil {
				return errPrepare
			}
			defer func() { _ = statement.Close() }()
			supersede, errPrepare := tx.PrepareContext(j.ctx, supersedeAttemptsSQL)
			if errPrepare != nil {
				return errPrepare
			}
			defer func() { _ = supersede.Close() }()
			for n := 0; n < 2048 && offset < source.size; n++ {
				if errContext := j.ctx.Err(); errContext != nil {
					return errContext
				}
				line, errRead := reader.ReadBytes('\n')
				if errRead != nil {
					return fmt.Errorf("incomplete journal %s at byte %d: %w", source.name, offset, errRead)
				}
				if len(line) > 1024*1024 {
					return fmt.Errorf("oversized journal %s at byte %d", source.name, offset)
				}
				var entry journalEntry
				if errDecode := json.Unmarshal(line, &entry); errDecode != nil {
					return fmt.Errorf("invalid journal %s at byte %d: %w", source.name, offset, errDecode)
				}
				if errInsert := insertJournalEntry(j.ctx, statement, supersede, source.name, offset, len(line), entry); errInsert != nil {
					return errInsert
				}
				offset += int64(len(line))
			}
			signature, errSignature := boundarySignature(file, offset)
			if errSignature != nil {
				return errSignature
			}
			if _, errSave := tx.ExecContext(j.ctx, "INSERT OR REPLACE INTO sources VALUES(?,?,?,?)", source.name, offset, source.modified, signature); errSave != nil {
				return errSave
			}
			return tx.Commit()
		}()
		if err != nil {
			return err
		}
		if offset == source.size {
			return nil
		}
	}
}

func insertJournalEntry(ctx context.Context, statement, supersede *sql.Stmt, source string, offset int64, length int, entry journalEntry) error {
	insert := func(kind int, at time.Time, u UsageEvent, actor, action string) error {
		model := strings.ToLower(u.Model)
		serving := model
		if u.Channel != "" && !strings.HasPrefix(model, strings.ToLower(u.Channel)+"/") {
			serving = strings.ToLower(u.Channel) + "/" + model
		}
		trace := usageTrace(u)
		if kind == 1 && u.Balanced {
			if _, err := supersede.ExecContext(ctx, trace); err != nil {
				return err
			}
		}
		_, err := statement.ExecContext(ctx, source, offset, length, kind,
			at.UTC().Format(journalTimeFormat), at.UTC().Format("2006-01-02"), at.UTC().Truncate(time.Hour).Unix(),
			u.Consumer, u.Owner, u.ChannelID, u.CredentialID, u.KeyID, u.Channel, u.Model, model, serving,
			actor, action, trace, u.Failed, u.TokensKnown, u.InputTokens, u.OutputTokens,
			u.ReasoningTokens, u.CacheReadTokens, u.CacheCreationTokens, u.TotalTokens, u.LatencyMS, u.TTFTMS, u.Balanced, false)
		return err
	}
	if entry.Usage != nil {
		if err := insert(1, entry.Usage.At, *entry.Usage, "", ""); err != nil {
			return err
		}
	}
	if entry.Call != nil {
		c := entry.Call
		if err := insert(2, c.At, UsageEvent{Consumer: c.Consumer, KeyID: c.KeyID, Model: c.Model}, "", ""); err != nil {
			return err
		}
	}
	if entry.Audit != nil {
		a := entry.Audit
		return insert(3, a.At, UsageEvent{}, a.Actor, a.Action)
	}
	return nil
}

// usageTrace groups the attempts of one client request.
func usageTrace(u UsageEvent) string {
	if u.TraceID != "" {
		return u.TraceID
	}
	if u.RequestID != "" {
		return u.RequestID
	}
	return u.ID
}
