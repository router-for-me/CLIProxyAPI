package usagestats

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

const (
	defaultRecordLimit = 50
	maxRecordLimit     = 200
	reverseReadSize    = 64 * 1024
)

// RecordFilter selects records by optional dimensions.
type RecordFilter struct {
	Provider string
	Model    string
	Account  string
	Failed   *bool
}

// UsageRecord is the public detail-record shape returned by the management API.
type UsageRecord struct {
	Timestamp         time.Time                `json:"timestamp"`
	Provider          string                   `json:"provider"`
	ExecutorType      string                   `json:"executor_type"`
	Model             string                   `json:"model"`
	Alias             string                   `json:"alias"`
	Account           string                   `json:"account"`
	AuthType          string                   `json:"auth_type"`
	RequestID         string                   `json:"request_id,omitempty"`
	Failed            bool                     `json:"failed"`
	StatusCode        int                      `json:"status_code"`
	LatencyMs         int64                    `json:"latency_ms"`
	Stream            bool                     `json:"stream,omitempty"`
	TtftMs            int64                    `json:"ttft_ms,omitempty"`
	ErrorMessage      string                   `json:"error_message,omitempty"`
	AccountingVersion int                      `json:"accounting_version"`
	TokenBreakdown    coreusage.TokenBreakdown `json:"token_breakdown"`
}

// RecordsPage is a reverse-chronological page of usage records.
type RecordsPage struct {
	Records []UsageRecord `json:"records"`
	HasMore bool          `json:"has_more"`
	NextOff int           `json:"next_offset"`
}

// QueryRecords scans append-only daily files newest-first and returns a bounded
// reverse-chronological page without loading an entire file into memory.
func QueryRecords(dir string, from, to time.Time, filter RecordFilter, offset, limit int) (RecordsPage, error) {
	if to.Before(from) {
		from, to = to, from
	}
	if offset < 0 {
		return RecordsPage{}, fmt.Errorf("usagestats: offset must be non-negative")
	}
	if limit <= 0 {
		limit = defaultRecordLimit
	}
	if limit > maxRecordLimit {
		limit = maxRecordLimit
	}

	from = from.UTC()
	to = to.UTC()
	startDay := from.Truncate(24 * time.Hour)
	endDay := to.Truncate(24 * time.Hour)

	result := RecordsPage{
		Records: make([]UsageRecord, 0, limit),
		NextOff: offset,
	}
	remaining := offset
	for day := endDay; !day.Before(startDay); day = day.AddDate(0, 0, -1) {
		path := filepath.Join(dir, "usage-"+day.Format(dateLayout)+".jsonl")
		file, errOpen := os.Open(path)
		if errOpen != nil {
			if os.IsNotExist(errOpen) {
				continue
			}
			return RecordsPage{}, fmt.Errorf("usagestats: open %s: %w", path, errOpen)
		}

		stop, errScan := scanFileReverse(file, func(raw []byte) bool {
			var e entry
			if errUnmarshal := json.Unmarshal(raw, &e); errUnmarshal != nil {
				return false
			}
			if e.Timestamp.Before(from) || e.Timestamp.After(to) || !matchesFilter(e, filter) {
				return false
			}
			if remaining > 0 {
				remaining--
				return false
			}
			result.Records = append(result.Records, toUsageRecord(e))
			result.NextOff++
			if len(result.Records) >= limit {
				result.HasMore = true
				return true
			}
			return false
		})
		closeErr := file.Close()
		if errScan != nil {
			return RecordsPage{}, fmt.Errorf("usagestats: scan %s: %w", path, errScan)
		}
		if closeErr != nil {
			return RecordsPage{}, fmt.Errorf("usagestats: close %s: %w", path, closeErr)
		}
		if stop {
			return result, nil
		}
	}
	return result, nil
}

func matchesFilter(e entry, filter RecordFilter) bool {
	if filter.Provider != "" && e.Provider != filter.Provider {
		return false
	}
	if filter.Model != "" && e.Model != filter.Model {
		return false
	}
	if filter.Account != "" && e.Account != filter.Account {
		return false
	}
	return filter.Failed == nil || e.Failed == *filter.Failed
}

func toUsageRecord(e entry) UsageRecord {
	return UsageRecord{
		Timestamp:         e.Timestamp,
		Provider:          e.Provider,
		ExecutorType:      e.ExecutorType,
		Model:             e.Model,
		Alias:             e.Alias,
		Account:           e.Account,
		AuthType:          e.AuthType,
		RequestID:         e.RequestID,
		Failed:            e.Failed,
		StatusCode:        e.StatusCode,
		LatencyMs:         e.LatencyMs,
		Stream:            e.Stream,
		TtftMs:            e.TtftMs,
		ErrorMessage:      e.ErrorMessage,
		AccountingVersion: e.AccountingVersion,
		TokenBreakdown:    e.TokenBreakdown,
	}
}

func scanFileReverse(file *os.File, visit func([]byte) bool) (bool, error) {
	info, errStat := file.Stat()
	if errStat != nil {
		return false, errStat
	}
	position := info.Size()
	carry := []byte(nil)
	buffer := make([]byte, reverseReadSize)

	for position > 0 {
		readSize := int64(len(buffer))
		if readSize > position {
			readSize = position
		}
		position -= readSize
		n, errRead := file.ReadAt(buffer[:readSize], position)
		if errRead != nil && errRead != io.EOF {
			return false, errRead
		}
		combined := make([]byte, 0, n+len(carry))
		combined = append(combined, buffer[:n]...)
		combined = append(combined, carry...)

		end := len(combined)
		if end > 0 && combined[end-1] == '\n' {
			end--
		}
		for end > 0 {
			separator := bytes.LastIndexByte(combined[:end], '\n')
			if separator < 0 {
				break
			}
			if visit(combined[separator+1 : end]) {
				return true, nil
			}
			end = separator
		}
		carry = append(carry[:0], combined[:end]...)
	}

	if len(carry) > 0 && visit(carry) {
		return true, nil
	}
	return false, nil
}
