// Package panel embeds the prox build of the CLIProxyAPI Management Center
// (web/panel) and serves it in place of the downloaded management.html.
package panel

import (
	"bytes"
	"compress/gzip"
	"embed"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// The build is stored gzipped to keep the repository and binary small.
//
//go:embed dist
var dist embed.FS

var (
	plainOnce sync.Once
	plain     []byte
)

func compressed() []byte {
	b, err := dist.ReadFile("dist/management.html.gz")
	if err != nil {
		return nil
	}
	return b
}

// HTML returns the decompressed panel, or nil when the binary was built
// without it.
func HTML() []byte {
	plainOnce.Do(func() {
		gz := compressed()
		if len(gz) == 0 {
			return
		}
		r, err := gzip.NewReader(bytes.NewReader(gz))
		if err != nil {
			log.WithError(err).Warn("prox panel: invalid embedded build")
			return
		}
		b, err := io.ReadAll(r)
		if err != nil {
			log.WithError(err).Warn("prox panel: invalid embedded build")
			return
		}
		plain = b
	})
	return plain
}

// UpstreamEnv set to "upstream" serves CPA's downloaded panel instead.
const UpstreamEnv = "PROX_PANEL"

// Serve writes the embedded panel and reports whether it did. Callers fall
// back to the upstream panel asset when it returns false.
func Serve(c *gin.Context) bool {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(UpstreamEnv)), "upstream") {
		return false
	}
	gz := compressed()
	if len(gz) == 0 {
		return false
	}
	c.Header("Cache-Control", "no-cache")
	c.Header("Vary", "Accept-Encoding")
	if strings.Contains(c.GetHeader("Accept-Encoding"), "gzip") {
		c.Header("Content-Encoding", "gzip")
		c.Data(http.StatusOK, "text/html; charset=utf-8", gz)
		return true
	}
	b := HTML()
	if len(b) == 0 {
		return false
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", b)
	return true
}
