package requestlogs

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/regexp"

	"github.com/MoeForge/moego/base64"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/proxx/groups"
	log "github.com/sirupsen/logrus"
)

// fileNameRe matches request log names: [error-]<sanitized-path>-<YYYY-MM-DDTHHMMSS>[_n]-<id>.log.
// Rotated app logs (main-2006-01-02T15-04-05.000.log) and *.tmp spool files never match.
var fileNameRe = regexp.MustCompile(`^(error-)?(.+)-(\d{4}-\d{2}-\d{2}T\d{6})(?:_\d+)?-([A-Za-z0-9]+)\.log$`)

// settleDelay is how long a file must stay unmodified before it counts as complete.
const settleDelay = time.Second

// metaFullParseLimit is the file size above which indexing reads only the head and the tail.
const metaFullParseLimit = 16 << 20

// metaTailBytes is how much of a large file's tail is searched for the RESPONSE section.
const metaTailBytes = 1 << 20

// ErrNotFound is returned when no request log matches an id.
var ErrNotFound = errors.New("request log not found")

// ErrBadCursor is returned for a malformed cursor.
var ErrBadCursor = errors.New("invalid cursor")

// Row is one request log in the list view (JSON contract shared with the panel).
type Row struct {
	ID              string   `json:"id"`
	File            string   `json:"file"`
	Time            string   `json:"time"`
	TimeMs          int64    `json:"time_ms"`
	Method          string   `json:"method"`
	Path            string   `json:"path"`
	Status          int      `json:"status"`
	DurationMs      *int64   `json:"duration_ms"`
	Stream          bool     `json:"stream"`
	RequestedModel  string   `json:"requested_model"`
	ServedModel     string   `json:"served_model"`
	Group           string   `json:"group"`
	Provider        string   `json:"provider"`
	InputTokens     *int64   `json:"input_tokens"`
	OutputTokens    *int64   `json:"output_tokens"`
	ReasoningTokens *int64   `json:"reasoning_tokens"`
	CachedTokens    *int64   `json:"cached_tokens"`
	CostUSD         *float64 `json:"cost_usd"`
	CostSource      string   `json:"cost_source"`
	Attempts        int      `json:"attempts"`
	Error           bool     `json:"error"`
	SizeBytes       int64    `json:"size_bytes"`
}

type entry struct {
	name        string
	id          string
	errorPrefix bool
	size        int64
	mtime       time.Time
	meta        fileMeta
	parsed      bool
	complete    bool
	silent      bool
}

func (e *entry) timeOf() time.Time {
	if !e.meta.time.IsZero() {
		return e.meta.time
	}
	return e.mtime
}

// Store indexes the request log directory.
type Store struct {
	mu       sync.Mutex
	dir      string
	maxBytes int64
	enabled  bool
	entries  map[string]*entry
	total    int64
	scanned  bool
	subs     map[chan Row]struct{}
	usage    *usageIndex
	now      func() time.Time
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{entries: map[string]*entry{}, subs: map[chan Row]struct{}{}, usage: newUsageIndex(), now: time.Now}
}

// SetClock overrides the clock (tests).
func (s *Store) SetClock(now func() time.Time) {
	s.mu.Lock()
	s.now = now
	s.mu.Unlock()
}

// Configure sets the log directory, the retention cap (0 = unlimited) and the enabled flag.
func (s *Store) Configure(dir string, maxBytes int64, enabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if dir != s.dir {
		s.dir = dir
		s.entries = map[string]*entry{}
		s.total = 0
		s.scanned = false
	}
	s.maxBytes = maxBytes
	s.enabled = enabled
}

// Status reports the directory, enabled flag, total size and cap.
type Status struct {
	Enabled       bool
	Dir           string
	TotalBytes    int64
	MaxTotalBytes int64
}

// Status returns the current index status (call Refresh first for fresh totals).
func (s *Store) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Enabled: s.enabled, Dir: s.dir, TotalBytes: s.total, MaxTotalBytes: s.maxBytes}
}

