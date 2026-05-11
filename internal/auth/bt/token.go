package bt

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/misc"
)

type BTTokenStorage struct {
	Phone     string `json:"phone"`
	UID       string `json:"uid"`
	AccessKey string `json:"access_key"`
	ServerID  string `json:"serverid"`
	Type      string `json:"type"`
}

func NewBTTokenStorage(phone, uid, accessKey, serverID string) *BTTokenStorage {
	return &BTTokenStorage{
		Phone:     phone,
		UID:       uid,
		AccessKey: accessKey,
		ServerID:  serverID,
		Type:      "bt",
	}
}

func LoadBTTokenStorage(path string) (*BTTokenStorage, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s BTTokenStorage
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("failed to parse bt token file: %w", err)
	}
	if s.Type != "bt" {
		return nil, fmt.Errorf("invalid token file type: %s", s.Type)
	}
	return &s, nil
}

func (s *BTTokenStorage) SaveTokenToFile(authFilePath string) error {
	misc.LogSavingCredentials(authFilePath)
	s.Type = "bt"
	if err := os.MkdirAll(filepath.Dir(authFilePath), 0700); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}
	f, err := os.OpenFile(authFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create token file: %w", err)
	}
	defer func() { _ = f.Close() }()
	if err = json.NewEncoder(f).Encode(s); err != nil {
		return fmt.Errorf("failed to write token to file: %w", err)
	}
	return nil
}
