package usage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
	_ "modernc.org/sqlite"
)

const schemaVersion = 1

const schema = `
CREATE TABLE IF NOT EXISTS usage_requests (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	timestamp DATETIME NOT NULL,
	api_key TEXT NOT NULL,
	model TEXT NOT NULL,
	source TEXT DEFAULT '',
	auth_index TEXT DEFAULT '',
	input_tokens INTEGER DEFAULT 0,
	output_tokens INTEGER DEFAULT 0,
	reasoning_tokens INTEGER DEFAULT 0,
	cached_tokens INTEGER DEFAULT 0,
	total_tokens INTEGER DEFAULT 0,
	failed INTEGER DEFAULT 0,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_requests_timestamp ON usage_requests(timestamp);
CREATE INDEX IF NOT EXISTS idx_requests_api_model ON usage_requests(api_key, model);
CREATE INDEX IF NOT EXISTS idx_requests_day ON usage_requests(date(timestamp));
`

// SQLiteStoreConfig configures the SQLite usage store.
type SQLiteStoreConfig struct {
	AuthDir       string // Used to construct DB path: {AuthDir}/usage.db
	RetentionDays int    // 0 = keep forever
}

// SQLiteStore provides persistent storage for usage statistics using SQLite.
type SQLiteStore struct {
	db       *sql.DB
	config   SQLiteStoreConfig
	mu       sync.Mutex
	stopChan chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

// NewSQLiteStore creates a new SQLite-backed usage store.
func NewSQLiteStore(config SQLiteStoreConfig) (*SQLiteStore, error) {
	if config.AuthDir == "" {
		return nil, fmt.Errorf("usage sqlite: auth dir is required")
	}

	dbPath := filepath.Join(config.AuthDir, "usage.db")

	if err := os.MkdirAll(config.AuthDir, 0o700); err != nil {
		return nil, fmt.Errorf("usage sqlite: create directory: %w", err)
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("usage sqlite: open database: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("usage sqlite: create schema: %w", err)
	}

	log.Infof("usage sqlite: initialized at %s", dbPath)

	return &SQLiteStore{
		db:       db,
		config:   config,
		stopChan: make(chan struct{}),
	}, nil
}

// Record inserts a single usage record into the database.
func (s *SQLiteStore) Record(record RequestDetail, apiKey, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`
		INSERT INTO usage_requests
		(timestamp, api_key, model, source, auth_index,
		 input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		record.Timestamp, apiKey, model, record.Source, record.AuthIndex,
		record.Tokens.InputTokens, record.Tokens.OutputTokens,
		record.Tokens.ReasoningTokens, record.Tokens.CachedTokens,
		record.Tokens.TotalTokens, boolToInt(record.Failed))
	return err
}

// LoadSnapshot retrieves all usage data from the database and returns it as a snapshot.
func (s *SQLiteStore) LoadSnapshot() (StatisticsSnapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := s.db.Query(`
		SELECT timestamp, api_key, model, source, auth_index,
		       input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed
		FROM usage_requests
		ORDER BY timestamp ASC`)
	if err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("usage sqlite: query records: %w", err)
	}
	defer rows.Close()

	snapshot := StatisticsSnapshot{
		APIs: make(map[string]APISnapshot),
	}

	apiData := make(map[string]map[string][]RequestDetail)

	for rows.Next() {
		var (
			timestamp                                                             time.Time
			apiKey, model, source, authIndex                                      string
			inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens int64
			failed                                                                int
		)

		err := rows.Scan(&timestamp, &apiKey, &model, &source, &authIndex,
			&inputTokens, &outputTokens, &reasoningTokens, &cachedTokens, &totalTokens, &failed)
		if err != nil {
			log.Warnf("usage sqlite: scan row: %v", err)
			continue
		}

		detail := RequestDetail{
			Timestamp: timestamp,
			Source:    source,
			AuthIndex: authIndex,
			Tokens: TokenStats{
				InputTokens:     inputTokens,
				OutputTokens:    outputTokens,
				ReasoningTokens: reasoningTokens,
				CachedTokens:    cachedTokens,
				TotalTokens:     totalTokens,
			},
			Failed: failed != 0,
		}

		if apiData[apiKey] == nil {
			apiData[apiKey] = make(map[string][]RequestDetail)
		}
		apiData[apiKey][model] = append(apiData[apiKey][model], detail)
	}

	if err := rows.Err(); err != nil {
		return StatisticsSnapshot{}, fmt.Errorf("usage sqlite: iterate rows: %w", err)
	}

	for apiKey, models := range apiData {
		apiSnapshot := APISnapshot{
			Models: make(map[string]ModelSnapshot),
		}

		for model, details := range models {
			var totalRequests, totalTokens int64
			for _, detail := range details {
				totalRequests++
				totalTokens += detail.Tokens.TotalTokens

				snapshot.TotalRequests++
				snapshot.TotalTokens += detail.Tokens.TotalTokens
				if detail.Failed {
					snapshot.FailureCount++
				} else {
					snapshot.SuccessCount++
				}
			}

			apiSnapshot.TotalRequests += totalRequests
			apiSnapshot.TotalTokens += totalTokens
			apiSnapshot.Models[model] = ModelSnapshot{
				TotalRequests: totalRequests,
				TotalTokens:   totalTokens,
				Details:       details,
			}
		}

		snapshot.APIs[apiKey] = apiSnapshot
	}

	log.Infof("usage sqlite: loaded %d records from database", snapshot.TotalRequests)
	return snapshot, nil
}

// ImportSnapshot imports usage data from a snapshot into the database.
// It uses deduplication to avoid inserting duplicate records.
func (s *SQLiteStore) ImportSnapshot(snapshot StatisticsSnapshot) (added, skipped int64, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return 0, 0, fmt.Errorf("usage sqlite: begin transaction: %w", err)
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare(`
		INSERT INTO usage_requests
		(timestamp, api_key, model, source, auth_index,
		 input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens, failed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return 0, 0, fmt.Errorf("usage sqlite: prepare statement: %w", err)
	}
	defer stmt.Close()

	seen := make(map[string]struct{})
	rows, err := s.db.Query("SELECT timestamp, api_key, model, source, auth_index, failed, input_tokens, output_tokens, reasoning_tokens, cached_tokens, total_tokens FROM usage_requests")
	if err != nil {
		return 0, 0, fmt.Errorf("usage sqlite: query existing records: %w", err)
	}
	for rows.Next() {
		var timestamp time.Time
		var apiKey, model, source, authIndex string
		var failed int
		var inputTokens, outputTokens, reasoningTokens, cachedTokens, totalTokens int64
		if err := rows.Scan(&timestamp, &apiKey, &model, &source, &authIndex, &failed, &inputTokens, &outputTokens, &reasoningTokens, &cachedTokens, &totalTokens); err == nil {
			detail := RequestDetail{
				Timestamp: timestamp,
				Source:    source,
				AuthIndex: authIndex,
				Tokens: TokenStats{
					InputTokens:     inputTokens,
					OutputTokens:    outputTokens,
					ReasoningTokens: reasoningTokens,
					CachedTokens:    cachedTokens,
					TotalTokens:     totalTokens,
				},
				Failed: failed != 0,
			}
			seen[dedupKey(apiKey, model, detail)] = struct{}{}
		}
	}
	rows.Close()

	for apiKey, apiSnapshot := range snapshot.APIs {
		for model, modelSnapshot := range apiSnapshot.Models {
			for _, detail := range modelSnapshot.Details {
				key := dedupKey(apiKey, model, detail)
				if _, exists := seen[key]; exists {
					skipped++
					continue
				}
				seen[key] = struct{}{}

				_, err := stmt.Exec(
					detail.Timestamp, apiKey, model, detail.Source, detail.AuthIndex,
					detail.Tokens.InputTokens, detail.Tokens.OutputTokens,
					detail.Tokens.ReasoningTokens, detail.Tokens.CachedTokens,
					detail.Tokens.TotalTokens, boolToInt(detail.Failed))
				if err != nil {
					log.Warnf("usage sqlite: insert record: %v", err)
					continue
				}
				added++
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("usage sqlite: commit transaction: %w", err)
	}

	log.Infof("usage sqlite: imported %d records (skipped %d duplicates)", added, skipped)
	return added, skipped, nil
}

// StartRetentionCleanup starts a background goroutine that periodically deletes old records.
func (s *SQLiteStore) StartRetentionCleanup(interval time.Duration) {
	if s.config.RetentionDays <= 0 {
		log.Info("usage sqlite: retention cleanup disabled (retention days = 0)")
		return
	}

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		cleanup := func() {
			cutoff := time.Now().AddDate(0, 0, -s.config.RetentionDays)
			s.mu.Lock()
			result, err := s.db.Exec("DELETE FROM usage_requests WHERE timestamp < ?", cutoff)
			s.mu.Unlock()

			if err != nil {
				log.Warnf("usage sqlite: retention cleanup failed: %v", err)
				return
			}

			deleted, _ := result.RowsAffected()
			if deleted > 0 {
				log.Infof("usage sqlite: retention cleanup deleted %d old records", deleted)
			}
		}

		cleanup()

		for {
			select {
			case <-s.stopChan:
				return
			case <-ticker.C:
				cleanup()
			}
		}
	}()

	log.Infof("usage sqlite: retention cleanup started (keeping %d days, interval %v)", s.config.RetentionDays, interval)
}

// Close stops the retention cleanup goroutine and closes the database.
func (s *SQLiteStore) Close() error {
	s.stopOnce.Do(func() {
		close(s.stopChan)
		s.wg.Wait()
	})
	return s.db.Close()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
