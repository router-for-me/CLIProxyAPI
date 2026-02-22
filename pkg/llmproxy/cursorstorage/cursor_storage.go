package cursorstorage

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	_ "modernc.org/sqlite"
)

// ReadAccessToken reads the Cursor access token from the local SQLite storage.
func ReadAccessToken() (string, error) {
	dbPath, err := getDatabasePath()
	if err != nil {
		return "", err
	}

	if _, err := os.Stat(dbPath); os.IsNotExist(err) {
		return "", fmt.Errorf("cursor database not found at %s", dbPath)
	}

	// Connect using the modernc.org/sqlite driver (pure Go)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return "", fmt.Errorf("failed to open cursor database: %w", err)
	}
	defer func() { _ = db.Close() }()

	var value string
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key = ?", "cursor.accessToken").Scan(&value)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", fmt.Errorf("access token not found in cursor database")
		}
		return "", fmt.Errorf("failed to query cursor access token: %w", err)
	}

	return value, nil
}

func getDatabasePath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library/Application Support/Cursor/User/globalStorage/state.vscdb"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA environment variable not set")
		}
		return filepath.Join(appData, "Cursor/User/globalStorage/state.vscdb"), nil
	case "linux":
		return filepath.Join(home, ".config/Cursor/User/globalStorage/state.vscdb"), nil
	default:
		return "", fmt.Errorf("unsupported operating system: %s", runtime.GOOS)
	}
}
