package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

var managementPanelKeyTestBatchPattern = regexp.MustCompile(`\(await Promise\.all\(t\.map\(e=>([A-Za-z_$][A-Za-z0-9_$]*)\(e\)\)\)\)\.filter\(Boolean\)\.length`)

var (
	managementPanelDisabledKeyGroupingNeedle      = []byte(`excludedModels:kg(t.excludedModels)`)
	managementPanelDisabledKeyGroupingReplacement = []byte(`excludedModels:kg(Lp(t.excludedModels))`)
)

func managementConfigVersionGuardScript(initialVersion string) []byte {
	initialVersionJSON, err := json.Marshal(strings.TrimSpace(initialVersion))
	if err != nil {
		initialVersionJSON = []byte(`""`)
	}
	return []byte(`<script id="cliproxy-config-version-guard">
(function () {
  if (window.__cliproxyConfigVersionGuard) return;
  window.__cliproxyConfigVersionGuard = true;
  var latestConfigVersion = ` + string(initialVersionJSON) + `;
  var lastConflictVersion = "";
  var writeQueue = Promise.resolve();
  var conflictAlertVisible = false;
  var lastConflictAlertAt = 0;
  var originalFetch = window.fetch ? window.fetch.bind(window) : null;
  if (!originalFetch) return;
  window.__cliproxyLatestConfigVersion = latestConfigVersion;
  window.__cliproxyKeyTestDelayMs = window.__cliproxyKeyTestDelayMs || 350;
  window.__cliproxySequentialMap = window.__cliproxySequentialMap || async function (items, mapper) {
    var results = [];
    var list = Array.isArray(items) ? items : [];
    for (var i = 0; i < list.length; i += 1) {
      results.push(await mapper(list[i], i));
      if (i + 1 < list.length) {
        await new Promise(function (resolve) {
          window.setTimeout(resolve, window.__cliproxyKeyTestDelayMs);
        });
      }
    }
    return results;
  };

  function setLatestConfigVersion(version) {
    version = String(version || "").trim();
    if (!version) return;
    latestConfigVersion = version;
    window.__cliproxyLatestConfigVersion = latestConfigVersion;
  }

  function requestPath(input) {
    try {
      var raw = typeof input === "string" ? input : input && input.url;
      return new URL(raw || "", window.location.href).pathname;
    } catch (_) {
      return "";
    }
  }

  function requestMethod(input, init) {
    return String((init && init.method) || (input && input.method) || "GET").toUpperCase();
  }

  function shouldGuard(method, path) {
    if (method === "GET" || method === "HEAD") return false;
    return path.indexOf("/v0/management/") === 0;
  }

  function isAPICall(method, path) {
    return method === "POST" && path === "/v0/management/api-call";
  }

  function detachedAbortSignal() {
    try {
      if (!window.AbortController) return undefined;
      return new window.AbortController().signal;
    } catch (_) {
      return undefined;
    }
  }

  function detachAPICallAbortSignal(input, init) {
    var nextInput = input;
    var nextInit = Object.assign({}, init || {});
    if ("signal" in nextInit) {
      try { delete nextInit.signal; } catch (_) { nextInit.signal = undefined; }
    }
    try {
      if (typeof Request !== "undefined" && input instanceof Request) {
        var signal = detachedAbortSignal();
        nextInput = signal ? new Request(input, { signal: signal }) : new Request(input);
      }
    } catch (_) {
      nextInput = input;
    }
    return { input: nextInput, init: nextInit };
  }

  function observeResponseVersion(response) {
    if (response && response.status === 409) return;
    var nextVersion = response && response.headers && response.headers.get("X-Config-Version");
    if (nextVersion) setLatestConfigVersion(nextVersion);
  }

  function publishConflict(detail) {
    window.__cliproxyConfigConflict = detail;
    if (window.console && window.console.warn) {
      window.console.warn("[CLIProxyAPI] management config write conflict", detail);
    }
    try {
      window.dispatchEvent(new CustomEvent("cliproxy:config-conflict", { detail: detail }));
    } catch (_) {}
  }

  function showConflictAlertOnce(detail) {
    var now = Date.now ? Date.now() : new Date().getTime();
    var signature = detail && (detail.currentVersion || detail.submittedVersion || detail.path || "");
    if (conflictAlertVisible) return;
    if (lastConflictVersion === signature && now - lastConflictAlertAt < 10000) return;
    lastConflictVersion = signature;
    lastConflictAlertAt = now;
    conflictAlertVisible = true;
    window.alert("配置文件刚刚被另一个保存动作更新了。\n\n这次保存已被拦截，没有覆盖线上配置。请刷新页面确认最新内容后再保存。");
    window.setTimeout(function () { conflictAlertVisible = false; }, 1000);
  }

  function handleConflictBody(body, source) {
    if (!body || body.error !== "config_conflict") return;
    var detail = {
      source: source || "",
      message: body.message || "",
      currentVersion: body["current-version"] || "",
      submittedVersion: body["submitted-version"] || "",
      lastModified: body["last-modified"] || "",
      method: body.method || "",
      route: body.route || "",
      path: body.path || ""
    };
    publishConflict(detail);
    showConflictAlertOnce(detail);
  }

  function handleConflictResponse(response) {
    if (!response || response.status !== 409) return;
    response.clone().json().then(function (body) {
      handleConflictBody(body, "fetch");
    }).catch(function () {});
  }

  function enqueueWrite(task) {
    var previous = writeQueue.catch(function () {});
    var next = previous.then(task, task);
    writeQueue = next.catch(function () {});
    return next;
  }

  window.fetch = async function (input, init) {
    init = init || {};
    var method = requestMethod(input, init);
    var path = requestPath(input);
    var guarded = shouldGuard(method, path);
    var apiCall = isAPICall(method, path);

    var send = async function () {
      var headers = new Headers(init.headers || (input && input.headers) || {});
      if (latestConfigVersion && guarded && !headers.has("If-Match") && !headers.has("X-Config-Version")) {
        headers.set("If-Match", '"' + latestConfigVersion + '"');
        init = Object.assign({}, init, { headers: headers });
      }
      var finalInput = input;
      var finalInit = init;
      if (apiCall) {
        var detached = detachAPICallAbortSignal(finalInput, finalInit);
        finalInput = detached.input;
        finalInit = detached.init;
      }
      var response = await originalFetch(finalInput, finalInit);
      observeResponseVersion(response);
      handleConflictResponse(response);
      return response;
    };

    if (guarded) {
      return enqueueWrite(send);
    }
    return send();
  };

  if (window.XMLHttpRequest) {
    var originalOpen = window.XMLHttpRequest.prototype.open;
    var originalSend = window.XMLHttpRequest.prototype.send;
    var originalSetRequestHeader = window.XMLHttpRequest.prototype.setRequestHeader;

    window.XMLHttpRequest.prototype.open = function (method, url, async) {
      this.__cliproxyMethod = String(method || "GET").toUpperCase();
      this.__cliproxyPath = requestPath(url || "");
      this.__cliproxyAsync = async !== false;
      this.__cliproxyHeaders = {};
      return originalOpen.apply(this, arguments);
    };

    window.XMLHttpRequest.prototype.setRequestHeader = function (name, value) {
      if (name) this.__cliproxyHeaders[String(name).toLowerCase()] = true;
      return originalSetRequestHeader.apply(this, arguments);
    };

    function observeXHR(xhr, done) {
      xhr.addEventListener("loadend", function () {
        var nextVersion = "";
        try { nextVersion = xhr.getResponseHeader("X-Config-Version") || ""; } catch (_) {}
        if (nextVersion && xhr.status !== 409) setLatestConfigVersion(nextVersion);
        if (xhr.status === 409) {
          try {
            var body = JSON.parse(xhr.responseText || "{}");
            handleConflictBody(body, "xhr");
          } catch (_) {}
        }
        if (done) done();
      });
    }

    function attachXHRVersionHeader(xhr) {
      var headers = xhr.__cliproxyHeaders || {};
      if (latestConfigVersion && !headers["if-match"] && !headers["x-config-version"]) {
        originalSetRequestHeader.call(xhr, "If-Match", '"' + latestConfigVersion + '"');
      }
    }

    window.XMLHttpRequest.prototype.send = function () {
      var xhr = this;
      var args = arguments;
      var guarded = shouldGuard(xhr.__cliproxyMethod || "GET", xhr.__cliproxyPath || "");
      if (!guarded || xhr.__cliproxyAsync === false) {
        if (guarded) attachXHRVersionHeader(xhr);
        observeXHR(xhr);
        return originalSend.apply(xhr, args);
      }

      enqueueWrite(function () {
        return new Promise(function (resolve) {
          attachXHRVersionHeader(xhr);
          observeXHR(xhr, resolve);
          originalSend.apply(xhr, args);
        });
      });
      return undefined;
    };
  }
})();
</script>`)
}

