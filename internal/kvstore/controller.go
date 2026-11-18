package kvstore

import (
	"fmt"
	"sync"

	log "github.com/sirupsen/logrus"
)

var (
	controllerMu sync.RWMutex
	homeProvider func() (KVClient, bool, error)
	localEnabled bool
	localStore   *BoltStore
	localPath    string
	localErr     error
)

// RegisterHomeProvider registers the callback used by CurrentKVClient to inspect
// whether Home mode is active and obtain the active Home KV client.
func RegisterHomeProvider(fn func() (KVClient, bool, error)) {
	controllerMu.Lock()
	homeProvider = fn
	controllerMu.Unlock()
}

// ConfigureLocal enables, reconfigures, or disables the local bbolt-backed KV store.
func ConfigureLocal(enabled bool, rawPath string) error {
	controllerMu.Lock()
	defer controllerMu.Unlock()

	if !enabled {
		return shutdownLocalLocked()
	}

	resolvedPath, errResolve := ResolvePath(rawPath)
	if errResolve != nil {
		if localStore != nil {
			if errClose := localStore.Close(); errClose != nil {
				log.Errorf("local kv store close failed: %v", errClose)
			}
			localStore = nil
			localPath = ""
		}
		localEnabled = true
		localErr = errResolve
		log.Errorf("local kv store resolve path failed: %v", errResolve)
		return errResolve
	}

	if localStore != nil && localPath == resolvedPath && localErr == nil {
		localEnabled = true
		return nil
	}

	if localStore != nil {
		if errClose := localStore.Close(); errClose != nil {
			log.Errorf("local kv store close failed: %v", errClose)
		}
		localStore = nil
		localPath = ""
	}

	store, errOpen := OpenBoltStore(resolvedPath)
	if errOpen != nil {
		localEnabled = true
		localErr = errOpen
		log.Errorf("local kv store open failed (path=%s): %v", resolvedPath, errOpen)
		return errOpen
	}

	localEnabled = true
	localStore = store
	localPath = resolvedPath
	localErr = nil
	return nil
}

// ShutdownLocal closes any open local BoltStore and resets local KV state to disabled.
func ShutdownLocal() error {
	controllerMu.Lock()
	defer controllerMu.Unlock()
	return shutdownLocalLocked()
}

func shutdownLocalLocked() error {
	var errClose error
	if localStore != nil {
		errClose = localStore.Close()
	}
	localEnabled = false
	localStore = nil
	localPath = ""
	localErr = nil
	return errClose
}

// CurrentKVClient returns the active KVClient using the three-tier routing order:
//  1. Home KV when Home mode is active (never falling back to local bbolt).
//  2. Local BoltStore when server.kv-store.disabled is false.
//  3. (nil, false, nil) for default in-memory storage.
func CurrentKVClient() (KVClient, bool, error) {
	controllerMu.RLock()
	provider := homeProvider
	controllerMu.RUnlock()

	if provider != nil {
		if client, homeMode, errHome := provider(); homeMode {
			return client, true, errHome
		}
	}

	controllerMu.RLock()
	defer controllerMu.RUnlock()

	if !localEnabled {
		return nil, false, nil
	}
	if localErr != nil {
		return nil, true, fmt.Errorf("local kv store unavailable: %w", localErr)
	}
	if localStore == nil {
		return nil, true, fmt.Errorf("local kv store unavailable: %w", errStoreClosed)
	}
	return localStore, true, nil
}
