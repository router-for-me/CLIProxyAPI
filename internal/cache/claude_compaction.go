package cache

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
)

const claudeCompactionMaxBytes = 8 << 20

var currentClaudeCompactionKVClient = func() (codexReasoningReplayKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// Compaction state is immutable. A distinct reference per compact operation keeps
// concurrent agents, retries, and branched conversations independent.
// Home uses shared KV; standalone servers persist files across process restarts.
func StoreClaudeCompaction(ctx context.Context, payload []byte) (string, error) {
	if len(payload) == 0 || len(payload) > claudeCompactionMaxBytes {
		return "", fmt.Errorf("invalid compaction cache payload size")
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	ref := hex.EncodeToString(random[:])
	if client, enabled, err := currentClaudeCompactionKVClient(); enabled {
		if err != nil {
			return "", err
		}
		written, err := client.KVSet(ctx, "cpa:claude:compaction:"+ref, payload, homekv.KVSetOptions{})
		if err != nil {
			return "", err
		}
		if !written {
			return "", fmt.Errorf("compaction cache write rejected")
		}
		return ref, nil
	}
	dir, err := claudeCompactionDirectory()
	if err != nil {
		return "", err
	}
	if err = os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	file, err := os.CreateTemp(dir, ".pending-")
	if err != nil {
		return "", err
	}
	name := file.Name()
	defer func() { _ = os.Remove(name) }()
	_, errWrite := file.Write(payload)
	errSync := file.Sync()
	errClose := file.Close()
	if errWrite != nil {
		return "", errWrite
	}
	if errSync != nil {
		return "", errSync
	}
	if errClose != nil {
		return "", errClose
	}
	if err = os.Rename(name, filepath.Join(dir, ref+".json")); err != nil {
		return "", err
	}
	return ref, nil
}

// LoadClaudeCompaction fails closed if a reference is missing. Sending only the
// post-compaction messages would silently discard the conversation's context.
func LoadClaudeCompaction(ctx context.Context, ref string) ([]byte, error) {
	decoded, err := hex.DecodeString(ref)
	if err != nil || len(decoded) != 32 || len(ref) != 64 {
		return nil, fmt.Errorf("invalid compaction cache reference")
	}
	if client, enabled, err := currentClaudeCompactionKVClient(); enabled {
		if err != nil {
			return nil, err
		}
		payload, found, err := client.KVGet(ctx, "cpa:claude:compaction:"+ref)
		if err != nil {
			return nil, err
		}
		if !found {
			return nil, fmt.Errorf("compaction state is unavailable; restore the server compaction cache or start a new conversation")
		}
		if len(payload) > claudeCompactionMaxBytes {
			return nil, fmt.Errorf("compaction cache entry is too large")
		}
		return payload, nil
	}
	dir, err := claudeCompactionDirectory()
	if err != nil {
		return nil, err
	}
	file, err := os.Open(filepath.Join(dir, ref+".json"))
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("compaction state is unavailable; restore the server compaction cache or start a new conversation")
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	payload, err := io.ReadAll(io.LimitReader(file, claudeCompactionMaxBytes+1))
	if len(payload) > claudeCompactionMaxBytes {
		return nil, fmt.Errorf("compaction cache entry is too large")
	}
	return payload, err
}

func claudeCompactionDirectory() (string, error) {
	dir := util.WritablePath()
	if dir == "" {
		var err error
		dir, err = os.UserCacheDir()
		if err != nil {
			return "", err
		}
	}
	return filepath.Join(dir, "cliproxyapi", "claude-compaction"), nil
}