func injectManagementConfigVersionGuard(data []byte, initialVersion ...string) []byte {
	version := ""
	if len(initialVersion) > 0 {
		version = initialVersion[0]
	}
	guardScript := managementConfigVersionGuardScript(version)
	data = removeManagementConfigVersionGuard(data)
	data = patchManagementPanelKeyTestBatch(data)
	data = patchManagementPanelDisabledKeyGrouping(data)
	lower := bytes.ToLower(data)
	if idx := bytes.LastIndex(lower, []byte("</body>")); idx >= 0 {
		out := make([]byte, 0, len(data)+len(guardScript))
		out = append(out, data[:idx]...)
		out = append(out, guardScript...)
		out = append(out, data[idx:]...)
		return out
	}
	out := make([]byte, 0, len(data)+len(guardScript))
	out = append(out, data...)
	out = append(out, guardScript...)
	return out
}

func patchManagementPanelKeyTestBatch(data []byte) []byte {
	return managementPanelKeyTestBatchPattern.ReplaceAll(
		data,
		[]byte(`(await window.__cliproxySequentialMap(t,e=>$1(e))).filter(Boolean).length`),
	)
}

func patchManagementPanelDisabledKeyGrouping(data []byte) []byte {
	return bytes.ReplaceAll(
		data,
		managementPanelDisabledKeyGroupingNeedle,
		managementPanelDisabledKeyGroupingReplacement,
	)
}

