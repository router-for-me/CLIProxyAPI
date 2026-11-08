package multiuser

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/andybalholm/brotli"
	"github.com/gin-gonic/gin"
)

//go:embed panel/management.html
var panelFiles embed.FS

type panelAsset struct {
	raw    []byte
	zipped []byte
	etag   string
	// brotli is filled in the background: maximum-quality compression of the
	// multi-MB panel takes seconds, and gzip serves until it is ready.
	brotli atomic.Pointer[[]byte]
}

var (
	panelAssetOnce sync.Once
	panelAssetVal  panelAsset
)

// loadPanelAsset reads the embedded panel once and precomputes the gzipped
// payload and a content-derived ETag. The single-file panel is several MB,
// so repeat visits should revalidate (304) instead of re-downloading, and a
// first visit over a slow uplink should transfer as few bytes as possible.
func loadPanelAsset() *panelAsset {
	panelAssetOnce.Do(func() {
		data, err := panelFiles.ReadFile("panel/management.html")
		if err != nil {
			return
		}
		sum := sha256.Sum256(data)
		var buf bytes.Buffer
		writer, errWriter := gzip.NewWriterLevel(&buf, gzip.BestCompression)
		if errWriter == nil {
			if _, errWrite := writer.Write(data); errWrite == nil {
				if errClose := writer.Close(); errClose == nil {
					panelAssetVal.zipped = buf.Bytes()
				}
			}
		}
		panelAssetVal.raw = data
		panelAssetVal.etag = `"` + hex.EncodeToString(sum[:16]) + `"`
		go func() {
			var out bytes.Buffer
			encoder := brotli.NewWriterOptions(&out, brotli.WriterOptions{Quality: 11, LGWin: 24})
			if _, errWrite := encoder.Write(data); errWrite != nil {
				return
			}
			if errClose := encoder.Close(); errClose != nil {
				return
			}
			compressed := out.Bytes()
			panelAssetVal.brotli.Store(&compressed)
		}()
	})
	return &panelAssetVal
}

// PreloadPanel starts panel compression at startup so the first visitor does
// not wait for it.
func PreloadPanel() { loadPanelAsset() }

func acceptsEncoding(header, encoding string) bool {
	for _, part := range strings.Split(header, ",") {
		name, params, _ := strings.Cut(strings.TrimSpace(part), ";")
		if strings.EqualFold(strings.TrimSpace(name), encoding) && !strings.Contains(strings.ReplaceAll(params, " ", ""), "q=0") {
			return true
		}
	}
	return false
}

func Panel(c *gin.Context) {
	asset := loadPanelAsset()
	if asset.raw == nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	// no-cache allows storing but forces revalidation; unchanged panels get a
	// cheap 304 while redeploys change the ETag and ship the new build.
	c.Header("Cache-Control", "no-cache")
	c.Header("ETag", asset.etag)
	c.Header("Vary", "Accept-Encoding")
	if strings.TrimSpace(c.GetHeader("If-None-Match")) == asset.etag {
		c.Status(http.StatusNotModified)
		return
	}
	acceptEncoding := c.GetHeader("Accept-Encoding")
	if compressed := asset.brotli.Load(); compressed != nil && acceptsEncoding(acceptEncoding, "br") {
		c.Header("Content-Encoding", "br")
		c.Data(http.StatusOK, "text/html; charset=utf-8", *compressed)
		return
	}
	if len(asset.zipped) > 0 && acceptsEncoding(acceptEncoding, "gzip") {
		c.Header("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "text/html; charset=utf-8", asset.zipped)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", asset.raw)
}