// Refresh rescans the directory, parses new or changed files, enforces retention and
// notifies subscribers about files that just completed.
func (s *Store) Refresh() {
	s.mu.Lock()
	rows := s.refreshLocked()
	subs := make([]chan Row, 0, len(s.subs))
	for ch := range s.subs {
		subs = append(subs, ch)
	}
	s.mu.Unlock()
	for _, row := range rows {
		for _, ch := range subs {
			select {
			case ch <- row:
			default:
			}
		}
	}
}

func (s *Store) refreshLocked() []Row {
	if s.dir == "" {
		return nil
	}
	dirEntries, err := os.ReadDir(s.dir)
	if err != nil {
		if !os.IsNotExist(err) {
			log.WithError(err).Debug("proxx: read request log dir")
		}
		s.entries = map[string]*entry{}
		s.total = 0
		s.scanned = true
		return nil
	}
	now := s.now()
	initial := !s.scanned
	seen := make(map[string]struct{}, len(dirEntries))
	var completed []*entry
	for _, de := range dirEntries {
		if de.IsDir() {
			continue
		}
		name := de.Name()
		m := fileNameRe.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		seen[name] = struct{}{}
		e := s.entries[name]
		if e != nil && e.complete {
			continue
		}
		info, errInfo := de.Info()
		if errInfo != nil || !info.Mode().IsRegular() {
			continue
		}
		if e == nil {
			e = &entry{name: name, id: m[4], errorPrefix: m[1] != ""}
			e.silent = initial && now.Sub(info.ModTime()) >= settleDelay
			s.entries[name] = e
		}
		if !e.parsed || e.size != info.Size() || !e.mtime.Equal(info.ModTime()) {
			e.size, e.mtime = info.Size(), info.ModTime()
			meta, errMeta := readMetaFile(filepath.Join(s.dir, name), e.size)
			if logging.ShortRequestID(meta.requestID) != e.id {
				meta.requestID = ""
			}
			e.meta, e.parsed = meta, errMeta == nil
		}
		if e.meta.valid && e.size > 0 && now.Sub(e.mtime) >= settleDelay {
			e.complete = true
			if !e.silent {
				completed = append(completed, e)
			}
		}
	}
	for name := range s.entries {
		if _, ok := seen[name]; !ok {
			delete(s.entries, name)
		}
	}
	s.scanned = true
	s.recountLocked()
	s.enforceLocked()
	rows := make([]Row, 0, len(completed))
	sort.Slice(completed, func(i, j int) bool { return completed[i].timeOf().Before(completed[j].timeOf()) })
	for _, e := range completed {
		if _, ok := s.entries[e.name]; ok {
			rows = append(rows, s.rowOf(e))
		}
	}
	return rows
}

func (s *Store) recountLocked() {
	var total int64
	for _, e := range s.entries {
		if e.meta.valid {
			total += e.size
		}
	}
	s.total = total
}

// enforceLocked deletes the oldest complete request logs until the total fits the cap.
func (s *Store) enforceLocked() {
	if s.maxBytes <= 0 || s.total <= s.maxBytes {
		return
	}
	var list []*entry
	for _, e := range s.entries {
		if e.meta.valid && e.complete {
			list = append(list, e)
		}
	}
	sort.Slice(list, func(i, j int) bool {
		if !list[i].mtime.Equal(list[j].mtime) {
			return list[i].mtime.Before(list[j].mtime)
		}
		return list[i].name < list[j].name
	})
	removed := 0
	for _, e := range list {
		if s.total <= s.maxBytes {
			break
		}
		if err := os.Remove(filepath.Join(s.dir, e.name)); err != nil && !os.IsNotExist(err) {
			log.WithError(err).Warnf("proxx: failed to remove request log %s", e.name)
			continue
		}
		delete(s.entries, e.name)
		s.total -= e.size
		removed++
	}
	if removed > 0 {
		log.Infof("proxx: request log retention removed %d file(s); total now %d bytes (cap %d)", removed, s.total, s.maxBytes)
	}
}

