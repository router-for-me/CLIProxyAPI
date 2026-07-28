package logqa

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const qaStateSchemaVersion = 1

func statePath(workDir string) string {
	return filepath.Join(workDir, "qa-state.json")
}

func loadState(workDir string) (QAState, error) {
	path := statePath(workDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return QAState{
				SchemaVersion: qaStateSchemaVersion,
				Files:         make(map[string]FileState),
			}, nil
		}
		return QAState{}, fmt.Errorf("read qa state: %w", err)
	}
	var state QAState
	if err := json.Unmarshal(raw, &state); err != nil {
		return QAState{}, fmt.Errorf("parse qa state: %w", err)
	}
	if state.Files == nil {
		state.Files = make(map[string]FileState)
	}
	if state.SchemaVersion == 0 {
		state.SchemaVersion = qaStateSchemaVersion
	}
	return state, nil
}

func saveState(workDir string, state QAState) error {
	if err := os.MkdirAll(workDir, 0o750); err != nil {
		return fmt.Errorf("create work dir: %w", err)
	}
	state.SchemaVersion = qaStateSchemaVersion
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal qa state: %w", err)
	}
	tmp := statePath(workDir) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("write qa state tmp: %w", err)
	}
	if err := os.Rename(tmp, statePath(workDir)); err != nil {
		return fmt.Errorf("rename qa state: %w", err)
	}
	return nil
}

func fileStateFromRequest(rec RequestRecord, now time.Time) FileState {
	return FileState{
		Path:              rec.SourceFile,
		ScannedAt:         now,
		SessionID:         rec.SessionID,
		ThreadID:          rec.ThreadID,
		InputLen:          rec.InputLen,
		PromptRounds:      rec.PromptRounds,
		ToolCalls:         rec.ToolCalls,
		DupAssistant:      rec.DupAssistant,
		ParseError:        rec.ParseError,
		Timestamp:         rec.Timestamp,
		KeyName:           rec.KeyName,
		RequestKind:       rec.RequestKind,
		SamplePrompts:     rec.SamplePrompts,
		Title:             rec.Title,
		TitleSource:       rec.TitleSource,
		HasRealSessionID:  rec.HasRealSessionID,
		UnpairedToolCalls: rec.UnpairedToolCalls,
		LastResponseType:  rec.LastResponseType,
		ResponseParsed:    rec.ResponseParsed,
		ModTime:           rec.ModTime,
		SizeBytes:         rec.SizeBytes,
	}
}

func requestFromFileState(fp string, st FileState) RequestRecord {
	return RequestRecord{
		SourceFile:        st.Path,
		KeyName:           st.KeyName,
		Fingerprint:       fp,
		SessionID:         st.SessionID,
		ThreadID:          st.ThreadID,
		RequestKind:       st.RequestKind,
		Timestamp:         st.Timestamp,
		ModTime:           st.ModTime,
		SizeBytes:         st.SizeBytes,
		InputLen:          st.InputLen,
		PromptRounds:      st.PromptRounds,
		ToolCalls:         st.ToolCalls,
		DupAssistant:      st.DupAssistant,
		SamplePrompts:     st.SamplePrompts,
		Title:             st.Title,
		TitleSource:       st.TitleSource,
		ParseError:        st.ParseError,
		HasRealSessionID:  st.HasRealSessionID,
		UnpairedToolCalls: st.UnpairedToolCalls,
		LastResponseType:  st.LastResponseType,
		ResponseParsed:    st.ResponseParsed,
	}
}
