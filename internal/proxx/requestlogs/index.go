package requestlogs

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	log "github.com/sirupsen/logrus"
)

// IndexFileName is the persisted meta index, kept next to the config file (never in the log
// directory, where the upstream app log cleaner deletes files).
const IndexFileName = "proxx-request-logs-index.json"

// indexVersion is bumped whenever the record layout or the meta semantics change; a mismatch means
// an empty index (every file is parsed again).
const indexVersion = 1

// indexBatchSize is how many files the worker parses between merges, and the most files a refresh
// parses inline once the backlog is done. A variable only so tests can shrink it.
var indexBatchSize = 200

// indexSaveInterval is the minimum time between two debounced index saves.
const indexSaveInterval = 60 * time.Second

// ResolveIndexFile returns the index path next to the config file.
func ResolveIndexFile(configPath string) string {
	base := "."
	if configPath != "" {
		base = filepath.Dir(configPath)
	}
	return filepath.Join(base, IndexFileName)
}

// indexFile is the persisted index.
type indexFile struct {
	Version int           `json:"version"`
	LogDir  string        `json:"log_dir"`
	Files   []indexRecord `json:"files"`
}

// indexRecord is the parsed meta of one file, valid only while the file keeps its size and mtime.
type indexRecord struct {
	Name           string `json:"name"`
	Size           int64  `json:"size"`
	MtimeUnixNano  int64  `json:"mtime_unix_nano"`
	Time           string `json:"time,omitempty"` // RFC3339Nano, keeps the request's zone offset
	Method         string `json:"method,omitempty"`
	Path           string `json:"path,omitempty"`
	Status         int    `json:"status,omitempty"`
	Stream         bool   `json:"stream,omitempty"`
	RequestedModel string `json:"requested_model,omitempty"`
	ServedHeader   string `json:"served_header,omitempty"`
	RequestID      string `json:"request_id,omitempty"`
	AuthProvider   string `json:"auth_provider,omitempty"`
	Attempts       int    `json:"attempts,omitempty"`
	InputTokens    *int64 `json:"input_tokens,omitempty"`
	OutputTokens   *int64 `json:"output_tokens,omitempty"`
	Reasoning      *int64 `json:"reasoning_tokens,omitempty"`
	CachedTokens   *int64 `json:"cached_tokens,omitempty"`
}

func recordOf(e *entry) indexRecord {
	m := e.meta
	r := indexRecord{
		Name: e.name, Size: e.size, MtimeUnixNano: e.mtime.UnixNano(),
		Method: m.method, Path: m.path, Status: m.status, Stream: m.stream,
		RequestedModel: m.requestedModel, ServedHeader: m.servedHeader, RequestID: m.requestID,
		AuthProvider: m.authProvider, Attempts: m.attempts,
		InputTokens: m.tokens.in, OutputTokens: m.tokens.out, Reasoning: m.tokens.reasoning, CachedTokens: m.tokens.cached,
	}
	if !m.time.IsZero() {
		r.Time = m.time.Format(time.RFC3339Nano)
	}
	return r
}

// fileMeta rebuilds the meta. Strings are cloned so entries never pin the decoded file.
func (r indexRecord) fileMeta() fileMeta {
	return fileMeta{
		valid:          true,
		time:           parseTimestamp(r.Time),
		method:         strings.Clone(r.Method),
		path:           strings.Clone(r.Path),
		status:         r.Status,
		stream:         r.Stream,
		requestedModel: strings.Clone(r.RequestedModel),
		servedHeader:   strings.Clone(r.ServedHeader),
		requestID:      strings.Clone(r.RequestID),
		authProvider:   strings.Clone(r.AuthProvider),
		attempts:       r.Attempts,
		tokens:         tokenSet{in: r.InputTokens, out: r.OutputTokens, reasoning: r.Reasoning, cached: r.CachedTokens},
	}
}