// readMetaFile indexes a file; files above metaFullParseLimit are read head + tail only.
func readMetaFile(path string, size int64) (fileMeta, error) {
	if size <= metaFullParseLimit {
		return readMeta(path)
	}
	return readMetaLarge(path, size)
}

func readMetaLarge(path string, size int64) (fileMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return fileMeta{}, err
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Debug("proxx: close request log")
		}
	}()
	meta, err := readMetaReader(io.LimitReader(f, metaFullParseLimit))
	if err != nil || !meta.valid {
		return meta, err
	}
	tail := make([]byte, metaTailBytes)
	n, err := f.ReadAt(tail, size-metaTailBytes)
	if err != nil && err != io.EOF {
		return meta, nil
	}
	tail = tail[:n]
	idx := bytes.LastIndex(tail, []byte("\n=== RESPONSE ===\n"))
	if idx < 0 {
		return meta, nil
	}
	section := io.MultiReader(strings.NewReader("=== REQUEST INFO ===\n\n"), bytes.NewReader(tail[idx+1:]))
	tailMeta, errTail := readMetaReader(section)
	if errTail == nil {
		meta.status = tailMeta.status
		meta.servedHeader = tailMeta.servedHeader
		meta.requestID = tailMeta.requestID
		meta.stream = meta.stream || tailMeta.stream
		meta.tokens = tailMeta.tokens
	}
	return meta, nil
}

// rowOf builds the list row, joining in-memory usage records at read time.
func (s *Store) rowOf(e *entry) Row {
	t := e.timeOf()
	m := e.meta
	row := Row{
		ID:             e.id,
		File:           e.name,
		Time:           t.Format(time.RFC3339),
		TimeMs:         t.UnixMilli(),
		Method:         m.method,
		Path:           m.path,
		Status:         m.status,
		Stream:         m.stream,
		RequestedModel: m.requestedModel,
		Attempts:       m.attempts,
		Error:          e.errorPrefix || m.status >= 400,
		SizeBytes:      e.size,
	}
	if !m.time.IsZero() && !e.mtime.Before(m.time) {
		d := e.mtime.Sub(m.time).Milliseconds()
		row.DurationMs = &d
	}
	if m.servedHeader != "" {
		row.ServedModel = m.servedHeader
		row.Group = m.requestedModel
	} else if groups.IsGroup(m.requestedModel) {
		row.Group = m.requestedModel
	}
	row.Provider = m.authProvider
	if agg, ok := s.usage.get(m.requestID, e.id); ok {
		if agg.provider != "" {
			row.Provider = agg.provider
		}
		if row.ServedModel == "" {
			row.ServedModel = agg.model
		}
		in, out, cached, reasoning := agg.in, agg.out, agg.cached, agg.reasoning
		// A failed request whose attempts reported no usage has unknown (not zero) tokens and cost.
		if row.Error && in+out+cached+reasoning == 0 {
			if m.tokens.any() {
				row.InputTokens, row.OutputTokens = m.tokens.in, m.tokens.out
				row.ReasoningTokens, row.CachedTokens = m.tokens.reasoning, m.tokens.cached
			}
		} else {
			row.InputTokens, row.OutputTokens, row.CachedTokens, row.ReasoningTokens = &in, &out, &cached, &reasoning
			if agg.costKnown {
				cost := agg.cost
				row.CostUSD = &cost
			}
			row.CostSource = agg.costSource
			if row.CostSource == "" {
				row.CostSource = "unknown"
			}
		}
		if row.Attempts == 0 {
			row.Attempts = agg.records
		}
	} else if m.tokens.any() {
		row.InputTokens, row.OutputTokens = m.tokens.in, m.tokens.out
		row.ReasoningTokens, row.CachedTokens = m.tokens.reasoning, m.tokens.cached
	}
	return row
}

// Query filters a list request.
type Query struct {
	Limit    int
	Cursor   string
	Status   string
	Model    string
	Provider string
	Q        string
	Since    int64
	Until    int64
}

