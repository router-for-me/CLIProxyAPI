package api

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/dashboardasset"
)

// mountDashboardRoutes serves the embedded NixLLM dashboard SPA under
// /dashboard. When no dashboard build is embedded (e.g. dev mode where the
// user runs `npm run dev` on :9173), the route returns a short instruction
// page instead of a 404. The SPA is served unauthenticated so the browser
// can load the bundle before prompting for the management password; all
// data operations go through the authenticated /v0/management routes.
func (s *Server) mountDashboardRoutes() {
	if !dashboardasset.Available() {
		s.engine.GET("/dashboard", func(c *gin.Context) {
			c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(devDashboardNotice))
		})
		return
	}
	dashFS := dashboardasset.FileSystem()
	// http.FileServer serves a filesystem rooted at index.html, but the SPA is
	// mounted under /dashboard. StripPrefix removes the /dashboard prefix so a
	// request for /dashboard/assets/... resolves to assets/... in the FS root.
	assetFS := http.StripPrefix("/dashboard", http.FileServer(http.FS(dashFS)))
	// Redirect the bare /dashboard to /dashboard/ so FileServer serves the
	// directory index (index.html) with relative asset links intact.
	s.engine.GET("/dashboard", func(c *gin.Context) {
		c.Redirect(http.StatusMovedPermanently, "/dashboard/")
	})
	s.engine.GET("/dashboard/*filepath", func(c *gin.Context) {
		// Serve the requested file (index.html for the root; assets/.. for JS/CSS).
		// SPA fallback: any sub-path that is not an actual file (a client-side
		// route like /dashboard/api-keys/:id) serves index.html so React Router
		// can handle it.
		path := c.Param("filepath") // includes leading slash, e.g. "/index.html"
		trimmed := strings.TrimPrefix(path, "/")
		if trimmed == "" || trimmed == "index.html" {
			assetFS.ServeHTTP(c.Writer, c.Request)
			return
		}
		if f, err := dashFS.Open(trimmed); err == nil {
			_ = f.Close()
			assetFS.ServeHTTP(c.Writer, c.Request)
			return
		}
		// Not a real file — fall back to index.html so client-side routes work.
		idx, err := fs.ReadFile(dashFS, "index.html")
		if err != nil {
			c.Status(http.StatusNotFound)
			return
		}
		c.Data(http.StatusOK, "text/html; charset=utf-8", idx)
	})
}

const devDashboardNotice = `<!doctype html><html><head><meta charset="utf-8">
<title>NixLLM Dashboard — dev mode</title>
<style>
body{font-family:-apple-system,sans-serif;background:#0b1220;color:#e7ecf5;margin:0;padding:40px;line-height:1.6}
code{background:#13203a;padding:2px 6px;border-radius:3px;font-family:monospace}
a{color:#5eead4}
.card{max-width:620px;margin:0 auto;background:#18243d;border:1px solid #233049;border-radius:8px;padding:24px}
h1{margin-top:0;font-weight:600}
</style></head><body><div class="card">
<h1>NixLLM Dashboard — not built</h1>
<p>The dashboard SPA is not embedded in this binary. Run it in development mode:</p>
<p><code>cd web/dashboard &amp;&amp; npm install &amp;&amp; npm run dev</code></p>
<p>Then open <a href="http://127.0.0.1:9173/">http://127.0.0.1:9173/</a>.</p>
<p>For production, build the bundle and rebuild the Go server:</p>
<p><code>cd web/dashboard &amp;&amp; npm run build</code> then <code>go build ./cmd/server</code></p>
</div></body></html>`