func removeManagementConfigVersionGuard(data []byte) []byte {
	start := bytes.Index(data, []byte(`<script id="cliproxy-config-version-guard">`))
	if start < 0 {
		return data
	}
	remaining := data[start:]
	endRel := bytes.Index(bytes.ToLower(remaining), []byte("</script>"))
	if endRel < 0 {
		return data
	}
	end := start + endRel + len("</script>")
	out := make([]byte, 0, len(data)-(end-start))
	out = append(out, data[:start]...)
	out = append(out, data[end:]...)
	return out
}

func managementConfigVersionFromFile(path string) (string, os.FileInfo, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(data)
	return fmt.Sprintf("sha256:%x", sum[:]), info, nil
}

func setManagementConfigVersionHeaders(c *gin.Context, version string, info os.FileInfo) {
	version = strings.TrimSpace(version)
	if c == nil || version == "" {
		return
	}
	c.Header("X-Config-Version", version)
	c.Header("ETag", `"`+version+`"`)
	c.Header("Cache-Control", "no-store")
	c.Header("X-Content-Type-Options", "nosniff")
	if info != nil {
		c.Header("Last-Modified", info.ModTime().UTC().Format(http.TimeFormat))
	}
}

func serveManagementControlPanelFile(c *gin.Context, filePath, configFilePath string) {
	version, info, errVersion := managementConfigVersionFromFile(configFilePath)
	if errVersion != nil {
		log.WithError(errVersion).Debug("failed to read config snapshot for management page guard")
	}
	setManagementConfigVersionHeaders(c, version, info)

	data, err := os.ReadFile(filePath)
	if err != nil {
		c.File(filePath)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", injectManagementConfigVersionGuard(data, version))
}