// loadIndex reads the persisted index for dir. Any problem means an empty index; the cost is a re-parse.
func loadIndex(path, dir string) map[string]indexRecord {
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Warn("proxx: read request log index")
		}
		return nil
	}
	var f indexFile
	if err = json.Unmarshal(data, &f); err != nil {
		log.WithError(err).Warn("proxx: request log index is corrupt; re-indexing")
		return nil
	}
	if f.Version != indexVersion || f.LogDir != dir {
		log.Infof("proxx: request log index is for version %d / %q; re-indexing", f.Version, f.LogDir)
		return nil
	}
	out := make(map[string]indexRecord, len(f.Files))
	for _, r := range f.Files {
		out[r.Name] = r
	}
	log.Debugf("proxx: loaded %d request log index record(s)", len(out))
	return out
}

// saveIndex writes the index when it changed: when force is set, otherwise at most once per
// indexSaveInterval. The snapshot is taken under s.mu, so the header and the records always belong
// to the same directory. A failed save is logged (once at warn level) and retried later.
func (s *Store) saveIndex(force bool) {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	now := s.now()
	if s.indexPath == "" || s.dir == "" || !s.dirty || (!force && now.Sub(s.lastSave) < indexSaveInterval) {
		s.mu.Unlock()
		return
	}
	path := s.indexPath
	f := indexFile{Version: indexVersion, LogDir: s.dir, Files: make([]indexRecord, 0, len(s.entries))}
	for _, e := range s.entries {
		if e.listed() {
			f.Files = append(f.Files, recordOf(e))
		}
	}
	s.dirty = false
	s.lastSave = now
	gen := s.gen
	s.mu.Unlock()

	err := writeIndexFile(path, f)
	if err != nil {
		// Warn once; a read-only or full disk would otherwise warn on every retry.
		if s.saveFailed {
			log.WithError(err).Debug("proxx: save request log index")
		} else {
			log.WithError(err).Warn("proxx: save request log index; will retry")
		}
	}
	s.mu.Lock()
	s.saveFailed = err != nil
	if err != nil && s.gen == gen {
		s.dirty = true
	}
	s.mu.Unlock()
}

// writeIndexFile writes f to a temp file in the target directory and renames it into place, so a
// reader never sees a partial file.
func writeIndexFile(path string, f indexFile) error {
	data, err := json.Marshal(f)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".proxx-request-logs-index-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	_, errWrite := tmp.Write(data)
	errClose := tmp.Close()
	if errWrite == nil {
		errWrite = errClose
	}
	if errWrite == nil {
		errWrite = os.Rename(tmpName, path)
	}
	if errWrite != nil {
		if errRemove := os.Remove(tmpName); errRemove != nil && !os.IsNotExist(errRemove) {
			log.WithError(errRemove).Debug("proxx: remove request log index temp file")
		}
	}
	return errWrite
}

// setIndexFile sets where the index is persisted ("" disables persistence). It is loaded by the next
// refresh, once per directory.
func (s *Store) setIndexFile(path string) {
	s.mu.Lock()
	s.indexPath = path
	s.mu.Unlock()
}

// metaJob is one file to parse.
type metaJob struct {
	name  string
	id    string
	stamp string
	size  int64
	mtime time.Time
}

// metaResult is a parsed file.
type metaResult struct {
	metaJob
	meta fileMeta
}

// parseJobs parses files without any lock. It stops early when stop reports true.
func parseJobs(dir string, jobs []metaJob, stop func() bool) []metaResult {
	results := make([]metaResult, 0, len(jobs))
	for _, j := range jobs {
		if stop != nil && stop() {
			break
		}
		meta, _ := readMetaFn(filepath.Join(dir, j.name), j.size)
		if logging.ShortRequestID(meta.requestID) != j.id {
			meta.requestID = ""
		}
		results = append(results, metaResult{metaJob: j, meta: meta})
	}
	return results
}

// mergeMetaLocked stores parse results, skipping files that changed, disappeared or belong to an
// older generation since the parse started. A file that failed to parse keeps an invalid meta, so it
// is not pending forever; it is parsed again when its size or mtime changes.
func (s *Store) mergeMetaLocked(gen uint64, results []metaResult) {
	if s.gen != gen {
		return
	}
	for _, r := range results {
		e := s.entries[r.name]
		if e == nil || e.hasMeta || e.size != r.size || !e.mtime.Equal(r.mtime) {
			continue
		}
		e.meta, e.hasMeta = r.meta, true
		if r.meta.valid {
			s.dirty = true
		}
	}
}

