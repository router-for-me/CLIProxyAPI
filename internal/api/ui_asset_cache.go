package api

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

type uiAssetTransform func([]byte) []byte

type uiAssetCache struct {
	mu      sync.Mutex
	limit   int
	entries map[string]*uiAssetCacheEntry
	order   []string
}

type uiAssetCacheEntry struct {
	key         string
	sourceInfo  os.FileInfo
	file        uiAssetFileIdentity
	contentType string
	identity    []byte
	gzip        []byte
	identityTag string
	gzipTag     string
}

type uiAssetFileIdentity struct {
	path        string
	size        int64
	modUnixNano int64
}

func newUIAssetCache(limit int) *uiAssetCache {
	if limit <= 0 {
		limit = 3
	}
	return &uiAssetCache{limit: limit, entries: make(map[string]*uiAssetCacheEntry)}
}

func (s *Server) serveCachedUIAsset(c *gin.Context, filePath string, contentType string, transform uiAssetTransform) {
	if c == nil {
		return
	}
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	s.uiAssetCacheOnce.Do(func() {
		if s.uiAssetCache == nil {
			s.uiAssetCache = newUIAssetCache(3)
		}
	})
	entry, err := s.uiAssetCache.get(filePath, contentType, transform)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		log.WithError(err).Error("failed to read management UI asset")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	useGzip := uiAssetAcceptsGzip(c.GetHeader("Accept-Encoding"))
	etag := entry.identityTag
	body := entry.identity
	if useGzip {
		etag = entry.gzipTag
		body = entry.gzip
	}

	c.Header("Cache-Control", "private, no-cache")
	c.Header("Vary", "Accept-Encoding")
	c.Header("ETag", etag)
	if c.Request != nil && c.Request.Method == http.MethodGet && uiAssetIfNoneMatch(c.GetHeader("If-None-Match"), etag) {
		c.Status(http.StatusNotModified)
		return
	}
	if useGzip {
		c.Header("Content-Encoding", "gzip")
	}
	c.Data(http.StatusOK, entry.contentType, body)
}

func (c *uiAssetCache) get(filePath string, contentType string, transform uiAssetTransform) (*uiAssetCacheEntry, error) {
	info, err := os.Stat(filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			c.removePath(filePath)
		}
		return nil, err
	}
	if info.IsDir() {
		c.removePath(filePath)
		return nil, os.ErrNotExist
	}
	file := uiAssetIdentity(filePath, info)
	key := contentType + "\x00" + filePath

	c.mu.Lock()
	defer c.mu.Unlock()
	if entry := c.entries[key]; entry != nil && entry.file == file && os.SameFile(entry.sourceInfo, info) {
		c.touchLocked(key)
		return entry, nil
	}

	payload, err := os.ReadFile(filePath)
	if err != nil {
		delete(c.entries, key)
		c.removeOrderLocked(key)
		return nil, err
	}
	if transform != nil {
		payload = transform(payload)
	}
	gzPayload, err := uiAssetGzip(payload)
	if err != nil {
		return nil, err
	}
	entry := &uiAssetCacheEntry{
		key:         key,
		file:        file,
		sourceInfo:  info,
		contentType: contentType,
		identity:    append([]byte(nil), payload...),
		gzip:        gzPayload,
		identityTag: uiAssetETag("identity", payload),
		gzipTag:     uiAssetETag("gzip", gzPayload),
	}
	c.entries[key] = entry
	c.touchLocked(key)
	for len(c.order) > c.limit {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	return entry, nil
}

func (c *uiAssetCache) removePath(filePath string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if entry != nil && entry.file.path == filePath {
			delete(c.entries, key)
			c.removeOrderLocked(key)
		}
	}
}

func (c *uiAssetCache) touchLocked(key string) {
	c.removeOrderLocked(key)
	c.order = append(c.order, key)
}

func (c *uiAssetCache) removeOrderLocked(key string) {
	for i, existing := range c.order {
		if existing == key {
			copy(c.order[i:], c.order[i+1:])
			c.order = c.order[:len(c.order)-1]
			return
		}
	}
}

func uiAssetIdentity(filePath string, info os.FileInfo) uiAssetFileIdentity {
	return uiAssetFileIdentity{path: filePath, size: info.Size(), modUnixNano: info.ModTime().UnixNano()}
}

func uiAssetGzip(payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	writer, err := gzip.NewWriterLevel(&buf, gzip.DefaultCompression)
	if err != nil {
		return nil, err
	}
	if _, err = writer.Write(payload); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func uiAssetETag(encoding string, payload []byte) string {
	h := sha256.New()
	_, _ = h.Write([]byte(encoding))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write(payload)
	return fmt.Sprintf(`W/"ui-%s"`, hex.EncodeToString(h.Sum(nil))[:32])
}

func uiAssetAcceptsGzip(header string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	gzipQ := -1.0
	starQ := -1.0
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		segments := strings.Split(part, ";")
		coding := strings.ToLower(strings.TrimSpace(segments[0]))
		q := 1.0
		for _, param := range segments[1:] {
			name, value, ok := strings.Cut(strings.TrimSpace(param), "=")
			if !ok || !strings.EqualFold(strings.TrimSpace(name), "q") {
				continue
			}
			parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
			if err != nil || parsed < 0 {
				parsed = 0
			}
			if parsed > 1 {
				parsed = 1
			}
			q = parsed
		}
		switch coding {
		case "gzip":
			gzipQ = q
		case "*":
			starQ = q
		}
	}
	if gzipQ >= 0 {
		return gzipQ > 0
	}
	return starQ > 0
}

func uiAssetIfNoneMatch(header string, etag string) bool {
	header = strings.TrimSpace(header)
	if header == "" {
		return false
	}
	current := uiAssetWeakCompareTag(etag)
	for _, part := range strings.Split(header, ",") {
		candidate := strings.TrimSpace(part)
		if candidate == "*" {
			return true
		}
		if uiAssetWeakCompareTag(candidate) == current {
			return true
		}
	}
	return false
}

func uiAssetWeakCompareTag(tag string) string {
	tag = strings.TrimSpace(tag)
	if strings.HasPrefix(tag, "W/") {
		tag = strings.TrimSpace(tag[2:])
	}
	return tag
}
