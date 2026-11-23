package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const configSyncAttempts = 5

var errRemoteConfigChanged = errors.New("remote config changed during persistence")

type remoteConfigVersion struct {
	data []byte
	etag string
}

// A nil result with no error means the remote write lost its precondition.
type writeRemoteConfig func(context.Context, *remoteConfigVersion, []byte) (*remoteConfigVersion, error)

// remoteConfigMirror tracks the version this instance can safely replace. The
// owning store holds its mutex across synchronization and publication.
type remoteConfigMirror struct {
	baseline *remoteConfigVersion // nil allows only a conditional create.
}

func (m *remoteConfigMirror) persist(ctx context.Context, path string, optional bool, write writeRemoteConfig) error {
	data, errPrepare := config.PrepareConfigFileForUploadWithOptional(path, optional)
	if errPrepare != nil {
		return fmt.Errorf("prepare config for upload: %w", errPrepare)
	}
	if data == nil {
		return nil
	}
	published, errWrite := write(ctx, m.baseline, data)
	if errWrite != nil {
		return fmt.Errorf("publish config: %w", errWrite)
	}
	if published == nil {
		return errRemoteConfigChanged
	}
	m.baseline = published
	return nil
}

// sync never publishes a speculative rewrite to the local mirror. A rejected
// remote CAS must use the winner's exact bytes as the next input.
func (m *remoteConfigMirror) sync(ctx context.Context, path, example string, optional bool,
	read func(context.Context) (*remoteConfigVersion, error), write writeRemoteConfig,
) (*config.Config, error) {
	local, errLocal := readLocalConfigSnapshot(path)
	if errLocal != nil {
		if optional && errors.Is(errLocal, config.ErrConfigFileDirectory) {
			return config.ParsePreparedConfigBytes(nil)
		}
		return nil, errLocal
	}
	for range configSyncAttempts {
		if errCtx := ctx.Err(); errCtx != nil {
			return nil, errCtx
		}
		remote, errRead := read(ctx)
		if errRead != nil {
			return nil, errRead
		}
		var data []byte
		if remote == nil {
			data, errRead = localConfigOrTemplate(local, example, optional)
			if errRead != nil {
				return nil, errRead
			}
		} else {
			data = remote.data
		}
		prepared, errPrepare := config.PrepareConfigBytesForUploadWithOptional(data, optional)
		if errPrepare != nil {
			return nil, fmt.Errorf("prepare config for upload: %w", errPrepare)
		}
		if prepared != nil && (remote == nil || !bytes.Equal(data, prepared)) {
			var errWrite error
			remote, errWrite = write(ctx, remote, prepared)
			if errWrite != nil {
				return nil, errWrite
			}
			if remote == nil {
				continue
			}
		}
		cfg, errParse := config.ParsePreparedConfigBytes(prepared)
		if errParse != nil {
			return nil, errParse
		}
		if remote != nil {
			// Mirror a remote standby snapshot too, even over a previously valid config.
			if errWrite := writeLocalConfigSnapshot(path, local, remote.data); errWrite != nil {
				return nil, errWrite
			}
		}
		m.baseline = remote
		return cfg, nil
	}
	return nil, fmt.Errorf("remote config changed during %d synchronization attempts", configSyncAttempts)
}

func readLocalConfigSnapshot(path string) (*config.ConfigFileSnapshot, error) {
	snapshot, errRead := config.ReadConfigFileSnapshot(path)
	if errors.Is(errRead, os.ErrNotExist) && !errors.Is(errRead, config.ErrConfigFileChanged) {
		return nil, nil
	}
	return snapshot, errRead
}

func localConfigOrTemplate(local *config.ConfigFileSnapshot, example string, optional bool) ([]byte, error) {
	if local != nil {
		return local.Bytes(), nil
	}
	if example == "" {
		return nil, nil
	}
	data, errRead := config.ReadConfigFile(example)
	if optional && (errors.Is(errRead, os.ErrNotExist) || errors.Is(errRead, config.ErrConfigFileDirectory)) && !errors.Is(errRead, config.ErrConfigFileChanged) {
		return nil, nil
	}
	return data, errRead
}

func writeLocalConfigSnapshot(path string, expected *config.ConfigFileSnapshot, data []byte) error {
	var errWrite error
	if expected == nil {
		if errMkdir := os.MkdirAll(filepath.Dir(path), 0o700); errMkdir != nil {
			return errMkdir
		}
		errWrite = config.CreateConfigFile(path, data)
	} else {
		_, errWrite = config.WriteConfigFileCAS(path, expected, data)
	}
	if errors.Is(errWrite, config.ErrConfigFileChanged) {
		// A peer may already have mirrored the winner. Never retry the write
		// against a fresh snapshot, since that could overwrite a user's edit.
		current, errRead := config.ReadConfigFile(path)
		if errRead == nil && bytes.Equal(current, data) {
			return nil
		}
	}
	return errWrite
}
