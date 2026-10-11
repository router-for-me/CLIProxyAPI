package claudemaster

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"
)

// WatchAccountLabels re-reads the account labels when their file changes (a new inode, size or modification time:
// the server's refresh replaces it with a rename) and swaps them into the running telemetry, so a label added to the
// secret reaches the metrics without a restart. load reads and checks the labels; when it fails, the labels already in
// use stay and the failure is logged once for that version of the file. It returns when ctx ends.
func WatchAccountLabels(ctx context.Context, file string, every time.Duration, load func() (map[string]string, error)) {
	watchAccountLabelsFrom(ctx, file, accountLabelsFingerprint(file), every, load)
}

// watchAccountLabelsFrom is WatchAccountLabels with the starting fingerprint taken by the caller, so a change made
// right after the call is never mistaken for the starting state.
func watchAccountLabelsFrom(ctx context.Context, file, last string, every time.Duration, load func() (map[string]string, error)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		now := accountLabelsFingerprint(file)
		if now == last || now == "" {
			continue
		}
		last = now
		labels, err := load()
		if err != nil {
			lg().Warn("account labels file changed but was not loaded; the previous labels stay", "why", err.Error())
			continue
		}
		if SetAccountLabels(labels) {
			lg().Info("account labels reloaded", "names", len(labels))
		}
	}
}

func accountLabelsFingerprint(file string) string {
	info, err := os.Stat(file)
	if err != nil {
		return ""
	}
	var ino uint64
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		ino = st.Ino
	}
	return fmt.Sprintf("%d/%d/%d", ino, info.Size(), info.ModTime().UnixNano())
}
