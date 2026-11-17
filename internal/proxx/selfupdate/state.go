package selfupdate

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/MoeForge/moego/ojson"
)

const stateFileName = "proxx-update-state.json"

// maxStartAttempts is the number of starts a new version gets to become healthy; the start that reaches
// it rolls back to the previous binary instead.
const maxStartAttempts = 3

// lastResult is the outcome of the most recent applied update.
type lastResult struct {
	From       string `json:"from"`
	To         string `json:"to"`
	At         string `json:"at"`
	OK         bool   `json:"ok"`
	RolledBack bool   `json:"rolled_back"`
	Error      string `json:"error"`
}

// updateState is persisted across the exec so the new process can confirm health or roll back.
type updateState struct {
	PendingVersion string
	FromVersion    string
	ExePath        string
	PrevPath       string
	Attempts       int
	StartedAt      string
	Last           *lastResult
}

func (s *updateState) encode() ([]byte, error) {
	doc := ojson.NewDocString("{}")
	set := func(path string, v any) error { return doc.Set(path, v) }
	if err := set("pending_version", s.PendingVersion); err != nil {
		return nil, err
	}
	_ = set("from_version", s.FromVersion)
	_ = set("exe_path", s.ExePath)
	_ = set("prev_path", s.PrevPath)
	_ = set("attempts", s.Attempts)
	_ = set("started_at", s.StartedAt)
	if s.Last != nil {
		_ = set("last_result.from", s.Last.From)
		_ = set("last_result.to", s.Last.To)
		_ = set("last_result.at", s.Last.At)
		_ = set("last_result.ok", s.Last.OK)
		_ = set("last_result.rolled_back", s.Last.RolledBack)
		if err := set("last_result.error", s.Last.Error); err != nil {
			return nil, err
		}
	}
	return doc.Bytes(), nil
}

func decodeState(data []byte) (*updateState, error) {
	if !ojson.ValidBytes(data) {
		return nil, fmt.Errorf("update state is not valid JSON")
	}
	r := ojson.ParseBytes(data)
	s := &updateState{
		PendingVersion: r.Get("pending_version").String(),
		FromVersion:    r.Get("from_version").String(),
		ExePath:        r.Get("exe_path").String(),
		PrevPath:       r.Get("prev_path").String(),
		Attempts:       int(r.Get("attempts").Int()),
		StartedAt:      r.Get("started_at").String(),
	}
	if lr := r.Get("last_result"); lr.IsObject() {
		s.Last = &lastResult{
			From:       lr.Get("from").String(),
			To:         lr.Get("to").String(),
			At:         lr.Get("at").String(),
			OK:         lr.Get("ok").Bool(),
			RolledBack: lr.Get("rolled_back").Bool(),
			Error:      lr.Get("error").String(),
		}
	}
	return s, nil
}

// loadState reads the state file; a missing file yields an empty state.
func loadState(path string) (*updateState, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &updateState{}, nil
		}
		return nil, err
	}
	return decodeState(data)
}

// saveState writes the state file atomically (temp file + rename) and fsyncs it.
func saveState(path string, s *updateState) error {
	data, err := s.encode()
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if errClose := f.Close(); err == nil {
		err = errClose
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

// configPathFromArgs mirrors cmd/server's --config handling: the flag value, else <wd>/config.yaml.
func configPathFromArgs(args []string, wd string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			break
		}
		name := strings.TrimLeft(a, "-")
		if name == a || (len(a)-len(name)) > 2 {
			continue
		}
		if v, ok := strings.CutPrefix(name, "config="); ok {
			return v
		}
		if name == "config" && i+1 < len(args) {
			return args[i+1]
		}
	}
	return filepath.Join(wd, "config.yaml")
}

// statePathFromArgs is the state file next to the config file.
func statePathFromArgs(args []string) string {
	wd, _ := os.Getwd()
	cfg := configPathFromArgs(args, wd)
	if abs, err := filepath.Abs(cfg); err == nil {
		cfg = abs
	}
	return filepath.Join(filepath.Dir(cfg), stateFileName)
}
