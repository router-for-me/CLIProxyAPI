package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementasset"
	log "github.com/sirupsen/logrus"
)

func (s *Server) registerManagementRoutes() {
	if s == nil || s.engine == nil || s.mgmt == nil {
		return
	}
	if !s.managementRoutesRegistered.CompareAndSwap(false, true) {
		return
	}

	log.Info("management routes registered after secret key configuration")

	s.engine.POST("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.PostOAuthCallback)
	s.engine.GET("/v0/management/oauth-callback", s.managementAvailabilityMiddleware(), s.mgmt.GetOAuthCallback)

	mgmt := s.engine.Group("/v0/management")
	mgmt.Use(s.managementAvailabilityMiddleware(), s.mgmt.Middleware())
	{
		mgmt.GET("/config", s.mgmt.GetConfig)
		mgmt.GET("/config.yaml", s.mgmt.GetConfigYAML)
		mgmt.PUT("/config.yaml", s.mgmt.PutConfigYAML)
		mgmt.GET("/latest-version", s.mgmt.GetLatestVersion)
		mgmt.GET("/plugins", s.mgmt.ListPlugins)
		mgmt.GET("/plugin-store", s.mgmt.ListPluginStore)
		mgmt.POST("/plugin-store/:id/install", s.mgmt.InstallPluginFromStore)
		mgmt.DELETE("/plugins/:id", s.mgmt.DeletePlugin)
		mgmt.PATCH("/plugins/:id/enabled", s.mgmt.PatchPluginEnabled)
		mgmt.GET("/plugins/:id/config", s.mgmt.GetPluginConfig)
		mgmt.PUT("/plugins/:id/config", s.mgmt.PutPluginConfig)
		mgmt.PATCH("/plugins/:id/config", s.mgmt.PatchPluginConfig)

		mgmt.GET("/debug", s.mgmt.GetDebug)
		mgmt.PUT("/debug", s.mgmt.PutDebug)
		mgmt.PATCH("/debug", s.mgmt.PutDebug)

		mgmt.GET("/logging-to-file", s.mgmt.GetLoggingToFile)
		mgmt.PUT("/logging-to-file", s.mgmt.PutLoggingToFile)
		mgmt.PATCH("/logging-to-file", s.mgmt.PutLoggingToFile)

		mgmt.GET("/logs-max-total-size-mb", s.mgmt.GetLogsMaxTotalSizeMB)
		mgmt.PUT("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)
		mgmt.PATCH("/logs-max-total-size-mb", s.mgmt.PutLogsMaxTotalSizeMB)

		mgmt.GET("/error-logs-max-files", s.mgmt.GetErrorLogsMaxFiles)
		mgmt.PUT("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)
		mgmt.PATCH("/error-logs-max-files", s.mgmt.PutErrorLogsMaxFiles)

		mgmt.GET("/usage-statistics-enabled", s.mgmt.GetUsageStatisticsEnabled)
		mgmt.PUT("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)
		mgmt.PATCH("/usage-statistics-enabled", s.mgmt.PutUsageStatisticsEnabled)

		mgmt.GET("/proxy-url", s.mgmt.GetProxyURL)
		mgmt.PUT("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.PATCH("/proxy-url", s.mgmt.PutProxyURL)
		mgmt.DELETE("/proxy-url", s.mgmt.DeleteProxyURL)

		mgmt.GET("/proxy-by-provider", s.mgmt.GetProxyByProvider)
		mgmt.PUT("/proxy-by-provider/:provider", s.mgmt.PutProxyByProvider)
		mgmt.PATCH("/proxy-by-provider/:provider", s.mgmt.PutProxyByProvider)
		mgmt.DELETE("/proxy-by-provider/:provider", s.mgmt.DeleteProxyByProvider)

		mgmt.POST("/api-call", s.mgmt.APICall)

		mgmt.GET("/quota-exceeded/switch-project", s.mgmt.GetSwitchProject)
		mgmt.PUT("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)
		mgmt.PATCH("/quota-exceeded/switch-project", s.mgmt.PutSwitchProject)

		mgmt.GET("/quota-exceeded/switch-preview-model", s.mgmt.GetSwitchPreviewModel)
		mgmt.PUT("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.PATCH("/quota-exceeded/switch-preview-model", s.mgmt.PutSwitchPreviewModel)
		mgmt.POST("/reset-quota", s.mgmt.ResetQuota)

		mgmt.GET("/api-keys", s.mgmt.GetAPIKeys)
		mgmt.PUT("/api-keys", s.mgmt.PutAPIKeys)
		mgmt.PATCH("/api-keys", s.mgmt.PatchAPIKeys)
		mgmt.DELETE("/api-keys", s.mgmt.DeleteAPIKeys)
		mgmt.GET("/api-key-usage", s.mgmt.GetAPIKeyUsage)
		mgmt.GET("/usage-queue", s.mgmt.GetUsageQueue)

		mgmt.GET("/gemini-api-key", s.mgmt.GetGeminiKeys)
		mgmt.PUT("/gemini-api-key", s.mgmt.PutGeminiKeys)
		mgmt.PATCH("/gemini-api-key", s.mgmt.PatchGeminiKey)
		mgmt.DELETE("/gemini-api-key", s.mgmt.DeleteGeminiKey)

		mgmt.GET("/interactions-api-key", s.mgmt.GetInteractionsKeys)
		mgmt.PUT("/interactions-api-key", s.mgmt.PutInteractionsKeys)
		mgmt.PATCH("/interactions-api-key", s.mgmt.PatchInteractionsKey)
		mgmt.DELETE("/interactions-api-key", s.mgmt.DeleteInteractionsKey)

		mgmt.GET("/logs", s.mgmt.GetLogs)
		mgmt.DELETE("/logs", s.mgmt.DeleteLogs)
		mgmt.GET("/request-error-logs", s.mgmt.GetRequestErrorLogs)
		mgmt.GET("/request-error-logs/:name", s.mgmt.DownloadRequestErrorLog)
		mgmt.GET("/request-log-by-id/:id", s.mgmt.GetRequestLogByID)
		mgmt.GET("/request-log", s.mgmt.GetRequestLog)
		mgmt.PUT("/request-log", s.mgmt.PutRequestLog)
		mgmt.PATCH("/request-log", s.mgmt.PutRequestLog)
		mgmt.GET("/ws-auth", s.mgmt.GetWebsocketAuth)
		mgmt.PUT("/ws-auth", s.mgmt.PutWebsocketAuth)
		mgmt.PATCH("/ws-auth", s.mgmt.PutWebsocketAuth)

		mgmt.GET("/request-retry", s.mgmt.GetRequestRetry)
		mgmt.PUT("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.PATCH("/request-retry", s.mgmt.PutRequestRetry)
		mgmt.GET("/max-retry-interval", s.mgmt.GetMaxRetryInterval)
		mgmt.PUT("/max-retry-interval", s.mgmt.PutMaxRetryInterval)
		mgmt.PATCH("/max-retry-interval", s.mgmt.PutMaxRetryInterval)

		mgmt.GET("/force-model-prefix", s.mgmt.GetForceModelPrefix)
		mgmt.PUT("/force-model-prefix", s.mgmt.PutForceModelPrefix)
		mgmt.PATCH("/force-model-prefix", s.mgmt.PutForceModelPrefix)

		mgmt.GET("/routing/strategy", s.mgmt.GetRoutingStrategy)
		mgmt.PUT("/routing/strategy", s.mgmt.PutRoutingStrategy)
		mgmt.PATCH("/routing/strategy", s.mgmt.PutRoutingStrategy)

		mgmt.GET("/claude-api-key", s.mgmt.GetClaudeKeys)
		mgmt.PUT("/claude-api-key", s.mgmt.PutClaudeKeys)
		mgmt.PATCH("/claude-api-key", s.mgmt.PatchClaudeKey)
		mgmt.DELETE("/claude-api-key", s.mgmt.DeleteClaudeKey)

		mgmt.GET("/codex-api-key", s.mgmt.GetCodexKeys)
		mgmt.PUT("/codex-api-key", s.mgmt.PutCodexKeys)
		mgmt.PATCH("/codex-api-key", s.mgmt.PatchCodexKey)
		mgmt.DELETE("/codex-api-key", s.mgmt.DeleteCodexKey)

		mgmt.GET("/xai-api-key", s.mgmt.GetXAIKeys)
		mgmt.PUT("/xai-api-key", s.mgmt.PutXAIKeys)
		mgmt.PATCH("/xai-api-key", s.mgmt.PatchXAIKey)
		mgmt.DELETE("/xai-api-key", s.mgmt.DeleteXAIKey)

		mgmt.GET("/openai-compatibility", s.mgmt.GetOpenAICompat)
		mgmt.PUT("/openai-compatibility", s.mgmt.PutOpenAICompat)
		mgmt.PATCH("/openai-compatibility", s.mgmt.PatchOpenAICompat)
		mgmt.DELETE("/openai-compatibility", s.mgmt.DeleteOpenAICompat)

		mgmt.GET("/vertex-api-key", s.mgmt.GetVertexCompatKeys)
		mgmt.PUT("/vertex-api-key", s.mgmt.PutVertexCompatKeys)
		mgmt.PATCH("/vertex-api-key", s.mgmt.PatchVertexCompatKey)
		mgmt.DELETE("/vertex-api-key", s.mgmt.DeleteVertexCompatKey)

		mgmt.GET("/oauth-excluded-models", s.mgmt.GetOAuthExcludedModels)
		mgmt.PUT("/oauth-excluded-models", s.mgmt.PutOAuthExcludedModels)
		mgmt.PATCH("/oauth-excluded-models", s.mgmt.PatchOAuthExcludedModels)
		mgmt.DELETE("/oauth-excluded-models", s.mgmt.DeleteOAuthExcludedModels)

		mgmt.GET("/oauth-model-alias", s.mgmt.GetOAuthModelAlias)
		mgmt.PUT("/oauth-model-alias", s.mgmt.PutOAuthModelAlias)
		mgmt.PATCH("/oauth-model-alias", s.mgmt.PatchOAuthModelAlias)
		mgmt.DELETE("/oauth-model-alias", s.mgmt.DeleteOAuthModelAlias)

		mgmt.GET("/auth-files", s.mgmt.ListAuthFiles)
		mgmt.GET("/codex-refresh-auth-files", s.mgmt.ListCodexRefreshAuthFiles)
		mgmt.GET("/auth-files/models", s.mgmt.GetAuthFileModels)
		mgmt.GET("/model-definitions/:channel", s.mgmt.GetStaticModelDefinitions)
		mgmt.GET("/auth-files/download", s.mgmt.DownloadAuthFile)
		mgmt.POST("/auth-files", s.mgmt.UploadAuthFile)
		mgmt.DELETE("/auth-files", s.mgmt.DeleteAuthFile)
		mgmt.PATCH("/auth-files/status", s.mgmt.PatchAuthFileStatus)
		mgmt.PATCH("/auth-files/fields", s.mgmt.PatchAuthFileFields)
		mgmt.POST("/vertex/import", s.mgmt.ImportVertexCredential)

		mgmt.GET("/anthropic-auth-url", s.mgmt.RequestAnthropicToken)
		mgmt.GET("/codex-auth-url", s.mgmt.RequestCodexToken)
		mgmt.GET("/antigravity-auth-url", s.mgmt.RequestAntigravityToken)
		mgmt.GET("/kimi-auth-url", s.mgmt.RequestKimiToken)
		mgmt.GET("/xai-auth-url", s.mgmt.RequestXAIToken)
		mgmt.GET("/get-auth-status", s.mgmt.GetAuthStatus)
		mgmt.DELETE("/oauth-session", s.mgmt.CancelAuthSession)

		mgmt.POST("/codex-free-refresh", s.mgmt.RefreshCodexFreeAccounts)
		mgmt.GET("/codex-free-refresh/:taskId", s.mgmt.GetRefreshCodexFreeStatus)

		mgmt.GET("/xai-refresh-auth-files", s.mgmt.ListXAIRefreshAuthFiles)
		mgmt.POST("/xai-free-refresh", s.mgmt.RefreshXAIFreeAccounts)
		mgmt.GET("/xai-free-refresh/:taskId", s.mgmt.GetRefreshXAIFreeStatus)
	}
}

func (s *Server) managementAvailabilityMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !s.managementAvailable(c) {
			return
		}
		c.Next()
	}
}

func (s *Server) managementAvailable(c *gin.Context) bool {
	if s == nil || s.cfg == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if s.cfg.Home.Enabled {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	if !s.managementRoutesEnabled.Load() {
		c.AbortWithStatus(http.StatusNotFound)
		return false
	}
	return true
}

func (s *Server) refreshPluginManagementRoutes() {
	if s == nil || s.pluginHost == nil || s.engine == nil {
		return
	}
	s.pluginHost.RegisterManagementRoutes(context.Background(), s.registeredManagementRouteKeys())
}

// RefreshPluginManagementRoutes rebuilds plugin-owned Management API routes.
func (s *Server) RefreshPluginManagementRoutes() {
	s.refreshPluginManagementRoutes()
}

func (s *Server) registeredManagementRouteKeys() map[string]struct{} {
	out := make(map[string]struct{})
	if s == nil || s.engine == nil {
		return out
	}
	for _, route := range s.engine.Routes() {
		if strings.HasPrefix(route.Path, "/v0/management/") || route.Path == "/v0/management" {
			out[strings.ToUpper(strings.TrimSpace(route.Method))+" "+route.Path] = struct{}{}
		}
	}
	return out
}

func (s *Server) pluginManagementNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	path := c.Request.URL.Path
	if strings.HasPrefix(path, "/v0/resource/plugins/") {
		s.pluginResourceNoRoute(c)
		return
	}
	if path != "/v0/management" && !strings.HasPrefix(path, "/v0/management/") {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost == nil || s.mgmt == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !s.managementAvailable(c) {
		return
	}
	s.mgmt.Middleware()(c)
	if c.IsAborted() {
		return
	}
	if s.mgmt.ServePluginAuthURL(c) {
		c.Abort()
		return
	}
	if s.pluginHost.ServeManagementHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
}

func (s *Server) pluginResourceNoRoute(c *gin.Context) {
	if s == nil || c == nil || c.Request == nil || c.Request.URL == nil {
		if c != nil {
			c.AbortWithStatus(http.StatusNotFound)
		}
		return
	}
	if s.cfg == nil || s.cfg.Home.Enabled || s.pluginHost == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if s.pluginHost.ServeResourceHTTP(c.Writer, c.Request) {
		c.Abort()
		return
	}
	c.AbortWithStatus(http.StatusNotFound)
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

	if _, err := os.Stat(filePath); err != nil {
		if os.IsNotExist(err) {
			s.startManagementAssetSync(cfg)
			s.serveManagementLoadingBootstrap(c)
			return
		} else {
			log.WithError(err).Error("failed to stat management control panel asset")
			c.AbortWithStatus(http.StatusInternalServerError)
			return
		}
	}

	data, errRead := os.ReadFile(filePath)
	if errRead != nil {
		log.WithError(errRead).Error("failed to read management control panel asset")
		c.AbortWithStatus(http.StatusInternalServerError)
		return
	}

	codexRefreshToken := ""
	xaiRefreshToken := ""
	if s.mgmt != nil {
		codexRefreshToken = s.mgmt.SignCodexRefreshActionToken()
		xaiRefreshToken = s.mgmt.SignXAIRefreshActionToken()
		s.mgmt.TryIssueSessionCookie(c)
	}
	patched := injectModelPriceDropdownClipPatch(data)
	patched = injectCodexFreeRefreshPatch(patched, codexRefreshToken)
	patched = injectXAIRefreshPatch(patched, xaiRefreshToken)

	etag := fmt.Sprintf(`"%x"`, sha256.Sum256(patched))
	c.Header("ETag", etag)
	c.Header("Cache-Control", "no-cache")

	if match := c.GetHeader("If-None-Match"); match != "" && strings.Contains(match, etag) {
		c.AbortWithStatus(http.StatusNotModified)
		return
	}

	c.Data(http.StatusOK, "text/html; charset=utf-8", patched)
}

func (s *Server) startManagementAssetSync(cfg *config.Config) {
	if s == nil || cfg == nil || !s.managementAssetSyncing.CompareAndSwap(false, true) {
		return
	}
	configPath := s.configFilePath
	proxyURL := cfg.ProxyURL
	panelRepository := cfg.RemoteManagement.PanelGitHubRepository
	go func() {
		defer s.managementAssetSyncing.Store(false)
		// Control panel bootstrap should not be canceled by client disconnects.
		managementasset.EnsureLatestManagementHTML(context.Background(), managementasset.StaticDir(configPath), proxyURL, panelRepository)
	}()
}

func (s *Server) serveManagementLoadingBootstrap(c *gin.Context) {
	c.Header("Cache-Control", "no-store, no-cache, must-revalidate, max-age=0")
	c.Header("Pragma", "no-cache")
	c.Header("Expires", "0")
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(managementLoadingBootstrapHTML))
}

const managementLoadingBootstrapHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Management panel is loading</title>
<style>body{font-family:Arial,sans-serif;margin:0;min-height:100vh;display:flex;align-items:center;justify-content:center;background:#0f172a;color:#e2e8f0}.card{max-width:520px;padding:28px;border:1px solid rgba(148,163,184,.35);border-radius:14px;background:rgba(15,23,42,.86);box-shadow:0 24px 80px rgba(0,0,0,.35)}h1{font-size:22px;margin:0 0 12px}p{line-height:1.5;color:#cbd5e1;margin:0 0 10px}</style>
</head>
<body>
<div class="card"><h1>Management panel is loading</h1><p>The management UI is being downloaded in the background. This page will refresh automatically.</p><p>If it does not load soon, refresh this page manually.</p></div>
<script>setTimeout(function(){ location.reload(); }, 2000);</script>
</body>
</html>`

func injectModelPriceDropdownClipPatch(html []byte) []byte {
	const marker = "__cpa_model_price_dropdown_clip_patch__"
	if len(html) == 0 || bytes.Contains(html, []byte(marker)) {
		return html
	}

	patch := []byte(`<script>
(function () {
  var MARKER = "__cpa_model_price_dropdown_clip_patch__";
  if (window[MARKER]) return;
  window[MARKER] = true;

  var SECTION_ZH = "\u6a21\u578b\u4ef7\u683c\u8bbe\u7f6e";
  var SECTION_EN = "model price settings";
  var LABEL_ZH = "\u6a21\u578b\u540d\u79f0";
  var LABEL_EN = "model name";
  var SELECT_LABEL_ZH = "\u9009\u62e9\u6a21\u578b";
  var SELECT_LABEL_EN = "select model";
  var COMBO_SELECTOR = "select,[role='combobox'],input[list],button[aria-haspopup='listbox'],button[aria-expanded]";

  function normalizeText(text) {
    return (text || "").toLowerCase().replace(/\s+/g, " ").trim();
  }

  function matchesNeedle(text, needles) {
    var value = normalizeText(text);
    if (!value) return false;
    for (var i = 0; i < needles.length; i++) {
      if (value.indexOf(normalizeText(needles[i])) !== -1) return true;
    }
    return false;
  }

  function readShortText(el) {
    if (!el) return "";
    var text = (el.innerText || el.textContent || "").trim();
    if (text.length > 120) return "";
    return text;
  }

  function elementHasNeedle(el, needles) {
    return matchesNeedle(readShortText(el), needles);
  }

  function hasComboLike(el) {
    if (!el || !el.querySelector) return false;
    return !!el.querySelector(COMBO_SELECTOR);
  }

  function countCombos(el) {
    if (!el || !el.querySelectorAll) return 0;
    return el.querySelectorAll(COMBO_SELECTOR).length;
  }

  function findTextElement(needles, root) {
    var scope = root || document;
    var nodes = scope.querySelectorAll("h1,h2,h3,h4,h5,h6,label,legend,span,div,p,strong,th,td");
    for (var i = 0; i < nodes.length; i++) {
      if (elementHasNeedle(nodes[i], needles)) {
        return nodes[i];
      }
    }
    return null;
  }

  function closestContainer(node) {
    var current = node;
    for (var i = 0; i < 8 && current; i++) {
      if (current.matches && current.matches("section,article,form,fieldset,.card,.panel,[class*='card'],[class*='panel']")) {
        return current;
      }
      if (hasComboLike(current)) {
        return current;
      }
      current = current.parentElement;
    }
    return node ? node.parentElement : null;
  }

  function findModelPriceSection() {
    var sectionNeedles = [SECTION_ZH, SECTION_EN];
    var labelNeedles = [LABEL_ZH, LABEL_EN, SELECT_LABEL_ZH, SELECT_LABEL_EN];

    var heading = findTextElement(sectionNeedles, document);
    if (heading) {
      return closestContainer(heading);
    }

    var label = findTextElement(labelNeedles, document);
    if (!label) return null;
    var current = label;
    for (var i = 0; i < 8 && current; i++) {
      if (matchesNeedle(current.textContent || "", sectionNeedles)) {
        return current;
      }
      if (hasComboLike(current)) {
        return current;
      }
      current = current.parentElement;
    }
    return closestContainer(label);
  }

  function findFirstComboRow(section) {
    if (!section || !section.querySelector) return null;
    var trigger = section.querySelector(COMBO_SELECTOR);
    if (!trigger) return null;
    var candidate = trigger;
    var current = trigger.parentElement;
    for (var i = 0; i < 6 && current && current !== section; i++) {
      if (countCombos(current) !== 1) break;
      candidate = current;
      current = current.parentElement;
    }
    return candidate;
  }

  function findModelNameRow(section) {
    if (!section) return null;
    var labelNeedles = [LABEL_ZH, LABEL_EN, SELECT_LABEL_ZH, SELECT_LABEL_EN];
    var label = findTextElement(labelNeedles, section);
    if (label) {
      var current = label;
      for (var i = 0; i < 6 && current; i++) {
        if (hasComboLike(current)) {
          return current;
        }
        current = current.parentElement;
      }
      if (label.parentElement) {
        return label.parentElement;
      }
    }
    return findFirstComboRow(section);
  }

  function isVisibleCombo(node) {
    if (!node || !node.getBoundingClientRect) return false;
    var rect = node.getBoundingClientRect();
    if (!rect || rect.width === 0 || rect.height === 0) return false;
    var style = window.getComputedStyle ? window.getComputedStyle(node) : null;
    if (style && (style.display === "none" || style.visibility === "hidden")) return false;
    return true;
  }

  function findLowestVisibleCombo() {
    var combos = document.querySelectorAll(COMBO_SELECTOR);
    var best = null;
    var bestTop = -Infinity;
    for (var i = 0; i < combos.length; i++) {
      var combo = combos[i];
      if (!isVisibleCombo(combo)) continue;
      var rect = combo.getBoundingClientRect();
      if (rect.top >= bestTop) {
        bestTop = rect.top;
        best = combo;
      }
    }
    return best;
  }

  function relaxNode(node, minZIndex) {
    if (!node || !node.style || !window.getComputedStyle) return;
    var style = window.getComputedStyle(node);
    var overflow = ((style.overflow || "") + " " + (style.overflowX || "") + " " + (style.overflowY || "")).toLowerCase();
    if (overflow.indexOf("hidden") !== -1 || overflow.indexOf("clip") !== -1 || overflow.indexOf("auto") !== -1 || overflow.indexOf("scroll") !== -1) {
      node.style.setProperty("overflow", "visible", "important");
      node.style.setProperty("overflow-x", "visible", "important");
      node.style.setProperty("overflow-y", "visible", "important");
    }
    if (style.position === "static") {
      node.style.setProperty("position", "relative", "important");
    }
    if (minZIndex > 0) {
      var z = parseInt(style.zIndex, 10);
      if (style.zIndex === "auto" || isNaN(z) || z < minZIndex) {
        node.style.setProperty("z-index", String(minZIndex), "important");
      }
    }
  }

  function relaxChain(start, depth, baseZ) {
    var current = start;
    for (var i = 0; i < depth && current && current !== document.body; i++) {
      relaxNode(current, baseZ + i);
      current = current.parentElement;
    }
  }

  function isUsageRoute() {
    var hash = normalizeText(window.location.hash || "");
    return hash.indexOf("/usage") !== -1 || hash.indexOf("usage") !== -1 || hash.indexOf("\u7edf\u8ba1") !== -1;
  }

  function patchModelPriceDropdown() {
    if (!isUsageRoute()) return false;
    var section = findModelPriceSection();
    var row = null;
    if (section) {
      relaxChain(section, 10, 1200);
      row = findModelNameRow(section);
    }
    if (!row) {
      var combo = findLowestVisibleCombo();
      if (!combo) return false;
      row = combo.parentElement || combo;
      relaxChain(combo, 8, 1300);
      relaxChain(row, 6, 1310);
    }
    relaxChain(row, 6, 1300);

    var trigger = row.querySelector(COMBO_SELECTOR);
    if (trigger && trigger.style) {
      trigger.style.setProperty("position", "relative", "important");
      trigger.style.setProperty("z-index", "1400", "important");
    }
    return true;
  }

  var observer = null;
  var scheduled = false;
  var patchApplied = false;
  var lastScheduleAt = 0;
  var scheduleGapMs = 180;

  function stopObserver() {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  }

  function schedulePatch() {
    if (!isUsageRoute()) {
      stopObserver();
      return;
    }
    var now = Date.now();
    if (scheduled || now - lastScheduleAt < scheduleGapMs) return;
    scheduled = true;
		lastScheduleAt = now;
		setTimeout(function () {
			scheduled = false;
			patchApplied = patchModelPriceDropdown() || patchApplied;
		}, 30);
	}

  function setupObserver() {
    if (!isUsageRoute()) {
      stopObserver();
      return;
    }
    if (!window.MutationObserver || !document.body || observer) return;
    observer = new MutationObserver(function () {
      schedulePatch();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function handleRouteChange() {
    patchApplied = false;
    setupObserver();
    schedulePatch();
  }

  window.addEventListener("hashchange", handleRouteChange, true);
  window.addEventListener("popstate", handleRouteChange, true);
  window.addEventListener("resize", schedulePatch, true);

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () {
      handleRouteChange();
    }, { once: true });
  } else {
    handleRouteChange();
  }

	setTimeout(handleRouteChange, 300);
	setTimeout(handleRouteChange, 1200);
	setTimeout(handleRouteChange, 2500);
})();
</script>`)

	lower := bytes.ToLower(html)
	bodyClose := []byte("</body>")
	if idx := bytes.LastIndex(lower, bodyClose); idx >= 0 {
		out := make([]byte, 0, len(html)+len(patch))
		out = append(out, html[:idx]...)
		out = append(out, patch...)
		out = append(out, html[idx:]...)
		return out
	}
	return append(html, patch...)
}

func injectCodexFreeRefreshPatch(html []byte, codexRefreshToken string) []byte {
	const marker = "__cpa_codex_free_refresh_patch__"
	if len(html) == 0 || bytes.Contains(html, []byte(marker)) {
		return html
	}

	patch := []byte(`<script>
(function () {
  var MARKER = "__cpa_codex_free_refresh_patch__";
  if (window[MARKER]) return;
  window[MARKER] = true;
  var CODEX_REFRESH_TOKEN = "__CPA_CODEX_REFRESH_TOKEN__";

  function normalizeText(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function decodeRoutePart(value) {
    var text = String(value == null ? "" : value);
    try {
      return decodeURIComponent(text);
    } catch (err) {
      return text;
    }
  }

  function normalizeRouteText(value) {
    return normalizeText(decodeRoutePart(value)).replace(/[\-_]+/g, " ");
  }

  function routeHasToken(text, token) {
    if (!text || !token) return false;
    if (text.indexOf(token) === -1) return false;
    if (token.charAt(0) === "/") return true;
    var idx = text.indexOf(token);
    while (idx !== -1) {
      var before = idx === 0 ? "" : text.charAt(idx - 1);
      var after = idx + token.length >= text.length ? "" : text.charAt(idx + token.length);
      if ((!before || /[\s/#?&=.]/.test(before)) && (!after || /[\s/#?&=.]/.test(after))) return true;
      idx = text.indexOf(token, idx + token.length);
    }
    return false;
  }

  function isAuthRoute() {
    var rawLocationText = (window.location.hash || "") + " " + (window.location.pathname || "") + " " + (window.location.search || "");
    var locationText = normalizeRouteText(rawLocationText);
    var routeTokens = ["auth files", "/auth", "认证文件", "凭证"];
    for (var i = 0; i < routeTokens.length; i++) {
      if (routeHasToken(locationText, normalizeRouteText(routeTokens[i]))) return true;
    }
    return activeAuthRouteFromNavigation() || !!findAuthSection();
  }

  function activeAuthRouteFromNavigation() {
    var selectors = ["[aria-current='page']", "[aria-selected='true']", "[data-state='active']", "[role='tab']", "nav [class*='active']", "aside [class*='active']", "[role='navigation'] [class*='active']"];
    for (var i = 0; i < selectors.length; i++) {
      var nodes = document.querySelectorAll(selectors[i]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (!isLayoutChrome(node) && !(node.getAttribute && normalizeText(node.getAttribute("role")) === "tab")) continue;
        var text = normalizeRouteText((node.innerText || node.textContent || "") + " " + (node.getAttribute ? (node.getAttribute("aria-label") || node.getAttribute("title") || "") : ""));
        if (routeHasToken(text, "auth files") || routeHasToken(text, "认证文件") || routeHasToken(text, "凭证")) return true;
      }
    }
    return false;
  }

  function isLayoutChrome(node) {
    var current = node;
    for (var i = 0; i < 6 && current; i++) {
      if (current.matches && current.matches("nav,aside,header,footer,[role='navigation'],[class*='sidebar'],[class*='menu']")) return true;
      current = current.parentElement;
    }
    return false;
  }

  function findAuthSection() {
    var roots = document.querySelectorAll("main,[role='main'],[class*='content'],[class*='page']");
    var titleSelectors = "h1,h2,h3,[role='heading'],[data-page-title],[class*='title']";
    for (var r = 0; r < roots.length; r++) {
      var root = roots[r];
      if (isLayoutChrome(root)) continue;
      var titles = root.querySelectorAll(titleSelectors);
      for (var i = 0; i < titles.length; i++) {
        if (isLayoutChrome(titles[i])) continue;
        var title = normalizeText(titles[i].innerText || titles[i].textContent || "");
        if (title.indexOf("auth files") === -1 && title.indexOf("认证文件") === -1 && title.indexOf("凭证") === -1) continue;
        var container = titles[i];
        for (var depth = 0; depth < 5 && container && container !== root; depth++) {
          if (container.matches && container.matches("section,article,.card,.panel,[class*='card'],[class*='panel'],[class*='page']")) break;
          container = container.parentElement;
        }
        return container || root;
      }
    }

    var selectors = ["main [class*='auth']", "main [id*='auth']", "main section", "main .card", "main .panel", "[role='main'] [class*='auth']", "[role='main'] [id*='auth']", "[role='main'] section", "[role='main'] .card", "[role='main'] .panel", "[class*='auth']", "[id*='auth']", "section", ".card", ".panel"];
    for (var s = 0; s < selectors.length; s++) {
      var nodes = document.querySelectorAll(selectors[s]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (isLayoutChrome(node)) continue;
        var text = normalizeText(node.innerText || node.textContent || "");
        if ((text.indexOf("codex") !== -1 || text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1) && (text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1 || text.indexOf("provider") !== -1)) {
          return node;
        }
      }
    }
    return null;
  }

  function createButton() {
    var btn = document.createElement("button");
    btn.id = "codex-free-refresh-btn";
    btn.textContent = "⟳ Refresh Free Accounts";
    btn.style.cssText = "margin:8px 0;padding:6px 14px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:13px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = startRefresh;
    return btn;
  }

  function createStatusEl() {
    var el = document.createElement("div");
    el.id = "codex-free-refresh-status";
    el.style.cssText = "margin:4px 0 8px;font-size:12px;color:#94a3b8;white-space:pre-wrap;";
    return el;
  }

  function getMgmtBase() {
    var scripts = document.querySelectorAll("script[src]");
    for (var i = 0; i < scripts.length; i++) {
      var src = scripts[i].src || "";
      var idx = src.indexOf("/management.");
      if (idx !== -1) {
        return src.substring(0, src.lastIndexOf("/"));
      }
    }
    return "/v0/management";
  }

  function apiHeaders(status) {
    return {
      "Content-Type": "application/json",
      "X-Codex-Refresh-Token": CODEX_REFRESH_TOKEN
    };
  }

  function getSelectedCodexAuthIndices() {
    var checkboxes = document.querySelectorAll(".codex-refresh-checkbox:checked");
    var indices = [];
    for (var i = 0; i < checkboxes.length; i++) {
      var idx = checkboxes[i].getAttribute("data-auth-index");
      if (idx) indices.push(idx);
    }
    return indices;
  }

  function startRefresh() {
    var btn = document.getElementById("codex-free-refresh-btn");
    var status = document.getElementById("codex-free-refresh-status");
    if (!btn || !status) return;

    var selectedIndices = getSelectedCodexAuthIndices();
    var body = {};
    if (selectedIndices.length > 0) {
      body.auth_indices = selectedIndices;
    }

    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";

    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "⟳ Refresh Free Accounts";
      return;
    }

    fetch(getMgmtBase() + "/codex-free-refresh", { method: "POST", headers: headers, credentials: "same-origin", body: JSON.stringify(body) })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh Free Accounts";
          return;
        }
        if (data.total === 0) {
          status.textContent = "No free codex accounts found.";
          btn.disabled = false;
          btn.textContent = "⟳ Refresh Free Accounts";
          return;
        }
        status.textContent = "Started: " + data.total + " accounts. Polling...";
        pollStatus(data.task_id, data.total);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "⟳ Refresh Free Accounts";
      });
  }

  function pollStatus(taskId, total) {
    var btn = document.getElementById("codex-free-refresh-btn");
    var status = document.getElementById("codex-free-refresh-status");
    if (!btn || !status) return;

    var interval = setInterval(function () {
      btn = document.getElementById("codex-free-refresh-btn");
      status = document.getElementById("codex-free-refresh-status");
      if (!btn || !status) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "⟳ Refresh Free Accounts";
        return;
      }
      fetch(getMgmtBase() + "/codex-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "⟳ Refresh Free Accounts";
            return;
          }
          var lines = [];
          lines.push("Progress: " + data.done + "/" + data.total + " | Success: " + data.success + " | Failed: " + data.failed);
          if (data.results && data.results.length > 0) {
            for (var i = 0; i < data.results.length; i++) {
              var r = data.results[i];
              var icon = r.success ? "✓" : "✗";
              var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
              lines.push("  " + icon + " " + (r.email || r.name || "?") + detail);
            }
          }
          status.textContent = lines.join("\n");

          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "⟳ Refresh Free Accounts";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh Free Accounts";
        });
    }, 2000);
  }

  var authFilesCache = null;
  var authFilesCacheAt = 0;
  var authFilesPending = null;

  function normalizeValue(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function isCodexAuthFile(file) {
    if (!file) return false;
    return normalizeValue(file.provider) === "codex" || normalizeValue(file.type) === "codex";
  }

  function getAuthFiles(status) {
    var now = Date.now();
    if (authFilesCache && now - authFilesCacheAt < 10000) return Promise.resolve(authFilesCache);
    if (authFilesPending) return authFilesPending;
    authFilesPending = fetchAuthFilesAttempt(status, 0);
    return authFilesPending;
  }

  function fetchAuthFilesAttempt(status, attempt) {
    var headers = apiHeaders(status);
    if (!headers) return Promise.resolve([]);
    return fetch(getMgmtBase() + "/codex-refresh-auth-files", { headers: headers, credentials: "same-origin" })
      .then(function (r) {
        if (r.status === 401 && attempt < 8) {
          return new Promise(function (resolve) {
            setTimeout(function () { resolve(fetchAuthFilesAttempt(status, attempt + 1)); }, 500 + attempt * 250);
          });
        }
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (data) {
        var files = data && data.files && data.files.slice ? data.files : [];
        authFilesCache = files;
        authFilesCacheAt = Date.now();
        authFilesPending = null;
        return files;
      })
      .catch(function (err) {
        authFilesPending = null;
        if (status) status.textContent = "Auth files request failed: " + err;
        return authFilesCache || [];
      });
  }

  function fileMatchValues(file) {
    var keys = ["auth_index", "name", "email", "label", "account", "id", "project_id"];
    var values = [];
    for (var i = 0; i < keys.length; i++) {
      var value = normalizeValue(file && file[keys[i]]);
      if (value && value.length >= 3) values.push(value);
    }
    return values;
  }

  function isVisibleElement(el) {
    if (!el || !el.getBoundingClientRect) return false;
    var rect = el.getBoundingClientRect();
    if (!rect || rect.width === 0 || rect.height === 0) return false;
    var style = window.getComputedStyle ? window.getComputedStyle(el) : null;
    return !(style && (style.display === "none" || style.visibility === "hidden"));
  }

  function rowMatchesAuthFile(row, file) {
    if (!row || !file || !isVisibleElement(row)) return false;
    var text = normalizeValue(row.innerText || row.textContent || "");
    if (!text || text.length > 5000) return false;
    var provider = normalizeValue(file.provider || file.type);
    if (provider && text.indexOf(provider) === -1 && text.indexOf("codex") === -1) return false;
    var values = fileMatchValues(file);
    for (var i = 0; i < values.length; i++) {
      if (text.indexOf(values[i]) !== -1) return true;
    }
    return false;
  }

  function candidateAuthRows() {
    var selectors = ["tr", "[role='row']", "li", "article", "section", ".card", ".panel", "[class*='card']", "[class*='row']", "[class*='item']"];
    var seen = [];
    var rows = [];
    var section = findAuthSection();
    var scopes = section ? [section, document] : [document];
    for (var scopeIndex = 0; scopeIndex < scopes.length; scopeIndex++) {
      var scope = scopes[scopeIndex];
      for (var i = 0; i < selectors.length; i++) {
        var nodes = scope.querySelectorAll(selectors[i]);
        for (var j = 0; j < nodes.length; j++) {
          var node = nodes[j];
          if (seen.indexOf(node) !== -1 || node.id === "codex-free-refresh-wrapper") continue;
          seen.push(node);
          if (isVisibleElement(node)) rows.push(node);
        }
      }
    }
    return rows;
  }

  function findRowForAuthFile(file, rows, used) {
    var best = null;
    var bestArea = Infinity;
    for (var i = 0; i < rows.length; i++) {
      var row = rows[i];
      if (used.indexOf(row) !== -1 || !rowMatchesAuthFile(row, file)) continue;
      var rect = row.getBoundingClientRect();
      var area = rect.width * rect.height;
      if (area > 0 && area < bestArea) {
        best = row;
        bestArea = area;
      }
    }
    return best;
  }

  function singleStatusText(data) {
    if (!data) return "Waiting...";
    var lines = [];
    lines.push("Progress: " + (data.done || 0) + "/" + (data.total || 1) + " | Success: " + (data.success || 0) + " | Failed: " + (data.failed || 0));
    if (data.results && data.results.length > 0) {
      for (var i = 0; i < data.results.length; i++) {
        var r = data.results[i];
        var icon = r.success ? "✓" : "✗";
        var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
        lines.push(icon + " " + (r.email || r.name || "?") + detail);
      }
    }
    return lines.join("\n");
  }

  function pollSingleStatus(taskId, btn, status) {
    var interval = setInterval(function () {
      if (!document.body.contains(btn) || !document.body.contains(status)) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "Refresh";
        return;
      }
      fetch(getMgmtBase() + "/codex-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "Refresh";
            return;
          }
          status.textContent = singleStatusText(data);
          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "Refresh";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "Refresh";
        });
    }, 2000);
  }

  function startSingleRefresh(file, btn, status) {
    if (!file || !file.auth_index || !btn || !status) return;
    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";
    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "Refresh";
      return;
    }
    fetch(getMgmtBase() + "/codex-free-refresh", {
      method: "POST",
      headers: headers,
      credentials: "same-origin",
      body: JSON.stringify({ auth_index: file.auth_index })
    })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        if (!data.task_id) {
          status.textContent = "No refresh task was started.";
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        status.textContent = "Started. Polling...";
        pollSingleStatus(data.task_id, btn, status);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "Refresh";
      });
  }

  function createSingleRefreshButton(file, status) {
    var btn = document.createElement("button");
    btn.className = "codex-single-refresh-btn";
    btn.type = "button";
    btn.textContent = "Refresh";
    btn.style.cssText = "margin:4px;padding:3px 8px;border:1px solid #475569;border-radius:5px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;line-height:1.2;";
    btn.onmouseenter = function () { if (!btn.disabled) btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function (event) {
      if (event && event.preventDefault) event.preventDefault();
      if (event && event.stopPropagation) event.stopPropagation();
      startSingleRefresh(file, btn, status);
    };
    return btn;
  }

  function attachSingleRefresh(row, file) {
    if (!row || !file || !file.auth_index) return false;
    var existing = row.querySelector ? row.querySelector(".codex-single-refresh-wrapper[data-auth-index='" + String(file.auth_index).replace(/'/g, "\\'") + "']") : null;
    if (existing) return false;
    var holder = document.createElement("span");
    holder.className = "codex-single-refresh-wrapper";
    holder.style.cssText = "display:inline-flex;align-items:center;gap:4px;flex-wrap:wrap;margin-left:4px;";
    holder.setAttribute("data-auth-index", file.auth_index);
    var checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "codex-refresh-checkbox";
    checkbox.setAttribute("data-auth-index", file.auth_index);
    checkbox.checked = true;
    checkbox.style.cssText = "margin:0 2px;cursor:pointer;";
    var status = document.createElement("span");
    status.className = "codex-single-refresh-status";
    status.style.cssText = "font-size:11px;color:#94a3b8;white-space:pre-wrap;margin-left:2px;";
    var btn = createSingleRefreshButton(file, status);
    btn.setAttribute("data-auth-index", file.auth_index);
    holder.appendChild(checkbox);
    holder.appendChild(btn);
    holder.appendChild(status);
    if (row.tagName && row.tagName.toLowerCase() === "tr") {
      var cell = document.createElement("td");
      cell.appendChild(holder);
      row.appendChild(cell);
    } else {
      row.appendChild(holder);
    }
    return true;
  }

  function injectSingleRefreshButtons() {
    if (!isAuthRoute()) return;
    var status = document.getElementById("codex-free-refresh-status");
    getAuthFiles(status).then(function (files) {
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      var codexFiles = [];
      for (var i = 0; i < files.length; i++) {
        if (isCodexAuthFile(files[i]) && files[i].auth_index) codexFiles.push(files[i]);
      }
      if (codexFiles.length === 0) return;
      var rows = candidateAuthRows();
      var used = [];
      for (var j = 0; j < codexFiles.length; j++) {
        var row = findRowForAuthFile(codexFiles[j], rows, used);
        if (!row) continue;
        if (attachSingleRefresh(row, codexFiles[j])) used.push(row);
      }
    });
  }

  function removeInjectedUI() {
    var wrapper = document.getElementById("codex-free-refresh-wrapper");
    if (wrapper && wrapper.parentElement) wrapper.parentElement.removeChild(wrapper);
    var singles = document.querySelectorAll(".codex-single-refresh-wrapper");
    for (var i = 0; i < singles.length; i++) {
      if (singles[i].parentElement) singles[i].parentElement.removeChild(singles[i]);
    }
  }

  function createSelectAllBtn() {
    var btn = document.createElement("button");
    btn.id = "codex-select-all-btn";
    btn.textContent = "Select All";
    btn.type = "button";
    btn.style.cssText = "margin:8px 0 8px 6px;padding:6px 10px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function () {
      var checkboxes = document.querySelectorAll(".codex-refresh-checkbox");
      var allChecked = true;
      for (var i = 0; i < checkboxes.length; i++) {
        if (!checkboxes[i].checked) { allChecked = false; break; }
      }
      for (var j = 0; j < checkboxes.length; j++) {
        checkboxes[j].checked = !allChecked;
      }
      btn.textContent = allChecked ? "Select All" : "Deselect All";
    };
    return btn;
  }

  function injectUI() {
    if (!isAuthRoute()) { removeInjectedUI(); return; }
    if (document.getElementById("codex-free-refresh-btn")) return;

    var section = findAuthSection();
    var target = section || document.body;
    if (!target) return;

    var wrapper = document.createElement("div");
    wrapper.id = "codex-free-refresh-wrapper";
    wrapper.style.cssText = "display:flex;align-items:center;flex-wrap:wrap;gap:4px;";
    wrapper.appendChild(createButton());
    wrapper.appendChild(createSelectAllBtn());
    var statusEl = createStatusEl();
    statusEl.style.marginTop = "4px";
    wrapper.appendChild(statusEl);
    target.insertBefore(wrapper, target.firstChild);
    injectSingleRefreshButtons();
  }

  var observer = null;
  var scheduled = false;

  function stopObserver() {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  }

  function scheduleAuthPatch() {
    if (scheduled) return;
    scheduled = true;
    setTimeout(function () {
      scheduled = false;
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      injectUI();
      injectSingleRefreshButtons();
    }, 80);
  }

  function setupObserver() {
    if (observer || !window.MutationObserver || !document.body) return;
    observer = new MutationObserver(function () {
      scheduleAuthPatch();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function handleRouteChange() {
    setupObserver();
    scheduleAuthPatch();
    setTimeout(scheduleAuthPatch, 300);
    setTimeout(scheduleAuthPatch, 1200);
    setTimeout(scheduleAuthPatch, 2500);
  }

  window.addEventListener("hashchange", handleRouteChange, true);
  window.addEventListener("popstate", handleRouteChange, true);
  if (window.history) {
    ["pushState", "replaceState"].forEach(function (name) {
      var original = window.history[name];
      if (typeof original !== "function") return;
      window.history[name] = function () {
        var result = original.apply(this, arguments);
        setTimeout(handleRouteChange, 0);
        return result;
      };
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { handleRouteChange(); }, { once: true });
  } else {
    handleRouteChange();
  }
})();
</script>`)

	patch = bytes.ReplaceAll(patch, []byte("__CPA_CODEX_REFRESH_TOKEN__"), []byte(codexRefreshToken))

	lower := bytes.ToLower(html)
	bodyClose := []byte("</body>")
	if idx := bytes.LastIndex(lower, bodyClose); idx >= 0 {
		out := make([]byte, 0, len(html)+len(patch))
		out = append(out, html[:idx]...)
		out = append(out, patch...)
		out = append(out, html[idx:]...)
		return out
	}
	return append(html, patch...)
}

func injectXAIRefreshPatch(html []byte, xaiRefreshToken string) []byte {
	const marker = "__cpa_xai_refresh_patch__"
	if len(html) == 0 || bytes.Contains(html, []byte(marker)) {
		return html
	}

	patch := []byte(`<script>
(function () {
  var MARKER = "__cpa_xai_refresh_patch__";
  if (window[MARKER]) return;
  window[MARKER] = true;
  var XAI_REFRESH_TOKEN = "__CPA_XAI_REFRESH_TOKEN__";

  function normalizeText(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function decodeRoutePart(value) {
    var text = String(value == null ? "" : value);
    try {
      return decodeURIComponent(text);
    } catch (err) {
      return text;
    }
  }

  function normalizeRouteText(value) {
    return normalizeText(decodeRoutePart(value)).replace(/[\-_]+/g, " ");
  }

  function routeHasToken(text, token) {
    if (!text || !token) return false;
    if (text.indexOf(token) === -1) return false;
    if (token.charAt(0) === "/") return true;
    var idx = text.indexOf(token);
    while (idx !== -1) {
      var before = idx === 0 ? "" : text.charAt(idx - 1);
      var after = idx + token.length >= text.length ? "" : text.charAt(idx + token.length);
      if ((!before || /[\s/#?&=.]/.test(before)) && (!after || /[\s/#?&=.]/.test(after))) return true;
      idx = text.indexOf(token, idx + token.length);
    }
    return false;
  }

  function isAuthRoute() {
    var rawLocationText = (window.location.hash || "") + " " + (window.location.pathname || "") + " " + (window.location.search || "");
    var locationText = normalizeRouteText(rawLocationText);
    var routeTokens = ["auth files", "/auth", "认证文件", "凭证"];
    for (var i = 0; i < routeTokens.length; i++) {
      if (routeHasToken(locationText, normalizeRouteText(routeTokens[i]))) return true;
    }
    return activeAuthRouteFromNavigation() || !!findAuthSection();
  }

  function activeAuthRouteFromNavigation() {
    var selectors = ["[aria-current='page']", "[aria-selected='true']", "[data-state='active']", "[role='tab']", "nav [class*='active']", "aside [class*='active']", "[role='navigation'] [class*='active']"];
    for (var i = 0; i < selectors.length; i++) {
      var nodes = document.querySelectorAll(selectors[i]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (!isLayoutChrome(node) && !(node.getAttribute && normalizeText(node.getAttribute("role")) === "tab")) continue;
        var text = normalizeRouteText((node.innerText || node.textContent || "") + " " + (node.getAttribute ? (node.getAttribute("aria-label") || node.getAttribute("title") || "") : ""));
        if (routeHasToken(text, "auth files") || routeHasToken(text, "认证文件") || routeHasToken(text, "凭证")) return true;
      }
    }
    return false;
  }

  function isLayoutChrome(node) {
    var current = node;
    for (var i = 0; i < 6 && current; i++) {
      if (current.matches && current.matches("nav,aside,header,footer,[role='navigation'],[class*='sidebar'],[class*='menu']")) return true;
      current = current.parentElement;
    }
    return false;
  }

  function findAuthSection() {
    var roots = document.querySelectorAll("main,[role='main'],[class*='content'],[class*='page']");
    var titleSelectors = "h1,h2,h3,[role='heading'],[data-page-title],[class*='title']";
    for (var r = 0; r < roots.length; r++) {
      var root = roots[r];
      if (isLayoutChrome(root)) continue;
      var titles = root.querySelectorAll(titleSelectors);
      for (var i = 0; i < titles.length; i++) {
        if (isLayoutChrome(titles[i])) continue;
        var title = normalizeText(titles[i].innerText || titles[i].textContent || "");
        if (title.indexOf("auth files") === -1 && title.indexOf("认证文件") === -1 && title.indexOf("凭证") === -1) continue;
        var container = titles[i];
        for (var depth = 0; depth < 5 && container && container !== root; depth++) {
          if (container.matches && container.matches("section,article,.card,.panel,[class*='card'],[class*='panel'],[class*='page']")) break;
          container = container.parentElement;
        }
        return container || root;
      }
    }

    var selectors = ["main [class*='auth']", "main [id*='auth']", "main section", "main .card", "main .panel", "[role='main'] [class*='auth']", "[role='main'] [id*='auth']", "[role='main'] section", "[role='main'] .card", "[role='main'] .panel", "[class*='auth']", "[id*='auth']", "section", ".card", ".panel"];
    for (var s = 0; s < selectors.length; s++) {
      var nodes = document.querySelectorAll(selectors[s]);
      for (var j = 0; j < nodes.length; j++) {
        var node = nodes[j];
        if (isLayoutChrome(node)) continue;
        var text = normalizeText(node.innerText || node.textContent || "");
        if ((text.indexOf("xai") !== -1 || text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1) && (text.indexOf("auth") !== -1 || text.indexOf("认证文件") !== -1 || text.indexOf("凭证") !== -1 || text.indexOf("provider") !== -1)) {
          return node;
        }
      }
    }
    return null;
  }

  function createButton() {
    var btn = document.createElement("button");
    btn.id = "xai-free-refresh-btn";
    btn.textContent = "⟳ Refresh xAI Accounts";
    btn.style.cssText = "margin:8px 0;padding:6px 14px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:13px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = startRefresh;
    return btn;
  }

  function createStatusEl() {
    var el = document.createElement("div");
    el.id = "xai-free-refresh-status";
    el.style.cssText = "margin:4px 0 8px;font-size:12px;color:#94a3b8;white-space:pre-wrap;";
    return el;
  }

  function getMgmtBase() {
    var scripts = document.querySelectorAll("script[src]");
    for (var i = 0; i < scripts.length; i++) {
      var src = scripts[i].src || "";
      var idx = src.indexOf("/management.");
      if (idx !== -1) {
        return src.substring(0, src.lastIndexOf("/"));
      }
    }
    return "/v0/management";
  }

  function apiHeaders(status) {
    return {
      "Content-Type": "application/json",
      "X-XAI-Refresh-Token": XAI_REFRESH_TOKEN
    };
  }

  function getSelectedXAIAuthIndices() {
    var checkboxes = document.querySelectorAll(".xai-refresh-checkbox:checked");
    var indices = [];
    for (var i = 0; i < checkboxes.length; i++) {
      var idx = checkboxes[i].getAttribute("data-auth-index");
      if (idx) indices.push(idx);
    }
    return indices;
  }

  function startRefresh() {
    var btn = document.getElementById("xai-free-refresh-btn");
    var status = document.getElementById("xai-free-refresh-status");
    if (!btn || !status) return;

    var selectedIndices = getSelectedXAIAuthIndices();
    var body = {};
    if (selectedIndices.length > 0) {
      body.auth_indices = selectedIndices;
    }

    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";

    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "⟳ Refresh xAI Accounts";
      return;
    }

    fetch(getMgmtBase() + "/xai-free-refresh", { method: "POST", headers: headers, credentials: "same-origin", body: JSON.stringify(body) })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh xAI Accounts";
          return;
        }
        if (data.total === 0) {
          status.textContent = "No xAI accounts found.";
          btn.disabled = false;
          btn.textContent = "⟳ Refresh xAI Accounts";
          return;
        }
        status.textContent = "Started: " + data.total + " accounts. Polling...";
        pollStatus(data.task_id, data.total);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "⟳ Refresh xAI Accounts";
      });
  }

  function pollStatus(taskId, total) {
    var btn = document.getElementById("xai-free-refresh-btn");
    var status = document.getElementById("xai-free-refresh-status");
    if (!btn || !status) return;

    var interval = setInterval(function () {
      btn = document.getElementById("xai-free-refresh-btn");
      status = document.getElementById("xai-free-refresh-status");
      if (!btn || !status) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "⟳ Refresh xAI Accounts";
        return;
      }
      fetch(getMgmtBase() + "/xai-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "⟳ Refresh xAI Accounts";
            return;
          }
          var lines = [];
          lines.push("Progress: " + data.done + "/" + data.total + " | Success: " + data.success + " | Failed: " + data.failed);
          if (data.results && data.results.length > 0) {
            for (var i = 0; i < data.results.length; i++) {
              var r = data.results[i];
              var icon = r.success ? "✓" : "✗";
              var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
              lines.push("  " + icon + " " + (r.email || r.name || "?") + detail);
              // Sync result to the corresponding card's status element.
              var cardStatus = findCardStatusForResult(r);
              if (cardStatus) {
                cardStatus.textContent = r.success ? "✓ Refreshed" : "✗ " + (r.error || "failed");
                cardStatus.style.color = r.success ? "#4ade80" : "#f87171";
                var wrapper = cardStatus.parentElement;
                if (wrapper) {
                  var cardBtn = wrapper.querySelector(".xai-single-refresh-btn");
                  if (cardBtn) {
                    cardBtn.disabled = false;
                    cardBtn.textContent = "Refresh";
                  }
                }
              }
            }
          }
          status.textContent = lines.join("\n");

          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "⟳ Refresh xAI Accounts";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "⟳ Refresh xAI Accounts";
        });
    }, 2000);
  }

  var authFilesCache = null;
  var authFilesCacheAt = 0;
  var authFilesPending = null;
  var xaiAuthIndexByName = {};

  function normalizeValue(value) {
    return String(value == null ? "" : value).toLowerCase().replace(/\s+/g, " ").trim();
  }

  function isXAIAuthFile(file) {
    if (!file) return false;
    return normalizeValue(file.provider) === "xai" || normalizeValue(file.type) === "xai";
  }

  function buildXAIAuthIndexMap(files) {
    xaiAuthIndexByName = {};
    for (var i = 0; i < files.length; i++) {
      var f = files[i];
      if (!f || !f.auth_index) continue;
      if (f.name) xaiAuthIndexByName[normalizeValue(f.name)] = f.auth_index;
      if (f.email) xaiAuthIndexByName[normalizeValue(f.email)] = f.auth_index;
      if (f.label) xaiAuthIndexByName[normalizeValue(f.label)] = f.auth_index;
    }
  }

  function findCardStatusForResult(result) {
    if (!result) return null;
    var candidates = [result.name, result.email];
    for (var i = 0; i < candidates.length; i++) {
      var key = normalizeValue(candidates[i]);
      if (!key) continue;
      var authIndex = xaiAuthIndexByName[key];
      if (!authIndex) continue;
      var el = document.querySelector(".xai-single-refresh-status[data-auth-index='" + String(authIndex).replace(/'/g, "\\'") + "']");
      if (el) return el;
      var wrapper = document.querySelector(".xai-single-refresh-wrapper[data-auth-index='" + String(authIndex).replace(/'/g, "\\'") + "']");
      if (wrapper) {
        var span = wrapper.querySelector(".xai-single-refresh-status");
        if (span) return span;
      }
    }
    return null;
  }

  function getAuthFiles(status) {
    var now = Date.now();
    if (authFilesCache && now - authFilesCacheAt < 10000) return Promise.resolve(authFilesCache);
    if (authFilesPending) return authFilesPending;
    authFilesPending = fetchAuthFilesAttempt(status, 0);
    return authFilesPending;
  }

  function fetchAuthFilesAttempt(status, attempt) {
    var headers = apiHeaders(status);
    if (!headers) return Promise.resolve([]);
    return fetch(getMgmtBase() + "/xai-refresh-auth-files", { headers: headers, credentials: "same-origin" })
      .then(function (r) {
        if (r.status === 401 && attempt < 8) {
          return new Promise(function (resolve) {
            setTimeout(function () { resolve(fetchAuthFilesAttempt(status, attempt + 1)); }, 500 + attempt * 250);
          });
        }
        if (!r.ok) throw new Error("HTTP " + r.status);
        return r.json();
      })
      .then(function (data) {
        var files = data && data.files && data.files.slice ? data.files : [];
        authFilesCache = files;
        authFilesCacheAt = Date.now();
        authFilesPending = null;
        return files;
      })
      .catch(function (err) {
        authFilesPending = null;
        if (status) status.textContent = "Auth files request failed: " + err;
        return authFilesCache || [];
      });
  }

  function fileMatchValues(file) {
    var keys = ["auth_index", "name", "email", "label", "account", "id", "project_id"];
    var values = [];
    for (var i = 0; i < keys.length; i++) {
      var value = normalizeValue(file && file[keys[i]]);
      if (value && value.length >= 3) values.push(value);
    }
    return values;
  }

  function isVisibleElement(el) {
    if (!el || !el.getBoundingClientRect) return false;
    var rect = el.getBoundingClientRect();
    if (!rect || rect.width === 0 || rect.height === 0) return false;
    var style = window.getComputedStyle ? window.getComputedStyle(el) : null;
    return !(style && (style.display === "none" || style.visibility === "hidden"));
  }

  function rowMatchesAuthFile(row, file) {
    if (!row || !file || !isVisibleElement(row)) return false;
    var text = normalizeValue(row.innerText || row.textContent || "");
    if (!text || text.length > 5000) return false;
    var provider = normalizeValue(file.provider || file.type);
    if (provider && text.indexOf(provider) === -1 && text.indexOf("xai") === -1) return false;
    var values = fileMatchValues(file);
    for (var i = 0; i < values.length; i++) {
      if (text.indexOf(values[i]) !== -1) return true;
    }
    return false;
  }

  function candidateAuthRows() {
    var selectors = ["tr", "[role='row']", "li", "article", "section", ".card", ".panel", "[class*='card']", "[class*='row']", "[class*='item']"];
    var seen = [];
    var rows = [];
    var section = findAuthSection();
    var scopes = section ? [section, document] : [document];
    for (var scopeIndex = 0; scopeIndex < scopes.length; scopeIndex++) {
      var scope = scopes[scopeIndex];
      for (var i = 0; i < selectors.length; i++) {
        var nodes = scope.querySelectorAll(selectors[i]);
        for (var j = 0; j < nodes.length; j++) {
          var node = nodes[j];
          if (seen.indexOf(node) !== -1 || node.id === "xai-free-refresh-wrapper") continue;
          seen.push(node);
          if (isVisibleElement(node)) rows.push(node);
        }
      }
    }
    return rows;
  }

  function findRowForAuthFile(file, rows, used) {
    var best = null;
    var bestArea = Infinity;
    for (var i = 0; i < rows.length; i++) {
      var row = rows[i];
      if (used.indexOf(row) !== -1 || !rowMatchesAuthFile(row, file)) continue;
      var rect = row.getBoundingClientRect();
      var area = rect.width * rect.height;
      if (area > 0 && area < bestArea) {
        best = row;
        bestArea = area;
      }
    }
    return best;
  }

  function singleStatusText(data) {
    if (!data) return "Waiting...";
    var lines = [];
    lines.push("Progress: " + (data.done || 0) + "/" + (data.total || 1) + " | Success: " + (data.success || 0) + " | Failed: " + (data.failed || 0));
    if (data.results && data.results.length > 0) {
      for (var i = 0; i < data.results.length; i++) {
        var r = data.results[i];
        var icon = r.success ? "✓" : "✗";
        var detail = r.success ? "" : " (" + (r.error || "unknown") + ")";
        lines.push(icon + " " + (r.email || r.name || "?") + detail);
      }
    }
    return lines.join("\n");
  }

  function pollSingleStatus(taskId, btn, status) {
    var interval = setInterval(function () {
      if (!document.body.contains(btn) || !document.body.contains(status)) {
        clearInterval(interval);
        return;
      }
      var headers = apiHeaders(status);
      if (!headers) {
        clearInterval(interval);
        btn.disabled = false;
        btn.textContent = "Refresh";
        return;
      }
      fetch(getMgmtBase() + "/xai-free-refresh/" + encodeURIComponent(taskId), { headers: headers, credentials: "same-origin" })
        .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
        .then(function (data) {
          if (data.error) {
            clearInterval(interval);
            status.textContent = "Error: " + data.error;
            btn.disabled = false;
            btn.textContent = "Refresh";
            return;
          }
          status.textContent = singleStatusText(data);
          if (data.status === "completed") {
            clearInterval(interval);
            btn.disabled = false;
            btn.textContent = "Refresh";
          }
        })
        .catch(function (err) {
          clearInterval(interval);
          status.textContent = "Poll failed: " + err;
          btn.disabled = false;
          btn.textContent = "Refresh";
        });
    }, 2000);
  }

  function startSingleRefresh(file, btn, status) {
    if (!file || !file.auth_index || !btn || !status) return;
    btn.disabled = true;
    btn.textContent = "Starting...";
    status.textContent = "";
    var headers = apiHeaders(status);
    if (!headers) {
      btn.disabled = false;
      btn.textContent = "Refresh";
      return;
    }
    fetch(getMgmtBase() + "/xai-free-refresh", {
      method: "POST",
      headers: headers,
      credentials: "same-origin",
      body: JSON.stringify({ auth_index: file.auth_index })
    })
      .then(function (r) {
          if (!r.ok) throw new Error("HTTP " + r.status);
          return r.json();
        })
      .then(function (data) {
        if (data.error) {
          status.textContent = "Error: " + data.error;
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        if (!data.task_id) {
          status.textContent = "No refresh task was started.";
          btn.disabled = false;
          btn.textContent = "Refresh";
          return;
        }
        status.textContent = "Started. Polling...";
        pollSingleStatus(data.task_id, btn, status);
      })
      .catch(function (err) {
        status.textContent = "Request failed: " + err;
        btn.disabled = false;
        btn.textContent = "Refresh";
      });
  }

  function createSingleRefreshButton(file, status) {
    var btn = document.createElement("button");
    btn.className = "xai-single-refresh-btn";
    btn.type = "button";
    btn.textContent = "Refresh";
    btn.style.cssText = "margin:4px;padding:3px 8px;border:1px solid #475569;border-radius:5px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;line-height:1.2;";
    btn.onmouseenter = function () { if (!btn.disabled) btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function (event) {
      if (event && event.preventDefault) event.preventDefault();
      if (event && event.stopPropagation) event.stopPropagation();
      startSingleRefresh(file, btn, status);
    };
    return btn;
  }

  function attachSingleRefresh(row, file) {
    if (!row || !file || !file.auth_index) return false;
    var existing = row.querySelector ? row.querySelector(".xai-single-refresh-wrapper[data-auth-index='" + String(file.auth_index).replace(/'/g, "\\'") + "']") : null;
    if (existing) return false;
    var holder = document.createElement("span");
    holder.className = "xai-single-refresh-wrapper";
    holder.style.cssText = "display:inline-flex;align-items:center;gap:4px;flex-wrap:wrap;margin-left:4px;";
    holder.setAttribute("data-auth-index", file.auth_index);
    var checkbox = document.createElement("input");
    checkbox.type = "checkbox";
    checkbox.className = "xai-refresh-checkbox";
    checkbox.setAttribute("data-auth-index", file.auth_index);
    checkbox.checked = true;
    checkbox.style.cssText = "margin:0 2px;cursor:pointer;";
    var status = document.createElement("span");
    status.className = "xai-single-refresh-status";
    status.style.cssText = "font-size:11px;color:#94a3b8;white-space:pre-wrap;margin-left:2px;";
    var btn = createSingleRefreshButton(file, status);
    btn.setAttribute("data-auth-index", file.auth_index);
    holder.appendChild(checkbox);
    holder.appendChild(btn);
    holder.appendChild(status);
    if (row.tagName && row.tagName.toLowerCase() === "tr") {
      var cell = document.createElement("td");
      cell.appendChild(holder);
      row.appendChild(cell);
    } else {
      row.appendChild(holder);
    }
    return true;
  }

  function injectSingleRefreshButtons() {
    if (!isAuthRoute()) return;
    var status = document.getElementById("xai-free-refresh-status");
    getAuthFiles(status).then(function (files) {
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      var xaiFiles = [];
      for (var i = 0; i < files.length; i++) {
        if (isXAIAuthFile(files[i]) && files[i].auth_index) xaiFiles.push(files[i]);
      }
      if (xaiFiles.length === 0) return;
      buildXAIAuthIndexMap(xaiFiles);
      var rows = candidateAuthRows();
      var used = [];
      for (var j = 0; j < xaiFiles.length; j++) {
        var row = findRowForAuthFile(xaiFiles[j], rows, used);
        if (!row) continue;
        if (attachSingleRefresh(row, xaiFiles[j])) used.push(row);
      }
    });
  }

  function removeInjectedUI() {
    var wrapper = document.getElementById("xai-free-refresh-wrapper");
    if (wrapper && wrapper.parentElement) wrapper.parentElement.removeChild(wrapper);
    var singles = document.querySelectorAll(".xai-single-refresh-wrapper");
    for (var i = 0; i < singles.length; i++) {
      if (singles[i].parentElement) singles[i].parentElement.removeChild(singles[i]);
    }
  }

  function createSelectAllBtn() {
    var btn = document.createElement("button");
    btn.id = "xai-select-all-btn";
    btn.textContent = "Select All";
    btn.type = "button";
    btn.style.cssText = "margin:8px 0 8px 6px;padding:6px 10px;border:1px solid #475569;border-radius:6px;background:#1e293b;color:#e2e8f0;cursor:pointer;font-size:12px;";
    btn.onmouseenter = function () { btn.style.background = "#334155"; };
    btn.onmouseleave = function () { btn.style.background = "#1e293b"; };
    btn.onclick = function () {
      var checkboxes = document.querySelectorAll(".xai-refresh-checkbox");
      var allChecked = true;
      for (var i = 0; i < checkboxes.length; i++) {
        if (!checkboxes[i].checked) { allChecked = false; break; }
      }
      for (var j = 0; j < checkboxes.length; j++) {
        checkboxes[j].checked = !allChecked;
      }
      btn.textContent = allChecked ? "Select All" : "Deselect All";
    };
    return btn;
  }

  function injectUI() {
    if (!isAuthRoute()) { removeInjectedUI(); return; }
    if (document.getElementById("xai-free-refresh-btn")) return;

    var section = findAuthSection();
    var target = section || document.body;
    if (!target) return;

    var wrapper = document.createElement("div");
    wrapper.id = "xai-free-refresh-wrapper";
    wrapper.style.cssText = "display:flex;align-items:center;flex-wrap:wrap;gap:4px;";
    wrapper.appendChild(createButton());
    wrapper.appendChild(createSelectAllBtn());
    var statusEl = createStatusEl();
    statusEl.style.marginTop = "4px";
    wrapper.appendChild(statusEl);
    target.insertBefore(wrapper, target.firstChild);
    injectSingleRefreshButtons();
  }

  var observer = null;
  var scheduled = false;

  function stopObserver() {
    if (!observer) return;
    observer.disconnect();
    observer = null;
  }

  function scheduleAuthPatch() {
    if (scheduled) return;
    scheduled = true;
    setTimeout(function () {
      scheduled = false;
      if (!isAuthRoute()) { removeInjectedUI(); return; }
      injectUI();
      injectSingleRefreshButtons();
    }, 80);
  }

  function setupObserver() {
    if (observer || !window.MutationObserver || !document.body) return;
    observer = new MutationObserver(function () {
      scheduleAuthPatch();
    });
    observer.observe(document.body, { childList: true, subtree: true });
  }

  function handleRouteChange() {
    setupObserver();
    scheduleAuthPatch();
    setTimeout(scheduleAuthPatch, 300);
    setTimeout(scheduleAuthPatch, 1200);
    setTimeout(scheduleAuthPatch, 2500);
  }

  window.addEventListener("hashchange", handleRouteChange, true);
  window.addEventListener("popstate", handleRouteChange, true);

  if (window.history) {
    ["pushState", "replaceState"].forEach(function (name) {
      var original = window.history[name];
      if (typeof original !== "function") return;
      window.history[name] = function () {
        var result = original.apply(this, arguments);
        setTimeout(handleRouteChange, 0);
        return result;
      };
    });
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", function () { handleRouteChange(); }, { once: true });
  } else {
    handleRouteChange();
  }
})();
</script>`)

	patch = bytes.ReplaceAll(patch, []byte("__CPA_XAI_REFRESH_TOKEN__"), []byte(xaiRefreshToken))

	lower := bytes.ToLower(html)
	bodyClose := []byte("</body>")
	if idx := bytes.LastIndex(lower, bodyClose); idx >= 0 {
		out := make([]byte, 0, len(html)+len(patch))
		out = append(out, html[:idx]...)
		out = append(out, patch...)
		out = append(out, html[idx:]...)
		return out
	}
	return append(html, patch...)
}
