package api

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	log "github.com/sirupsen/logrus"
)

var errManagementWebAssetNotFound = errors.New("management web asset not found")

var managementWebContentTypes = map[string]string{
	".avif":        "image/avif",
	".css":         "text/css; charset=utf-8",
	".gif":         "image/gif",
	".html":        "text/html; charset=utf-8",
	".ico":         "image/x-icon",
	".jpeg":        "image/jpeg",
	".jpg":         "image/jpeg",
	".js":          "text/javascript; charset=utf-8",
	".json":        "application/json; charset=utf-8",
	".mjs":         "text/javascript; charset=utf-8",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".ttf":         "font/ttf",
	".wasm":        "application/wasm",
	".webmanifest": "application/manifest+json",
	".webp":        "image/webp",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
}

func (s *Server) redirectManagementWeb(c *gin.Context) {
	if !s.managementWebAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	target := "/management.html"
	if c.Request != nil && c.Request.URL != nil && c.Request.URL.RawQuery != "" {
		target += "?" + c.Request.URL.RawQuery
	}
	c.Redirect(http.StatusPermanentRedirect, target)
}

func (s *Server) serveManagementWeb(c *gin.Context) {
	requestedPath := c.Param("assetPath")
	if requestedPath == "" || requestedPath == "/" {
		// Keep the classic single-file panel at the former dashboard URL.
		s.serveManagementControlPanel(c)
		return
	}
	if requestedPath == "/legacy" || requestedPath == "/legacy/" {
		s.serveManagementControlPanel(c)
		return
	}
	s.serveManagementWebAsset(c, requestedPath)
}

func (s *Server) serveManagementDashboard(c *gin.Context) {
	if s.managementWebAvailable() {
		if webRoot, errRoot := s.managementWebRoot(); errRoot == nil {
			if _, _, errRead := readManagementWebAsset(webRoot, "index.html"); !errors.Is(errRead, errManagementWebAssetNotFound) {
				s.serveManagementWebAsset(c, "index.html")
				return
			}
		}
	}
	// Keep the upstream single-file panel usable when an external dashboard has
	// not been deployed next to the active configuration yet.
	s.serveManagementControlPanel(c)
}

