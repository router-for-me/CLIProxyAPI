package logqa

import "time"

// RequestRecord is the QA view of a single source .log file (one API request).
type RequestRecord struct {
	SourceFile   string    `json:"source_file"`
	KeyName      string    `json:"key_name"`
	Fingerprint  string    `json:"fingerprint"`
	SessionID    string    `json:"session_id"`
	ThreadID     string    `json:"thread_id"`
	RequestKind  string    `json:"request_kind,omitempty"`
	Timestamp    time.Time `json:"timestamp"`
	ModTime      time.Time `json:"mod_time"`
	SizeBytes    int64     `json:"size_bytes"`
	InputLen     int       `json:"input_len"`
	PromptRounds int       `json:"prompt_rounds"`
	ToolCalls    int       `json:"tool_calls"`
	DupAssistant int       `json:"dup_assistant_groups"`
	// HasRealSessionID is false when session_id was empty and a synthetic key was used.
	HasRealSessionID bool `json:"has_real_session_id"`
	// UnpairedToolCalls is true when function_call / *_output call_id pairing fails.
	UnpairedToolCalls bool `json:"unpaired_tool_calls"`
	// LastResponseType is the last completed RESPONSE output item type when parsed.
	LastResponseType string `json:"last_response_type,omitempty"`
	// ResponseParsed is true when a RESPONSE tail type was recovered.
	ResponseParsed bool     `json:"response_parsed"`
	SamplePrompts  []string `json:"sample_prompts,omitempty"`
	// Title is best-effort Codex thread title (from title-generation turns) or empty.
	Title string `json:"title,omitempty"`
	// TitleSource: "codex_title" | "user_prompt" | "".
	TitleSource string `json:"title_source,omitempty"`
	ParseError  string `json:"parse_error,omitempty"`
	// AssistantTexts kept only in memory during aggregation, not always serialized.
	AssistantTexts []string `json:"-"`
}

// SessionRecord is the session-level QA verdict.
type SessionRecord struct {
	SessionID string `json:"session_id"`
	// Title is the Codex-side thread title when recoverable; otherwise a short
	// first-user-prompt preview so operators can match the conversation in the UI.
	Title              string   `json:"title,omitempty"`
	TitleSource        string   `json:"title_source,omitempty"` // codex_title | user_prompt
	ThreadIDs          []string `json:"thread_ids,omitempty"`
	KeyNames           []string `json:"key_names,omitempty"`
	PromptRounds       int      `json:"prompt_rounds"`
	ToolCalls          int      `json:"tool_calls"`
	DupAssistantGroups int      `json:"dup_assistant_groups"`
	OK                 bool     `json:"ok"`
	FailReasons        []string `json:"fail_reasons,omitempty"`
	// UploadEligibility mirrors uploader session-gate: hold | eligible | orphan.
	UploadEligibility string    `json:"upload_eligibility,omitempty"`
	FirstTS           time.Time `json:"first_ts"`
	LastTS            time.Time `json:"last_ts"`
	SourceFiles       []string  `json:"source_files,omitempty"`
	SamplePrompts     []string  `json:"sample_prompts,omitempty"`
	InputLen          int       `json:"input_len"`
	RequestCount      int       `json:"request_count"`
}

// RunSummary is written to summary.json for each QA run.
type RunSummary struct {
	RunID                   string         `json:"run_id"`
	StartedAt               time.Time      `json:"started_at"`
	FinishedAt              time.Time      `json:"finished_at"`
	FilesSeen               int            `json:"files_seen"`
	FilesScanned            int            `json:"files_scanned"`
	FilesSkippedHot         int            `json:"files_skipped_hot"`
	FilesSkippedUnchanged   int            `json:"files_skipped_unchanged"`
	FilesSkippedCurrentHour int            `json:"files_skipped_current_hour"`
	FilesSkippedTooLarge    int            `json:"files_skipped_too_large"`
	FilesDisappeared        int            `json:"files_disappeared"`
	FilesParseError         int            `json:"files_parse_error"`
	BytesScanned            int64          `json:"bytes_scanned"`
	SessionsTotal           int            `json:"sessions_total"`
	SessionsPass            int            `json:"sessions_pass"`
	SessionsFail            int            `json:"sessions_fail"`
	PassRate                float64        `json:"pass_rate"`
	FailReasonHist          map[string]int `json:"fail_reason_hist"`
	Partial                 bool           `json:"partial"`
	AggregationKey          string         `json:"aggregation_key"`
	LogsRoot                string         `json:"logs_root"`
	WorkDir                 string         `json:"work_dir"`
}

// LatestPointer points at the newest report directory.
type LatestPointer struct {
	RunID string `json:"run_id"`
	Dir   string `json:"dir"`
}

// FileState tracks incremental scan fingerprints.
type FileState struct {
	Path         string    `json:"path"`
	ScannedAt    time.Time `json:"scanned_at"`
	SessionID    string    `json:"session_id"`
	ThreadID     string    `json:"thread_id"`
	InputLen     int       `json:"input_len"`
	PromptRounds int       `json:"prompt_rounds"`
	ToolCalls    int       `json:"tool_calls"`
	DupAssistant int       `json:"dup_assistant_groups"`
	ParseError   string    `json:"parse_error,omitempty"`
	// Cached request fields for re-aggregation without re-read when unchanged.
	Timestamp         time.Time `json:"timestamp"`
	KeyName           string    `json:"key_name"`
	RequestKind       string    `json:"request_kind,omitempty"`
	SamplePrompts     []string  `json:"sample_prompts,omitempty"`
	Title             string    `json:"title,omitempty"`
	TitleSource       string    `json:"title_source,omitempty"`
	HasRealSessionID  bool      `json:"has_real_session_id"`
	UnpairedToolCalls bool      `json:"unpaired_tool_calls"`
	LastResponseType  string    `json:"last_response_type,omitempty"`
	ResponseParsed    bool      `json:"response_parsed"`
	// Note: assistant texts not persisted (can be large); re-scan on rule changes.
	// For unchanged files we re-use stored numeric metrics only when re-aggregating
	// from state — but rule 3 needs assistant texts. Unchanged files still need
	// re-read for full accuracy OR we store hashes. MVP: re-use metrics from state
	// and set DupAssistant from state; full re-parse only on fingerprint change.
	ModTime   time.Time `json:"mod_time"`
	SizeBytes int64     `json:"size_bytes"`
}

// QAState is the durable incremental index.
type QAState struct {
	SchemaVersion int                  `json:"schema_version"`
	Files         map[string]FileState `json:"files"`
	LastRunAt     time.Time            `json:"last_run_at"`
}
