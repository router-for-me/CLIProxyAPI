package backup

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// WebDAVStorage implements Storage interface for WebDAV storage.
type WebDAVStorage struct {
	url      string
	username string
	password string
	basePath string
	client   *http.Client
}

// NewWebDAVStorage creates a new WebDAV storage backend.
func NewWebDAVStorage(cfg WebDAVConfig) (*WebDAVStorage, error) {
	if cfg.URL == "" {
		return nil, fmt.Errorf("WebDAV URL is required")
	}

	// Ensure URL doesn't end with slash
	url := strings.TrimSuffix(cfg.URL, "/")

	return &WebDAVStorage{
		url:      url,
		username: cfg.Username,
		password: cfg.Password,
		basePath: strings.Trim(cfg.Path, "/"),
		client:   &http.Client{},
	}, nil
}

// Upload uploads a backup file to WebDAV.
func (s *WebDAVStorage) Upload(filename string, data []byte) error {
	url := s.fileURL(filename)

	req, err := http.NewRequest(http.MethodPut, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("failed to create WebDAV request: %w", err)
	}

	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	req.Header.Set("Content-Type", "application/zip")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to upload to WebDAV: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("WebDAV upload failed with status %d: %s", resp.StatusCode, string(body))
	}

	log.Infof("backup uploaded to WebDAV: %s", url)
	return nil
}

// List returns all available backups in WebDAV.
func (s *WebDAVStorage) List() ([]BackupInfo, error) {
	url := s.dirURL()

	req, err := http.NewRequest("PROPFIND", url, strings.NewReader(`<?xml version="1.0"?>
<d:propfind xmlns:d="DAV:">
  <d:prop>
    <d:displayname/>
    <d:getcontentlength/>
    <d:getlastmodified/>
  </d:prop>
</d:propfind>`))
	if err != nil {
		return nil, fmt.Errorf("failed to create WebDAV PROPFIND request: %w", err)
	}

	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Depth", "1")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to list WebDAV directory: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("WebDAV list failed with status %d: %s", resp.StatusCode, string(body))
	}

	// Simple parsing: extract files ending with .zip
	// For production, should use proper XML parsing
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read WebDAV response: %w", err)
	}

	// Parse backup files from WebDAV response
	backups := s.parseWebDAVResponse(string(body))

	// Sort by name (which includes timestamp)
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].Name < backups[j].Name
	})

	return backups, nil
}

// Download retrieves a backup file from WebDAV.
func (s *WebDAVStorage) Download(name string) ([]byte, error) {
	url := s.fileURL(name)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create WebDAV request: %w", err)
	}

	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to download from WebDAV: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("WebDAV download failed with status %d: %s", resp.StatusCode, string(body))
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read WebDAV response: %w", err)
	}

	return data, nil
}

// Delete removes a backup file from WebDAV.
func (s *WebDAVStorage) Delete(name string) error {
	url := s.fileURL(name)

	req, err := http.NewRequest(http.MethodDelete, url, nil)
	if err != nil {
		return fmt.Errorf("failed to create WebDAV request: %w", err)
	}

	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to delete from WebDAV: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("WebDAV delete failed with status %d: %s", resp.StatusCode, string(body))
	}

	log.Infof("backup deleted from WebDAV: %s", url)
	return nil
}

// TestConnection tests if the WebDAV storage is accessible.
func (s *WebDAVStorage) TestConnection() error {
	url := s.dirURL()

	req, err := http.NewRequest("PROPFIND", url, nil)
	if err != nil {
		return fmt.Errorf("failed to create WebDAV request: %w", err)
	}

	if s.username != "" {
		req.SetBasicAuth(s.username, s.password)
	}
	req.Header.Set("Depth", "0")

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to connect to WebDAV: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusMultiStatus {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("WebDAV connection test failed with status %d: %s", resp.StatusCode, string(body))
	}

	return nil
}

// fileURL constructs the full WebDAV file URL.
func (s *WebDAVStorage) fileURL(filename string) string {
	if s.basePath == "" {
		return fmt.Sprintf("%s/%s", s.url, filename)
	}
	return fmt.Sprintf("%s/%s/%s", s.url, s.basePath, filename)
}

// dirURL constructs the WebDAV directory URL.
func (s *WebDAVStorage) dirURL() string {
	if s.basePath == "" {
		return s.url
	}
	return fmt.Sprintf("%s/%s", s.url, s.basePath)
}

// parseWebDAVResponse is a simple parser for WebDAV PROPFIND responses.
// For production use, should use proper XML parsing with encoding/xml.
func (s *WebDAVStorage) parseWebDAVResponse(body string) []BackupInfo {
	var backups []BackupInfo

	// Simple string-based parsing - look for .zip files
	lines := strings.Split(body, "\n")
	var currentFile string
	var currentSize int64
	var currentTime time.Time

	for _, line := range lines {
		line = strings.TrimSpace(line)

		// Extract filename from href or displayname
		if strings.Contains(line, ".zip") {
			if strings.Contains(line, "<d:href>") || strings.Contains(line, "<D:href>") {
				start := strings.Index(line, ">")
				end := strings.LastIndex(line, "<")
				if start > 0 && end > start {
					href := line[start+1 : end]
					currentFile = path.Base(href)
				}
			} else if strings.Contains(line, "<d:displayname>") || strings.Contains(line, "<D:displayname>") {
				start := strings.Index(line, ">")
				end := strings.LastIndex(line, "<")
				if start > 0 && end > start {
					currentFile = line[start+1 : end]
				}
			}
		}

		// Extract size
		if strings.Contains(line, "<d:getcontentlength>") || strings.Contains(line, "<D:getcontentlength>") {
			start := strings.Index(line, ">")
			end := strings.LastIndex(line, "<")
			if start > 0 && end > start {
				sizeStr := line[start+1 : end]
				fmt.Sscanf(sizeStr, "%d", &currentSize)
			}
		}

		// Extract timestamp
		if strings.Contains(line, "<d:getlastmodified>") || strings.Contains(line, "<D:getlastmodified>") {
			start := strings.Index(line, ">")
			end := strings.LastIndex(line, "<")
			if start > 0 && end > start {
				timeStr := line[start+1 : end]
				// Try to parse RFC1123 format
				if t, err := time.Parse(time.RFC1123, timeStr); err == nil {
					currentTime = t
				}
			}
		}

		// When we hit the end of a response element, add the backup
		if strings.Contains(line, "</d:response>") || strings.Contains(line, "</D:response>") {
			if currentFile != "" && strings.HasSuffix(currentFile, ".zip") {
				backups = append(backups, BackupInfo{
					Name:      currentFile,
					Timestamp: currentTime,
					Size:      currentSize,
					Storage:   StorageTypeWebDAV,
					Location:  s.fileURL(currentFile),
				})
			}
			currentFile = ""
			currentSize = 0
			currentTime = time.Time{}
		}
	}

	return backups
}
