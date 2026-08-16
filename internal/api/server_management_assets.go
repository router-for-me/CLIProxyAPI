package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	log "github.com/sirupsen/logrus"
)

const maxManagementHTMLSize = 16 << 20

type managementHTMLAssetCache struct {
	mu         sync.Mutex
	path       string
	modTime    time.Time
	size       int64
	sourceInfo os.FileInfo
	html       []byte
}

func (s *Server) serveManagementControlPanel(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	filePath := managementasset.FilePath(s.configFilePath)
	if strings.TrimSpace(filePath) == "" {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	if _, errStat := os.Stat(filePath); errStat != nil {
		if os.IsNotExist(errStat) {
			available := managementasset.EnsureLatestManagementHTML(
				context.Background(),
				managementasset.StaticDir(s.configFilePath),
				cfg.ProxyURL,
				cfg.RemoteManagement.PanelGitHubRepository,
			)
			if !available {
				c.AbortWithStatus(http.StatusNotFound)
				return
			}
		} else {
			log.WithError(errStat).Error("failed to stat management control panel asset")
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}

	html, errRead := s.readManagementHTML(filePath)
	if errRead != nil {
		if errors.Is(errRead, os.ErrNotExist) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		log.WithError(errRead).Error("failed to read management control panel asset")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Content-Length", strconv.Itoa(len(html)))
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", html)
}

func (s *Server) readManagementHTML(filePath string) ([]byte, error) {
	file, errOpen := os.Open(filePath)
	if errOpen != nil {
		return nil, errOpen
	}

	info, errStat := file.Stat()
	if errStat != nil {
		if errClose := file.Close(); errClose != nil {
			return nil, fmt.Errorf(
				"stat management control panel asset: %v; close management control panel asset: %w",
				errStat,
				errClose,
			)
		}
		return nil, fmt.Errorf("stat management control panel asset: %w", errStat)
	}
	if !info.Mode().IsRegular() {
		if errClose := file.Close(); errClose != nil {
			return nil, fmt.Errorf(
				"management control panel asset is not a regular file; close management control panel asset: %w",
				errClose,
			)
		}
		return nil, fmt.Errorf("management control panel asset is not a regular file")
	}
	if info.Size() > maxManagementHTMLSize {
		if errClose := file.Close(); errClose != nil {
			return nil, fmt.Errorf(
				"management control panel asset exceeds %d bytes; close management control panel asset: %w",
				maxManagementHTMLSize,
				errClose,
			)
		}
		return nil, fmt.Errorf(
			"management control panel asset exceeds %d bytes",
			maxManagementHTMLSize,
		)
	}

	cache := &s.managementHTMLCache
	cache.mu.Lock()
	if cache.path == filePath &&
		cache.size == info.Size() &&
		cache.modTime.Equal(info.ModTime()) &&
		cache.sourceInfo != nil &&
		os.SameFile(cache.sourceInfo, info) &&
		len(cache.html) > 0 {
		html := cache.html
		cache.mu.Unlock()
		if errClose := file.Close(); errClose != nil {
			return nil, fmt.Errorf("close management control panel asset: %w", errClose)
		}
		return html, nil
	}
	cache.mu.Unlock()

	html, errRead := io.ReadAll(io.LimitReader(file, maxManagementHTMLSize+1))
	errClose := file.Close()
	if errRead != nil {
		if errClose != nil {
			return nil, fmt.Errorf(
				"read management control panel asset: %v; close management control panel asset: %w",
				errRead,
				errClose,
			)
		}
		return nil, fmt.Errorf("read management control panel asset: %w", errRead)
	}
	if errClose != nil {
		return nil, fmt.Errorf("close management control panel asset: %w", errClose)
	}
	if len(html) > maxManagementHTMLSize {
		return nil, fmt.Errorf(
			"management control panel asset exceeds %d bytes",
			maxManagementHTMLSize,
		)
	}

	html = managementasset.InjectRequestLogUsageScript(html)
	html = managementasset.InjectLogQAScript(html)

	cache.mu.Lock()
	cache.path = filePath
	cache.modTime = info.ModTime()
	cache.size = info.Size()
	cache.sourceInfo = info
	cache.html = html
	cache.mu.Unlock()

	return html, nil
}

func (s *Server) serveRequestLogUsageScript(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	writeStaticManagementAsset(
		c,
		"application/javascript; charset=utf-8",
		managementasset.RequestLogUsageScript(),
	)
}

func (s *Server) serveLogQAScript(c *gin.Context) {
	cfg := s.cfg
	if cfg == nil || cfg.Home.Enabled || cfg.RemoteManagement.DisableControlPanel {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	writeStaticManagementAsset(
		c,
		"application/javascript; charset=utf-8",
		managementasset.LogQAScript(),
	)
}

func writeStaticManagementAsset(c *gin.Context, contentType string, payload []byte) {
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Type", contentType)
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Content-Length", strconv.Itoa(len(payload)))
	if c.Request.Method == http.MethodHead {
		c.Status(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, contentType, payload)
}
