package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

const managementCodexWidgetScriptPath = "/management-codex-widget.js"

const managementCodexWidgetJS = `(function () {
  var ROOT_ID = "cpa-codex-usage-widget";
  var REFRESH_MS = 30000;

  function managementKey() {
    var keys = ["management_key", "managementKey", "MANAGEMENT_KEY"];
    for (var i = 0; i < keys.length; i++) {
      var value = "";
      try {
        value = (window.localStorage && localStorage.getItem(keys[i])) || "";
      } catch (_) {}
      if (value) return value;
    }
    return "";
  }

  function headers() {
    var out = { Accept: "application/json" };
    var key = managementKey();
    if (key) out.Authorization = "Bearer " + key;
    return out;
  }

  function escapeHtml(input) {
    return String(input || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function formatTime(ts) {
    if (!ts) return "-";
    try {
      var d = new Date(ts);
      if (Number.isNaN(d.getTime())) return "-";
      return d.toLocaleString();
    } catch (_) {
      return "-";
    }
  }

  function formatWindow(windowObj) {
    if (!windowObj) return "-";
    var used = typeof windowObj.used_percent === "number" ? windowObj.used_percent : "-";
    var reset = typeof windowObj.reset_after_seconds === "number" && windowObj.reset_after_seconds > 0
      ? windowObj.reset_after_seconds + "s"
      : "-";
    return used + "% (reset " + reset + ")";
  }

  function ensureRoot() {
    var existing = document.getElementById(ROOT_ID);
    if (existing) return existing;
    var root = document.createElement("section");
    root.id = ROOT_ID;
    root.innerHTML =
      "<style>" +
      "#" + ROOT_ID + "{position:fixed;right:16px;bottom:16px;z-index:2147483000;width:min(560px,92vw);max-height:72vh;overflow:auto;background:#111827;color:#f9fafb;border:1px solid #374151;border-radius:12px;box-shadow:0 12px 28px rgba(0,0,0,.35);font-family:ui-sans-serif,system-ui,-apple-system,Segoe UI,Roboto,sans-serif}" +
      "#" + ROOT_ID + " .head{padding:10px 12px;border-bottom:1px solid #374151;font-size:14px;font-weight:700;display:flex;justify-content:space-between;align-items:center}" +
      "#" + ROOT_ID + " .meta{font-size:12px;color:#9ca3af;font-weight:500}" +
      "#" + ROOT_ID + " .body{padding:10px 12px;display:flex;flex-direction:column;gap:10px}" +
      "#" + ROOT_ID + " .total{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:8px}" +
      "#" + ROOT_ID + " .card{background:#1f2937;border:1px solid #374151;border-radius:8px;padding:8px}" +
      "#" + ROOT_ID + " .label{font-size:12px;color:#9ca3af}" +
      "#" + ROOT_ID + " .value{font-size:14px;font-weight:700;color:#f3f4f6}" +
      "#" + ROOT_ID + " .auth{border:1px solid #374151;background:#0f172a;border-radius:8px;padding:8px;display:flex;flex-direction:column;gap:6px}" +
      "#" + ROOT_ID + " .row{display:flex;justify-content:space-between;gap:8px;align-items:center;flex-wrap:wrap}" +
      "#" + ROOT_ID + " button{border:1px solid #4b5563;background:#1f2937;color:#f9fafb;border-radius:6px;padding:4px 8px;font-size:12px;cursor:pointer}" +
      "#" + ROOT_ID + " button:hover{background:#374151}" +
      "#" + ROOT_ID + " .err{color:#fca5a5;font-size:12px}" +
      "</style>" +
      "<div class='head'><span>Codex Usage Cache</span><span class='meta' id='" + ROOT_ID + "-time'>-</span></div>" +
      "<div class='body' id='" + ROOT_ID + "-body'>加载中...</div>";
    document.body.appendChild(root);
    return root;
  }

  function downloadCliAuth(name) {
    var query = encodeURIComponent(name || "");
    return fetch("/v0/management/codex-cli-oauth-file?name=" + query, { headers: headers() })
      .then(function (resp) {
        if (!resp.ok) return resp.text().then(function (txt) { throw new Error(txt || ("HTTP " + resp.status)); });
        var disposition = resp.headers.get("Content-Disposition") || "";
        var match = /filename="([^"]+)"/.exec(disposition);
        var filename = match && match[1] ? match[1] : "auth.json";
        return resp.blob().then(function (blob) {
          var url = URL.createObjectURL(blob);
          var a = document.createElement("a");
          a.href = url;
          a.download = filename;
          document.body.appendChild(a);
          a.click();
          a.remove();
          URL.revokeObjectURL(url);
        });
      });
  }

  function render(summary) {
    var body = document.getElementById(ROOT_ID + "-body");
    var time = document.getElementById(ROOT_ID + "-time");
    if (!body || !time) return;

    var total = (summary && summary.total) || {};
    var primary = total.primary_window || null;
    var secondary = total.secondary_window || null;
    var authFiles = (summary && summary.auth_files) || [];

    var parts = [];
    parts.push("<div class='total'>");
    parts.push("<div class='card'><div class='label'>Primary</div><div class='value'>" + escapeHtml(formatWindow(primary)) + "</div></div>");
    parts.push("<div class='card'><div class='label'>5h Progress</div><div class='value'>" + escapeHtml(primary && primary.progress_percent != null ? primary.progress_percent + "%" : "-") + "</div></div>");
    parts.push("<div class='card'><div class='label'>Secondary</div><div class='value'>" + escapeHtml(formatWindow(secondary)) + "</div></div>");
    parts.push("<div class='card'><div class='label'>7d Progress</div><div class='value'>" + escapeHtml(secondary && secondary.progress_percent != null ? secondary.progress_percent + "%" : "-") + "</div></div>");
    parts.push("</div>");

    if (authFiles.length === 0) {
      parts.push("<div class='card'>暂无缓存认证用量</div>");
    } else {
      for (var i = 0; i < authFiles.length; i++) {
        var item = authFiles[i] || {};
        var usage = item.usage || {};
        var rate = usage.rate_limit || {};
        var p = rate.primary_window || null;
        var s = rate.secondary_window || null;
        var name = item.file_name || item.auth_id || ("codex-auth-" + i);
        parts.push("<div class='auth'>");
        parts.push("<div class='row'><strong>" + escapeHtml(name) + "</strong><button data-auth-name='" + escapeHtml(name) + "'>下载 Codex CLI OAuth</button></div>");
        parts.push("<div class='row'><span class='label'>Plan</span><span>" + escapeHtml(usage.plan_type || "-") + "</span></div>");
        parts.push("<div class='row'><span class='label'>Primary</span><span>" + escapeHtml(formatWindow(p)) + "</span></div>");
        parts.push("<div class='row'><span class='label'>Secondary</span><span>" + escapeHtml(formatWindow(s)) + "</span></div>");
        parts.push("<div class='row'><span class='label'>Status</span><span>" + escapeHtml(item.status || "-") + "</span></div>");
        if (item.error) {
          parts.push("<div class='err'>" + escapeHtml(item.error) + "</div>");
        }
        parts.push("</div>");
      }
    }

    body.innerHTML = parts.join("");
    time.textContent = "updated " + formatTime(summary && summary.updated_at);

    var buttons = body.querySelectorAll("button[data-auth-name]");
    for (var b = 0; b < buttons.length; b++) {
      buttons[b].addEventListener("click", function (ev) {
        var btn = ev.currentTarget;
        var authName = btn.getAttribute("data-auth-name");
        if (!authName) return;
        btn.disabled = true;
        var old = btn.textContent;
        btn.textContent = "下载中...";
        downloadCliAuth(authName).catch(function (err) {
          alert("下载失败: " + (err && err.message ? err.message : err));
        }).finally(function () {
          btn.disabled = false;
          btn.textContent = old;
        });
      });
    }
  }

  function refresh() {
    return fetch("/v0/management/codex-usage-summary", { headers: headers() })
      .then(function (resp) {
        if (!resp.ok) return resp.text().then(function (txt) { throw new Error(txt || ("HTTP " + resp.status)); });
        return resp.json();
      })
      .then(function (json) { render(json || {}); })
      .catch(function (err) {
        var body = document.getElementById(ROOT_ID + "-body");
        if (body) {
          body.innerHTML = "<div class='card err'>读取用量失败: " + escapeHtml(err && err.message ? err.message : err) + "</div>";
        }
      });
  }

  function init() {
    ensureRoot();
    refresh();
    setInterval(refresh, REFRESH_MS);
  }

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();`

func injectManagementCodexWidget(html string) string {
	if strings.Contains(html, managementCodexWidgetScriptPath) {
		return html
	}
	tag := `<script src="` + managementCodexWidgetScriptPath + `"></script>`
	lower := strings.ToLower(html)
	if idx := strings.LastIndex(lower, "</body>"); idx >= 0 {
		return html[:idx] + tag + html[idx:]
	}
	return html + tag
}

func (s *Server) serveManagementCodexWidget(c *gin.Context) {
	c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(managementCodexWidgetJS))
}