// startIndexing marks the first viewer use.
func (s *Store) startIndexing() {
	s.mu.Lock()
	s.ensureIndexingLocked()
	s.mu.Unlock()
}

// ensureIndexingLocked turns meta indexing on and starts the worker while the backlog is not done.
func (s *Store) ensureIndexingLocked() {
	s.indexing = true
	if s.backlogDone || s.workerDone != nil || s.dir == "" || !s.scanned {
		return
	}
	done := make(chan struct{})
	s.workerDone = done
	go s.indexWorker(s.gen, s.dir, done)
}

// pendingJobsLocked returns the files whose meta still needs parsing.
func (s *Store) pendingJobsLocked() []metaJob {
	var jobs []metaJob
	for _, e := range s.entries {
		if e.pending() {
			jobs = append(jobs, metaJob{name: e.name, id: e.id, stamp: e.stamp, size: e.size, mtime: e.mtime})
		}
	}
	return jobs
}

// indexProgressLocked returns the number of pending request logs and of known request logs.
func (s *Store) indexProgressLocked() (pending, total int) {
	for _, e := range s.entries {
		if e.isLog {
			total++
			if e.pending() {
				pending++
			}
		}
	}
	return pending, total
}

// inlineJobsLocked picks the files a refresh parses itself: files waiting to be announced to the live
// tail, and, once the backlog is done, every pending file when there are few. A larger batch goes
// back to the worker.
func (s *Store) inlineJobsLocked() []metaJob {
	if !s.indexing {
		return nil
	}
	pending := s.pendingJobsLocked()
	if !s.backlogDone && len(pending) > 0 || len(pending) > indexBatchSize {
		s.backlogDone = false
		s.ensureIndexingLocked()
		jobs := pending[:0]
		for _, j := range pending {
			if _, ok := s.toAnnounce[j.name]; ok {
				jobs = append(jobs, j)
			}
		}
		return jobs
	}
	return pending
}

// indexWorker parses pending files newest first (by file name timestamp) in batches, without any
// lock while reading, and merges each batch under the lock. It never announces to the live tail.
// It exits when nothing is pending or the generation changes (directory change or stopIndexing).
func (s *Store) indexWorker(gen uint64, dir string, done chan struct{}) {
	finished := false
	defer func() {
		if finished {
			s.saveIndex(true)
		}
		s.mu.Lock()
		if s.workerDone == done {
			s.workerDone = nil
		}
		s.mu.Unlock()
		close(done)
	}()
	stale := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.gen != gen
	}
	for {
		s.mu.Lock()
		if s.gen != gen {
			s.mu.Unlock()
			return
		}
		jobs := s.pendingJobsLocked()
		if len(jobs) == 0 {
			s.backlogDone = true
			s.mu.Unlock()
			finished = true
			return
		}
		s.mu.Unlock()
		sort.Slice(jobs, func(i, j int) bool {
			if jobs[i].stamp != jobs[j].stamp {
				return jobs[i].stamp > jobs[j].stamp
			}
			return jobs[i].name > jobs[j].name
		})
		if len(jobs) > indexBatchSize {
			jobs = jobs[:indexBatchSize]
		}
		results := parseJobs(dir, jobs, stale)
		s.mu.Lock()
		s.mergeMetaLocked(gen, results)
		s.mu.Unlock()
		s.saveIndex(false)
	}
}

// waitIndexIdle blocks until the worker is not running.
func (s *Store) waitIndexIdle() {
	for {
		s.mu.Lock()
		done := s.workerDone
		s.mu.Unlock()
		if done == nil {
			return
		}
		<-done
	}
}

// stopIndexing stops the worker and waits for it. Parses in flight are dropped.
func (s *Store) stopIndexing() {
	s.mu.Lock()
	s.gen++
	s.indexing = false
	s.backlogDone = false
	s.mu.Unlock()
	s.waitIndexIdle()
}