func (s *Server) serveManagementWebAsset(c *gin.Context, requestedPath string) {
	if !s.managementWebAvailable() {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	relativePath, ok := normalizeManagementWebAssetPath(requestedPath)
	if !ok {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	webRoot, errRoot := s.managementWebRoot()
	if errRoot != nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}

	data, contentType, errRead := readManagementWebAsset(webRoot, relativePath)
	if errors.Is(errRead, errManagementWebAssetNotFound) && relativePath != "index.html" && path.Ext(relativePath) == "" {
		// Extensionless paths are handled by the client-side router. This also
		// keeps the previous /management/usage bookmark working.
		data, contentType, errRead = readManagementWebAsset(webRoot, "index.html")
	}
	if errRead != nil {
		if errors.Is(errRead, errManagementWebAssetNotFound) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		log.WithError(errRead).Error("failed to serve management web asset")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	setManagementWebSecurityHeaders(c)
	if c.Request != nil && c.Request.Method == http.MethodHead {
		c.Header("Content-Type", contentType)
		c.Header("Content-Length", strconv.Itoa(len(data)))
		c.Status(http.StatusOK)
		return
	}
	c.Data(http.StatusOK, contentType, data)
}

func (s *Server) managementWebAvailable() bool {
	return s != nil && s.cfg != nil && !s.cfg.Home.Enabled && !s.cfg.RemoteManagement.DisableControlPanel
}

func (s *Server) managementWebRoot() (string, error) {
	configured := ""
	if s != nil && s.cfg != nil {
		configured = strings.TrimSpace(s.cfg.RemoteManagement.WebDirectory)
	}
	if configured == "" {
		configured = config.DefaultManagementWebDirectory
	}
	configured = filepath.FromSlash(configured)
	if filepath.IsAbs(configured) {
		return filepath.Clean(configured), nil
	}
	if filepath.VolumeName(configured) != "" || (len(configured) > 0 && configured[0] == filepath.Separator) {
		return "", fmt.Errorf("management web directory must be absolute or config-relative")
	}

	baseDirectory := ""
	if s != nil && strings.TrimSpace(s.configFilePath) != "" {
		configPath := s.configFilePath
		if !filepath.IsAbs(configPath) && strings.TrimSpace(s.currentPath) != "" {
			configPath = filepath.Join(s.currentPath, configPath)
		}
		baseDirectory = filepath.Dir(configPath)
	} else if s != nil {
		baseDirectory = s.currentPath
	}
	if strings.TrimSpace(baseDirectory) == "" {
		baseDirectory = "."
	}
	root, errAbs := filepath.Abs(filepath.Join(baseDirectory, configured))
	if errAbs != nil {
		return "", fmt.Errorf("resolve management web directory: %w", errAbs)
	}
	return filepath.Clean(root), nil
}

func normalizeManagementWebAssetPath(requested string) (string, bool) {
	if strings.ContainsAny(requested, "\\:\x00") {
		return "", false
	}
	requested = strings.TrimPrefix(requested, "/")
	if requested == "" {
		return "index.html", true
	}
	cleaned := path.Clean(requested)
	if !fs.ValidPath(cleaned) {
		return "", false
	}
	return cleaned, true
}

func readManagementWebAsset(root, relativePath string) ([]byte, string, error) {
	normalizedPath, validPath := normalizeManagementWebAssetPath(relativePath)
	if !validPath || normalizedPath != relativePath {
		return nil, "", errManagementWebAssetNotFound
	}
	extension := strings.ToLower(path.Ext(relativePath))
	contentType, allowed := managementWebContentTypes[extension]
	if !allowed {
		return nil, "", errManagementWebAssetNotFound
	}

	rootHandle, errRoot := os.OpenRoot(root)
	if errRoot != nil {
		if errors.Is(errRoot, fs.ErrNotExist) {
			return nil, "", errManagementWebAssetNotFound
		}
		return nil, "", fmt.Errorf("open management web root: %w", errRoot)
	}
	assetFile, errOpen := rootHandle.Open(relativePath)
	if errOpen != nil {
		if errClose := rootHandle.Close(); errClose != nil && !errors.Is(errClose, os.ErrClosed) {
			return nil, "", fmt.Errorf("open management web asset: %w; close root: %v", errOpen, errClose)
		}
		if isManagementWebPathError(errOpen) {
			return nil, "", errManagementWebAssetNotFound
		}
		return nil, "", fmt.Errorf("open management web asset: %w", errOpen)
	}
	info, errStat := assetFile.Stat()
	if errStat != nil {
		if errClose := closeManagementWebAsset(assetFile, rootHandle); errClose != nil {
			return nil, "", fmt.Errorf("stat management web asset: %w; close asset: %v", errStat, errClose)
		}
		if isManagementWebPathError(errStat) {
			return nil, "", errManagementWebAssetNotFound
		}
		return nil, "", fmt.Errorf("stat management web asset: %w", errStat)
	}
	if !info.Mode().IsRegular() {
		if errClose := closeManagementWebAsset(assetFile, rootHandle); errClose != nil {
			return nil, "", fmt.Errorf("close management web asset: %w", errClose)
		}
		return nil, "", errManagementWebAssetNotFound
	}
	data, errRead := io.ReadAll(assetFile)
	errClose := closeManagementWebAsset(assetFile, rootHandle)
	if errRead != nil {
		if isManagementWebPathError(errRead) {
			return nil, "", errManagementWebAssetNotFound
		}
		return nil, "", fmt.Errorf("read management web asset: %w", errRead)
	}
	if errClose != nil {
		return nil, "", fmt.Errorf("close management web asset: %w", errClose)
	}
	return data, contentType, nil
}

func isManagementWebPathError(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrInvalid)
}

func closeManagementWebAsset(assetFile *os.File, rootHandle *os.Root) error {
	var errFile error
	if assetFile != nil {
		errFile = assetFile.Close()
		if errors.Is(errFile, os.ErrClosed) {
			errFile = nil
		}
	}
	var errRoot error
	if rootHandle != nil {
		errRoot = rootHandle.Close()
		if errors.Is(errRoot, os.ErrClosed) {
			errRoot = nil
		}
	}
	return errors.Join(errFile, errRoot)
}

func setManagementWebSecurityHeaders(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data: blob:; font-src 'self'; manifest-src 'self'; worker-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'")
	c.Header("Cross-Origin-Opener-Policy", "same-origin")
	c.Header("Cross-Origin-Resource-Policy", "same-origin")
	c.Header("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("X-Frame-Options", "DENY")
}
