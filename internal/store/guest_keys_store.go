package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/minio/minio-go/v7"
)

// GuestKeysFileName is the management guest-key file kept beside config.yaml.
const GuestKeysFileName = "guest-keys.json"

const (
	postgresGuestKeysKey    = "guest-keys" // row id in the config table
	objectStoreGuestKeysKey = "config/" + GuestKeysFileName
)

// readGuestKeysFile returns nil when the file is missing or empty.
func readGuestKeysFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, nil
	}
	return data, nil
}

func writeGuestKeysFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (s *PostgresStore) guestKeysPath() string {
	return filepath.Join(filepath.Dir(s.configPath), GuestKeysFileName)
}

// PersistGuestKeys mirrors the local guest-key file to PostgreSQL.
func (s *PostgresStore) PersistGuestKeys(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := readGuestKeysFile(s.guestKeysPath())
	if err != nil {
		return fmt.Errorf("postgres store: read guest keys: %w", err)
	}
	if data == nil {
		query := fmt.Sprintf("DELETE FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
		if _, err = s.db.ExecContext(ctx, query, postgresGuestKeysKey); err != nil {
			return fmt.Errorf("postgres store: delete guest keys: %w", err)
		}
		return nil
	}
	return s.upsertGuestKeys(ctx, data)
}

func (s *PostgresStore) upsertGuestKeys(ctx context.Context, data []byte) error {
	query := fmt.Sprintf(`
		INSERT INTO %s (id, content, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
		ON CONFLICT (id)
		DO UPDATE SET content = EXCLUDED.content, updated_at = NOW()
	`, s.fullTableName(s.cfg.ConfigTable))
	if _, err := s.db.ExecContext(ctx, query, postgresGuestKeysKey, string(data)); err != nil {
		return fmt.Errorf("postgres store: upsert guest keys: %w", err)
	}
	return nil
}

// syncGuestKeysFromDatabase writes the stored guest keys to the spool. When the
// database has none, a file left in the spool by an earlier run seeds it.
func (s *PostgresStore) syncGuestKeysFromDatabase(ctx context.Context) error {
	query := fmt.Sprintf("SELECT content FROM %s WHERE id = $1", s.fullTableName(s.cfg.ConfigTable))
	var content string
	err := s.db.QueryRowContext(ctx, query, postgresGuestKeysKey).Scan(&content)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		data, errRead := readGuestKeysFile(s.guestKeysPath())
		if errRead != nil {
			return fmt.Errorf("postgres store: read guest keys: %w", errRead)
		}
		if data != nil {
			return s.upsertGuestKeys(ctx, data)
		}
	case err != nil:
		return fmt.Errorf("postgres store: load guest keys from database: %w", err)
	default:
		if err = writeGuestKeysFile(s.guestKeysPath(), []byte(content)); err != nil {
			return fmt.Errorf("postgres store: write guest keys to spool: %w", err)
		}
	}
	return nil
}

func (s *ObjectTokenStore) guestKeysPath() string {
	return filepath.Join(filepath.Dir(s.configPath), GuestKeysFileName)
}

// PersistGuestKeys uploads the local guest-key file to the object storage backend.
func (s *ObjectTokenStore) PersistGuestKeys(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := readGuestKeysFile(s.guestKeysPath())
	if err != nil {
		return fmt.Errorf("object store: read guest keys: %w", err)
	}
	if data == nil {
		return s.deleteObject(ctx, objectStoreGuestKeysKey)
	}
	return s.putObject(ctx, objectStoreGuestKeysKey, data, "application/json")
}

// syncGuestKeysFromBucket writes the stored guest keys to the spool. When the
// bucket has none, a file left in the spool by an earlier run seeds it.
func (s *ObjectTokenStore) syncGuestKeysFromBucket(ctx context.Context) error {
	key := s.prefixedKey(objectStoreGuestKeysKey)
	_, err := s.client.StatObject(ctx, s.cfg.Bucket, key, minio.StatObjectOptions{})
	switch {
	case err == nil:
		object, errGet := s.client.GetObject(ctx, s.cfg.Bucket, key, minio.GetObjectOptions{})
		if errGet != nil {
			return fmt.Errorf("object store: fetch guest keys: %w", errGet)
		}
		defer object.Close()
		data, errRead := io.ReadAll(object)
		if errRead != nil {
			return fmt.Errorf("object store: read guest keys: %w", errRead)
		}
		if errWrite := writeGuestKeysFile(s.guestKeysPath(), data); errWrite != nil {
			return fmt.Errorf("object store: write guest keys: %w", errWrite)
		}
	case isObjectNotFound(err):
		data, errRead := readGuestKeysFile(s.guestKeysPath())
		if errRead != nil {
			return fmt.Errorf("object store: read guest keys: %w", errRead)
		}
		if data != nil {
			return s.putObject(ctx, objectStoreGuestKeysKey, data, "application/json")
		}
	default:
		return fmt.Errorf("object store: stat guest keys: %w", err)
	}
	return nil
}

// PersistGuestKeys commits and pushes the guest-key file to git. The file is
// part of the cloned repository, so it is restored with the clone.
func (s *GitTokenStore) PersistGuestKeys(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureRepositoryLocked(); err != nil {
		return err
	}
	configPath := s.ConfigPath()
	if configPath == "" {
		return fmt.Errorf("git token store: config path not configured")
	}
	rel, err := s.relativeToRepo(filepath.Join(filepath.Dir(configPath), GuestKeysFileName))
	if err != nil {
		return err
	}
	return s.commitAndPushLocked("Update guest keys", rel)
}