// Page is one page of rows.
type Page struct {
	Items      []Row
	NextCursor string
	// Models and Providers list every distinct value in the Since/Until window, ignoring the other
	// filters and paging, so the viewer's dropdowns offer values that are not loaded yet. Only the
	// first page (no cursor) fills them.
	Models    []string
	Providers []string
}

func encodeCursor(r Row) string {
	return base64.RawURLEncoding.EncodeToString([]byte(strconv.FormatInt(r.TimeMs, 10) + "|" + r.File))
}

func decodeCursor(raw string) (int64, string, error) {
	data, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return 0, "", ErrBadCursor
	}
	ts, file, ok := strings.Cut(string(data), "|")
	if !ok {
		return 0, "", ErrBadCursor
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return 0, "", ErrBadCursor
	}
	return n, file, nil
}

func rowBefore(a Row, ts int64, file string) bool {
	return a.TimeMs < ts || (a.TimeMs == ts && a.File < file)
}

func matchStatus(r Row, status string) bool {
	switch strings.ToLower(status) {
	case "":
		return true
	case "2xx":
		return r.Status >= 200 && r.Status < 300
	case "4xx":
		return r.Status >= 400 && r.Status < 500
	case "5xx":
		return r.Status >= 500 && r.Status < 600
	case "error":
		return r.Error
	}
	return false
}

// ValidStatusFilter reports whether status is a supported status filter value.
func ValidStatusFilter(status string) bool {
	switch strings.ToLower(status) {
	case "", "2xx", "4xx", "5xx", "error":
		return true
	}
	return false
}

func (s *Store) rowsSortedLocked() []Row {
	rows := make([]Row, 0, len(s.entries))
	for _, e := range s.entries {
		if e.meta.valid {
			rows = append(rows, s.rowOf(e))
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].TimeMs != rows[j].TimeMs {
			return rows[i].TimeMs > rows[j].TimeMs
		}
		return rows[i].File > rows[j].File
	})
	return rows
}

// List returns rows newest first. Cheap filters run on the index; q scans file contents.
func (s *Store) List(q Query) (Page, error) {
	if q.Limit <= 0 {
		q.Limit = 50
	}
	var (
		curTS   int64
		curFile string
	)
	if q.Cursor != "" {
		var err error
		if curTS, curFile, err = decodeCursor(q.Cursor); err != nil {
			return Page{}, err
		}
	}
	s.Refresh()
	s.mu.Lock()
	dir := s.dir
	all := s.rowsSortedLocked()
	s.mu.Unlock()

	model := strings.ToLower(strings.TrimSpace(q.Model))
	provider := strings.ToLower(strings.TrimSpace(q.Provider))
	needle := []byte(strings.ToLower(q.Q))
	items := make([]Row, 0, q.Limit)
	more := false
	for _, r := range all {
		if q.Cursor != "" && !rowBefore(r, curTS, curFile) {
			continue
		}
		if !matchStatus(r, q.Status) {
			continue
		}
		if q.Since > 0 && r.TimeMs < q.Since {
			continue
		}
		if q.Until > 0 && r.TimeMs > q.Until {
			continue
		}
		if model != "" && strings.ToLower(r.RequestedModel) != model && strings.ToLower(r.ServedModel) != model &&
			strings.ToLower(r.Group) != model {
			continue
		}
		if provider != "" && strings.ToLower(r.Provider) != provider {
			continue
		}
		if len(needle) > 0 && !fileContains(filepath.Join(dir, r.File), needle) {
			continue
		}
		if len(items) == q.Limit {
			more = true
			break
		}
		items = append(items, r)
	}
	page := Page{Items: items}
	if more && len(items) > 0 {
		page.NextCursor = encodeCursor(items[len(items)-1])
	}
	if q.Cursor == "" {
		page.Models, page.Providers = facets(all, q.Since, q.Until)
	}
	return page, nil
}

// facets returns the sorted distinct models (group, requested and served) and providers of rows
// in the [since, until] window (0 = open).
func facets(rows []Row, since, until int64) ([]string, []string) {
	models, providers := map[string]struct{}{}, map[string]struct{}{}
	for _, r := range rows {
		if (since > 0 && r.TimeMs < since) || (until > 0 && r.TimeMs > until) {
			continue
		}
		for _, m := range []string{r.Group, r.RequestedModel, r.ServedModel} {
			if m != "" {
				models[m] = struct{}{}
			}
		}
		if r.Provider != "" {
			providers[r.Provider] = struct{}{}
		}
	}
	return sortedKeys(models), sortedKeys(providers)
}

func sortedKeys(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// fileContains reports whether the file contains needle (already lower-cased), ignoring case.
func fileContains(path string, needle []byte) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.WithError(errClose).Debug("proxx: close request log")
		}
	}()
	const chunk = 1 << 20
	overlap := len(needle) - 1
	buf := make([]byte, chunk+overlap)
	keep := 0
	for {
		n, errRead := f.Read(buf[keep:])
		if n > 0 {
			window := buf[:keep+n]
			if bytes.Contains(bytes.ToLower(window), needle) {
				return true
			}
			if overlap > 0 && len(window) > overlap {
				copy(buf, window[len(window)-overlap:])
				keep = overlap
			} else {
				keep = len(window)
			}
		}
		if errRead != nil {
			return false
		}
	}
}

// find resolves an id (short request id, full request id, or file name) to the newest file.
func (s *Store) find(id string) (*entry, string, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, `/\`) || strings.Contains(id, "..") {
		return nil, "", ErrNotFound
	}
	s.Refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	if e, ok := s.entries[id]; ok && e.meta.valid {
		return e, filepath.Join(s.dir, e.name), nil
	}
	short := logging.ShortRequestID(id)
	var best *entry
	for _, e := range s.entries {
		if e.id != short || !e.meta.valid {
			continue
		}
		if best == nil || e.timeOf().After(best.timeOf()) || (e.timeOf().Equal(best.timeOf()) && e.name > best.name) {
			best = e
		}
	}
	if best == nil {
		return nil, "", ErrNotFound
	}
	return best, filepath.Join(s.dir, best.name), nil
}

// Detail parses the file for id.
func (s *Store) Detail(id string) (*Detail, error) {
	e, path, err := s.find(id)
	if err != nil {
		return nil, err
	}
	d, err := readDetail(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	s.mu.Lock()
	d.Row = s.rowOf(e)
	d.RawSizeBytes = e.size
	s.mu.Unlock()
	return d, nil
}

// RawPath returns the file path for id.
func (s *Store) RawPath(id string) (string, error) {
	_, path, err := s.find(id)
	return path, err
}

// Delete removes the file for id.
func (s *Store) Delete(id string) error {
	e, path, err := s.find(id)
	if err != nil {
		return err
	}
	if errRemove := os.Remove(path); errRemove != nil && !os.IsNotExist(errRemove) {
		return fmt.Errorf("remove request log: %w", errRemove)
	}
	s.mu.Lock()
	delete(s.entries, e.name)
	s.recountLocked()
	s.mu.Unlock()
	return nil
}

// DeleteAll removes every request log file (never app logs) and returns how many were removed.
func (s *Store) DeleteAll() (int, error) {
	s.Refresh()
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	var firstErr error
	for name, e := range s.entries {
		if !e.meta.valid {
			continue
		}
		if err := os.Remove(filepath.Join(s.dir, name)); err != nil && !os.IsNotExist(err) {
			if firstErr == nil {
				firstErr = fmt.Errorf("remove request log: %w", err)
			}
			continue
		}
		delete(s.entries, name)
		removed++
	}
	s.recountLocked()
	return removed, firstErr
}

// Subscribe registers for rows of newly completed files; call the returned func to stop.
func (s *Store) Subscribe() (<-chan Row, func()) {
	ch := make(chan Row, 64)
	s.mu.Lock()
	s.subs[ch] = struct{}{}
	s.mu.Unlock()
	return ch, func() {
		s.mu.Lock()
		delete(s.subs, ch)
		s.mu.Unlock()
	}
}

func (s *Store) hasSubscribers() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.subs) > 0
}
