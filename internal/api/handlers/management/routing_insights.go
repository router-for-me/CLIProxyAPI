package management

import (
	"math"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// GetRoutingInsights returns sticky session activity and balanced-hash score preview.
func (h *Handler) GetRoutingInsights(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth manager unavailable"})
		return
	}

	window := 5 * time.Minute
	if raw := strings.TrimSpace(c.Query("window")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			window = parsed
		}
	}

	provider := strings.TrimSpace(c.Query("provider"))
	model := strings.TrimSpace(c.Query("model"))
	requestKey := strings.TrimSpace(c.Query("idempotency_key"))

	sessions := h.authManager.SessionAffinitySnapshot(window)
	hashPreview := h.authManager.BalancedHashPreview(provider, model, requestKey)

	authCounts := make(map[string]int, len(sessions))
	for i := 0; i < len(sessions); i++ {
		authID := strings.TrimSpace(sessions[i].AuthID)
		if authID == "" {
			continue
		}
		authCounts[authID]++
	}

	countItems := make([]gin.H, 0, len(authCounts))
	countValues := make([]int, 0, len(authCounts))
	for authID, count := range authCounts {
		countItems = append(countItems, gin.H{
			"auth_id":         authID,
			"active_sessions": count,
		})
		countValues = append(countValues, count)
	}
	sort.Slice(countItems, func(i, j int) bool {
		return countItems[i]["active_sessions"].(int) > countItems[j]["active_sessions"].(int)
	})

	metrics := computeBalanceMetrics(countValues)

	c.JSON(http.StatusOK, gin.H{
		"window_seconds":             int(window.Seconds()),
		"active_session_bindings":    sessions,
		"active_session_auth_counts": countItems,
		"balance_metrics":            metrics,
		"hash_preview":               hashPreview,
		"provider":                   provider,
		"model":                      model,
	})
}

func computeBalanceMetrics(counts []int) gin.H {
	if len(counts) == 0 {
		return gin.H{
			"active_auths":          0,
			"total_active_sessions": 0,
			"max_min_ratio":         0.0,
			"top1_share":            0.0,
			"gini":                  0.0,
		}
	}
	total := 0
	maxV := counts[0]
	minV := counts[0]
	for i := 0; i < len(counts); i++ {
		v := counts[i]
		total += v
		if v > maxV {
			maxV = v
		}
		if v < minV {
			minV = v
		}
	}

	maxMinRatio := 0.0
	if minV > 0 {
		maxMinRatio = float64(maxV) / float64(minV)
	}

	top1Share := 0.0
	if total > 0 {
		top1Share = float64(maxV) / float64(total)
	}

	// Gini coefficient: sum_i sum_j |xi-xj| / (2*n*sum_x)
	gini := 0.0
	if total > 0 {
		n := len(counts)
		diffSum := 0.0
		for i := 0; i < n; i++ {
			for j := 0; j < n; j++ {
				diffSum += math.Abs(float64(counts[i] - counts[j]))
			}
		}
		gini = diffSum / (2.0 * float64(n) * float64(total))
	}

	return gin.H{
		"active_auths":          len(counts),
		"total_active_sessions": total,
		"max_min_ratio":         maxMinRatio,
		"top1_share":            top1Share,
		"gini":                  gini,
	}
}

// GetSessionMonitorPage serves a tiny built-in front-end for routing diagnostics.
func (h *Handler) GetSessionMonitorPage(c *gin.Context) {
	if h == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	// Optional defaults for convenience when behind reverse proxy.
	defaultWindow := strings.TrimSpace(c.Query("window"))
	if defaultWindow == "" {
		defaultWindow = "5m"
	}
	c.Header("X-Session-Monitor-Default-Window", defaultWindow)
	html := strings.ReplaceAll(sessionMonitorHTML, "__DEFAULT_WINDOW__", defaultWindow)
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(html))
}

const sessionMonitorHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>CLIProxy 路由监控</title>
  <style>
    :root { color-scheme: dark; }
    body { margin: 0; font-family: -apple-system, Segoe UI, Roboto, sans-serif; background: #0f1115; color: #e8eaf0; }
    .wrap { max-width: 1200px; margin: 0 auto; padding: 16px; }
    .row { display: flex; gap: 8px; flex-wrap: wrap; margin-bottom: 12px; }
    input, button { padding: 8px 10px; border: 1px solid #313744; border-radius: 8px; background: #171b23; color: #e8eaf0; }
    input { min-width: 180px; }
    button { cursor: pointer; background: #2a5bd7; border-color: #2a5bd7; }
    button:hover { background: #2f66ee; }
    h2 { margin: 14px 0 8px; font-size: 16px; }
    table { width: 100%; border-collapse: collapse; background: #151922; border: 1px solid #2b3240; }
    th, td { border-bottom: 1px solid #232a36; padding: 6px 8px; text-align: left; font-size: 12px; }
    th { color: #aeb9cf; font-weight: 600; }
    .muted { color: #94a3b8; font-size: 12px; margin: 6px 0; }
    .err { color: #ff8585; }
    .ok { color: #7de2a8; }
  </style>
</head>
<body>
  <div class="wrap">
    <h1 style="font-size:20px;margin:0 0 10px;">CLIProxy 路由/会话监控</h1>
    <div class="row">
      <input id="base" placeholder="API 基地址，如 https://2api.xxx.com" />
      <input id="key" placeholder="管理密钥（Bearer）" />
      <input id="window" value="__DEFAULT_WINDOW__" placeholder="窗口，如 5m" />
      <input id="provider" placeholder="provider，可空" />
      <input id="model" placeholder="model，可空" />
      <input id="idem" placeholder="idempotency_key，可空" />
      <button id="refresh">刷新</button>
    </div>
    <div id="status" class="muted">等待刷新...</div>
    <div id="metrics" class="muted">均衡度指标：-</div>

    <h2>最近活跃会话绑定</h2>
    <table id="sessionTable">
      <thead><tr><th>session_id</th><th>auth_id</th><th>provider</th><th>model_key</th><th>last_seen_at</th></tr></thead>
      <tbody></tbody>
    </table>

    <h2 style="margin-top:14px;">按账号活跃会话数</h2>
    <table id="countTable">
      <thead><tr><th>auth_id</th><th>active_sessions</th></tr></thead>
      <tbody></tbody>
    </table>

    <h2 style="margin-top:14px;">Hash 预览（score breakdown）</h2>
    <table id="hashTable">
      <thead><tr><th>auth_id</th><th>provider</th><th>total</th><th>hash</th><th>fresh</th><th>quota</th><th>penalty</th><th>blocked</th><th>reason</th></tr></thead>
      <tbody></tbody>
    </table>
  </div>
  <script>
    const byId = (id) => document.getElementById(id);
    const statusEl = byId("status");
    const fill = (tableId, rows, render) => {
      const tbody = byId(tableId).querySelector("tbody");
      tbody.innerHTML = "";
      rows.forEach((r) => {
        const tr = document.createElement("tr");
        tr.innerHTML = render(r);
        tbody.appendChild(tr);
      });
    };
    const num = (v) => Number(v || 0).toFixed(4);
    async function refresh() {
      const base = (byId("base").value || location.origin).trim().replace(/\/$/, "");
      const key = byId("key").value.trim();
      const windowVal = byId("window").value.trim() || "5m";
      const provider = byId("provider").value.trim();
      const model = byId("model").value.trim();
      const idem = byId("idem").value.trim();
      const params = new URLSearchParams();
      params.set("window", windowVal);
      if (provider) params.set("provider", provider);
      if (model) params.set("model", model);
      if (idem) params.set("idempotency_key", idem);
      const url = base + "/v0/management/routing/insights?" + params.toString();
      statusEl.className = "muted";
      statusEl.textContent = "加载中...";
      try {
        const headers = key ? { Authorization: "Bearer " + key } : {};
        const res = await fetch(url, { headers: headers });
        const body = await res.json();
        if (!res.ok) throw new Error(body.error || ("HTTP " + res.status));
        fill("sessionTable", body.active_session_bindings || [], (r) =>
          "<td>" + (r.session_id || "") + "</td><td>" + (r.auth_id || "") + "</td><td>" + (r.provider || "") + "</td><td>" + (r.model_key || "") + "</td><td>" + (r.last_seen_at || "") + "</td>");
        fill("countTable", body.active_session_auth_counts || [], (r) =>
          "<td>" + (r.auth_id || "") + "</td><td>" + (r.active_sessions || 0) + "</td>");
        fill("hashTable", body.hash_preview || [], (r) =>
          "<td>" + (r.auth_id || "") + "</td><td>" + (r.provider || "") + "</td><td>" + num(r.total_score) + "</td><td>" + num(r.hash_score) + "</td><td>" + num(r.freshness_score) + "</td><td>" + num(r.quota_score) + "</td><td>" + num(r.penalty_score) + "</td><td>" + (r.blocked ? "yes" : "no") + "</td><td>" + (r.block_reason || "") + "</td>");
        const m = body.balance_metrics || {};
        byId("metrics").textContent = "均衡度指标：active_auths=" + (m.active_auths || 0) + ", total_sessions=" + (m.total_active_sessions || 0) + ", max/min=" + num(m.max_min_ratio) + ", top1=" + num(m.top1_share) + ", gini=" + num(m.gini);
        statusEl.className = "ok";
        statusEl.textContent = "已更新：window=" + body.window_seconds + "s, sessions=" + ((body.active_session_bindings || []).length);
      } catch (err) {
        statusEl.className = "err";
        statusEl.textContent = "加载失败: " + (err && err.message ? err.message : err);
      }
    }
    byId("refresh").addEventListener("click", refresh);
  </script>
</body>
</html>`

// GetHashMonitorPage serves a modern hash scoring monitoring page.
func (h *Handler) GetHashMonitorPage(c *gin.Context) {
	if h == nil {
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(hashMonitorHTML))
}

const hashMonitorHTML = `<!doctype html>
<html lang="zh-CN">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>CLIProxy Hash 监控</title>
  <style>
    :root {
      --bg: #0f1117;
      --surface: #151a24;
      --card: #1c2130;
      --border: #2a3042;
      --text: #e2e8f0;
      --text-muted: #8892a4;
      --accent: #6366f1;
      --accent2: #818cf8;
      --green: #22c55e;
      --red: #ef4444;
      --yellow: #eab308;
      --purple: #a855f7;
      --radius: 12px;
      --radius-sm: 8px;
    }
    * { box-sizing: border-box; margin: 0; padding: 0; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
      background: var(--bg);
      color: var(--text);
      min-height: 100vh;
    }
    .header {
      background: var(--surface);
      border-bottom: 1px solid var(--border);
      padding: 14px 24px;
      display: flex;
      align-items: center;
      gap: 16px;
      position: sticky;
      top: 0;
      z-index: 10;
    }
    .header-icon {
      width: 36px; height: 36px;
      background: linear-gradient(135deg, var(--accent), var(--purple));
      border-radius: 10px;
      display: flex; align-items: center; justify-content: center;
      font-size: 18px;
      flex-shrink: 0;
    }
    .header-title { font-size: 16px; font-weight: 700; }
    .header-sub { font-size: 12px; color: var(--text-muted); margin-top: 2px; }
    .header-right { margin-left: auto; display: flex; align-items: center; gap: 12px; }
    .status-badge {
      padding: 4px 10px;
      border-radius: 20px;
      font-size: 11px;
      font-weight: 600;
      background: var(--card);
      color: var(--text-muted);
    }
    .status-badge.ok { background: rgba(34,197,94,0.15); color: var(--green); }
    .status-badge.err { background: rgba(239,68,68,0.15); color: var(--red); }
    .filters {
      padding: 16px 24px;
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
      align-items: center;
      border-bottom: 1px solid var(--border);
      background: var(--surface);
    }
    .filter-label { font-size: 12px; color: var(--text-muted); }
    .filter-select, .filter-input {
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: var(--radius-sm);
      color: var(--text);
      padding: 6px 10px;
      font-size: 12px;
      outline: none;
      transition: border-color 0.2s;
    }
    .filter-select:focus, .filter-input:focus { border-color: var(--accent); }
    .filter-input { min-width: 160px; }
    .refresh-btn {
      margin-left: auto;
      background: var(--accent);
      border: none;
      border-radius: var(--radius-sm);
      color: #fff;
      padding: 6px 14px;
      font-size: 12px;
      font-weight: 600;
      cursor: pointer;
      transition: background 0.2s;
      display: flex; align-items: center; gap: 6px;
    }
    .refresh-btn:hover { background: var(--accent2); }
    .refresh-btn:disabled { opacity: 0.5; cursor: not-allowed; }
    .main { padding: 20px 24px; }
    .metrics-row {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(180px, 1fr));
      gap: 12px;
      margin-bottom: 20px;
    }
    .metric-card {
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: var(--radius);
      padding: 14px 16px;
    }
    .metric-label { font-size: 11px; color: var(--text-muted); margin-bottom: 6px; text-transform: uppercase; letter-spacing: 0.5px; }
    .metric-value { font-size: 22px; font-weight: 700; font-variant-numeric: tabular-nums; }
    .metric-value.green { color: var(--green); }
    .metric-value.red { color: var(--red); }
    .metric-value.yellow { color: var(--yellow); }
    .metric-value.accent { color: var(--accent2); }
    .section-title {
      font-size: 13px;
      font-weight: 600;
      color: var(--text-muted);
      margin-bottom: 12px;
      text-transform: uppercase;
      letter-spacing: 0.5px;
    }
    .hash-grid {
      display: grid;
      grid-template-columns: repeat(auto-fill, minmax(240px, 1fr));
      gap: 12px;
    }
    .hash-card {
      background: var(--card);
      border: 1px solid var(--border);
      border-radius: var(--radius);
      padding: 14px 16px;
      transition: border-color 0.2s, transform 0.2s;
      position: relative;
      overflow: hidden;
    }
    .hash-card:hover { border-color: var(--accent); transform: translateY(-1px); }
    .hash-card.blocked { border-color: rgba(239,68,68,0.4); background: rgba(239,68,68,0.05); }
    .hash-card.top1 { border-color: rgba(99,102,241,0.5); }
    .hash-card-rank {
      position: absolute;
      top: 10px;
      right: 12px;
      font-size: 10px;
      font-weight: 700;
      color: var(--text-muted);
      background: var(--surface);
      padding: 2px 6px;
      border-radius: 4px;
    }
    .hash-card-rank.rank1 { color: #fbbf24; background: rgba(251,191,36,0.12); }
    .hash-card-rank.rank2 { color: #94a3b8; background: rgba(148,163,184,0.12); }
    .hash-card-rank.rank3 { color: #b45309; background: rgba(180,83,9,0.12); }
    .hash-auth-id {
      font-size: 12px;
      font-weight: 700;
      margin-bottom: 4px;
      white-space: nowrap;
      overflow: hidden;
      text-overflow: ellipsis;
      padding-right: 40px;
    }
    .hash-auth-meta { font-size: 11px; color: var(--text-muted); margin-bottom: 10px; }
    .hash-scores { display: flex; flex-direction: column; gap: 4px; }
    .hash-score-row {
      display: flex;
      align-items: center;
      gap: 8px;
      font-size: 11px;
    }
    .hash-score-label { color: var(--text-muted); width: 52px; flex-shrink: 0; }
    .hash-score-bar-wrap { flex: 1; background: var(--surface); border-radius: 3px; height: 5px; overflow: hidden; }
    .hash-score-bar { height: 100%; border-radius: 3px; transition: width 0.4s ease; }
    .bar-hash { background: var(--accent); }
    .bar-fresh { background: var(--green); }
    .bar-quota { background: var(--yellow); }
    .bar-penalty { background: var(--red); }
    .hash-score-num { font-size: 11px; color: var(--text); width: 36px; text-align: right; font-variant-numeric: tabular-nums; }
    .hash-total {
      margin-top: 10px;
      padding-top: 10px;
      border-top: 1px solid var(--border);
      display: flex;
      align-items: center;
      justify-content: space-between;
    }
    .hash-total-label { font-size: 11px; color: var(--text-muted); }
    .hash-total-value { font-size: 16px; font-weight: 800; color: var(--green); }
    .hash-card.blocked .hash-total-value { color: var(--red); }
    .blocked-badge {
      display: inline-block;
      font-size: 10px;
      padding: 2px 7px;
      border-radius: 4px;
      background: rgba(239,68,68,0.2);
      color: var(--red);
      margin-left: 6px;
    }
    .hash-empty {
      grid-column: 1 / -1;
      text-align: center;
      padding: 48px 0;
      color: var(--text-muted);
      font-size: 13px;
    }
    .hash-empty-icon { font-size: 32px; margin-bottom: 8px; opacity: 0.4; }
    .provider-tabs {
      display: flex;
      gap: 4px;
      margin-bottom: 16px;
      border-bottom: 1px solid var(--border);
    }
    .provider-tab {
      padding: 6px 14px;
      font-size: 12px;
      font-weight: 600;
      color: var(--text-muted);
      cursor: pointer;
      border: none;
      background: none;
      border-bottom: 2px solid transparent;
      transition: all 0.2s;
      margin-bottom: -1px;
    }
    .provider-tab:hover { color: var(--text); }
    .provider-tab.active { color: var(--accent2); border-bottom-color: var(--accent); }
  </style>
</head>
<body>
  <div class="header">
    <div class="header-icon">&#9881;</div>
    <div>
      <div class="header-title">Hash 评分监控</div>
      <div class="header-sub">Balanced Hash Score Overview</div>
    </div>
    <div class="header-right">
      <span id="lastUpdate" class="status-badge">-</span>
      <span id="accountCount" class="status-badge">-</span>
    </div>
  </div>

  <div class="filters">
    <span class="filter-label">Provider</span>
    <select id="providerFilter" class="filter-select">
      <option value="">全部</option>
    </select>
    <input id="authIdFilter" class="filter-input" placeholder="搜索 auth_id..." />
    <button id="refreshBtn" class="refresh-btn" onclick="doRefresh()">
      <span id="refreshIcon">&#8635;</span> 刷新
    </button>
  </div>

  <div class="main">
    <div class="metrics-row" id="metricsRow"></div>

    <div class="provider-tabs" id="providerTabs"></div>
    <div class="section-title">账号 Hash 评分</div>
    <div class="hash-grid" id="hashGrid"></div>
  </div>

  <script>
    var allData = [];
    var activeProvider = "";

    function num(v) { return Number(v || 0); }
    function fmt(v, d) { return num(v).toFixed(d !== undefined ? d : 4); }

    function scoreColor(score) {
      if (score >= 0.75) return "var(--green)";
      if (score >= 0.5) return "var(--yellow)";
      return "var(--red)";
    }

    function rankClass(rank) {
      if (rank === 1) return "rank1";
      if (rank === 2) return "rank2";
      if (rank === 3) return "rank3";
      return "";
    }

    function renderMetrics(items) {
      var total = items.length;
      var blocked = 0;
      var top1 = total > 0 ? (items[0] ? items[0].total_score : 0) : 0;
      var avg = 0;
      for (var i = 0; i < items.length; i++) {
        if (items[i].blocked) blocked++;
        avg += num(items[i].total_score);
      }
      if (total > 0) avg /= total;

      var providers = {};
      for (var i = 0; i < items.length; i++) {
        var p = items[i].provider || "unknown";
        providers[p] = (providers[p] || 0) + 1;
      }
      var topProvider = "";
      var topProviderCount = 0;
      for (var k in providers) { if (providers[k] > topProviderCount) { topProviderCount = providers[k]; topProvider = k; } }

      var gini = 0;
      if (total > 1) {
        var scores = [];
        for (var i = 0; i < items.length; i++) scores.push(num(items[i].total_score));
        scores.sort(function(a,b){return a-b;});
        var n = scores.length;
        var sum = 0;
        for (var i = 0; i < n; i++) sum += scores[i];
        var diffSum = 0;
        for (var i = 0; i < n; i++) for (var j = 0; j < n; j++) diffSum += Math.abs(scores[i] - scores[j]);
        gini = diffSum / (2.0 * n * sum);
      }

      var mRow = document.getElementById("metricsRow");
      mRow.innerHTML = "" +
        '<div class="metric-card"><div class="metric-label">账号总数</div><div class="metric-value accent">' + total + '</div></div>' +
        '<div class="metric-card"><div class="metric-label">被屏蔽</div><div class="metric-value ' + (blocked > 0 ? "red" : "green") + '">' + blocked + '</div></div>' +
        '<div class="metric-card"><div class="metric-label">最高总分</div><div class="metric-value green">' + fmt(top1, 4) + '</div></div>' +
        '<div class="metric-card"><div class="metric-label">平均总分</div><div class="metric-value">' + fmt(avg, 4) + '</div></div>' +
        '<div class="metric-card"><div class="metric-label">Provider 数</div><div class="metric-value accent">' + Object.keys(providers).length + '</div></div>' +
        '<div class="metric-card"><div class="metric-label">Gini 不均衡度</div><div class="metric-value ' + (gini > 0.3 ? "red" : "green") + '">' + fmt(gini, 4) + '</div></div>';
    }

    function renderProviderTabs(items) {
      var providers = {};
      for (var i = 0; i < items.length; i++) {
        var p = items[i].provider || "unknown";
        if (!providers[p]) providers[p] = 0;
        providers[p]++;
      }
      var pList = Object.keys(providers).sort();
      var tabsEl = document.getElementById("providerTabs");
      var selEl = document.getElementById("providerFilter");

      tabsEl.innerHTML = '<button class="provider-tab ' + (activeProvider === "" ? "active" : "") + '" onclick="setProvider(\"\")">全部</button>';
      selEl.innerHTML = '<option value="">全部</option>';
      for (var i = 0; i < pList.length; i++) {
        var p = pList[i];
        var active = activeProvider === p ? "active" : "";
        tabsEl.innerHTML += '<button class="provider-tab ' + active + '" onclick="setProvider(\'' + p + '\')">' + p + ' (' + providers[p] + ')</button>';
        selEl.innerHTML += '<option value="' + p + '">' + p + ' (' + providers[p] + ')</option>';
      }
    }

    function setProvider(p) {
      activeProvider = p;
      renderProviderTabs(allData);
      renderCards(filterData());
    }

    function filterData() {
      var p = activeProvider;
      var q = (document.getElementById("authIdFilter").value || "").toLowerCase();
      var out = [];
      for (var i = 0; i < allData.length; i++) {
        var item = allData[i];
        if (p && item.provider !== p) continue;
        if (q && (item.auth_id || "").toLowerCase().indexOf(q) < 0) continue;
        out.push(item);
      }
      return out;
    }

    function renderCards(items) {
      var grid = document.getElementById("hashGrid");
      if (items.length === 0) {
        grid.innerHTML = '<div class="hash-empty"><div class="hash-empty-icon">&#9888;</div>暂无数据<br><small>确保管理密钥已配置且账号正常</small></div>';
        return;
      }
      var html = "";
      for (var i = 0; i < items.length; i++) {
        var item = items[i];
        var blocked = item.blocked;
        var total = num(item.total_score);
        var rank = i + 1;
        var topClass = rank <= 3 ? " top" + rank : "";
        var cardClass = "hash-card" + (blocked ? " blocked" : "") + topClass;
        var rankCls = rankClass(rank);
        html += '<div class="' + cardClass + '">' +
          '<div class="hash-card-rank ' + rankCls + '">#' + rank + '</div>' +
          '<div class="hash-auth-id" title="' + item.auth_id + '">' + item.auth_id + '</div>' +
          '<div class="hash-auth-meta">' + (item.provider || "?") + ' &middot; 会话: ' + (item._sessCount || 0) + (blocked ? ' &middot; <span style="color:var(--red)">' + (item.block_reason || "blocked") + '</span>' : '') + '</div>' +
          '<div class="hash-scores">' +
            scoreBar("hash", num(item.hash_score)) +
            scoreBar("fresh", num(item.freshness_score)) +
            scoreBar("quota", num(item.quota_score)) +
            scoreBar("penalty", num(item.penalty_score)) +
          '</div>' +
          '<div class="hash-total">' +
            '<span class="hash-total-label">总分</span>' +
            '<span class="hash-total-value">' + fmt(total, 4) + (blocked ? '<span class="blocked-badge">' + (item.block_reason || "blocked") + '</span>' : '') + '</span>' +
          '</div>' +
        '</div>';
      }
      grid.innerHTML = html;
    }

    function scoreBar(label, score) {
      var pct = Math.round(Math.max(0, Math.min(1, score)) * 100);
      var cls = label === "hash" ? "bar-hash" : label === "fresh" ? "bar-fresh" : label === "quota" ? "bar-quota" : "bar-penalty";
      return '<div class="hash-score-row">' +
        '<span class="hash-score-label">' + label + '</span>' +
        '<div class="hash-score-bar-wrap"><div class="hash-score-bar ' + cls + '" style="width:' + pct + '%"></div></div>' +
        '<span class="hash-score-num" style="color:' + scoreColor(score) + '">' + fmt(score, 2) + '</span>' +
      '</div>';
    }

    function setStatus(msg, cls) {
      var el = document.getElementById("lastUpdate");
      if (el) { el.textContent = msg; el.className = "status-badge " + (cls || ""); }
    }

    function setCount(n) {
      var el = document.getElementById("accountCount");
      if (el) { el.textContent = n + " 账号"; }
    }

    function setLoading(loading) {
      var btn = document.getElementById("refreshBtn");
      var icon = document.getElementById("refreshIcon");
      if (btn) btn.disabled = loading;
      if (icon) icon.innerHTML = loading ? "&#8230;" : "&#8635;";
    }

    function getAuthToken() {
      var params = new URLSearchParams(window.location.search);
      var k = params.get("key");
      if (k && k.length >= 4) return k;
      return "";
    }

    async function doRefresh() {
      setLoading(true);
      setStatus("加载中...", "");
      try {
        var url = "/v0/management/routing/insights?window=5m&idempotency_key=" + Date.now();
        if (activeProvider) url += "&provider=" + encodeURIComponent(activeProvider);
        var headers = {};
        var token = getAuthToken();
        if (token) headers["Authorization"] = "Bearer " + token;
        var res = await fetch(url, { headers: headers });
        if (!res.ok) {
          var errMsg = res.status === 401 ? "认证失败：请检查管理密钥" : "HTTP " + res.status;
          setStatus(errMsg, "err");
          setLoading(false);
          return;
        }
        var body = await res.json();
        allData = body.hash_preview || [];
        var ac = body.active_session_auth_counts || [];
        var acMap = {};
        for (var i = 0; i < ac.length; i++) acMap[(ac[i].auth_id || "").trim()] = ac[i].active_sessions || 0;
        for (var i = 0; i < allData.length; i++) {
          var aid = (allData[i].auth_id || "").trim();
          allData[i]._sessCount = acMap[aid] || 0;
        }
        // Sort by total desc
        allData.sort(function(a, b){ return (b.total_score||0) - (a.total_score||0); });
        renderMetrics(allData);
        renderProviderTabs(allData);
        renderCards(filterData());
        setCount(allData.length);
        var now = new Date();
        setStatus(now.getHours().toString().padStart(2,"0") + ":" + now.getMinutes().toString().padStart(2,"0") + ":" + now.getSeconds().toString().padStart(2,"0"), "ok");
      } catch(e) {
        setStatus("异常: " + (e && e.message ? e.message : String(e)), "err");
      }
      setLoading(false);
    }

    document.getElementById("providerFilter").addEventListener("change", function(e) {
      setProvider(e.target.value);
    });
    document.getElementById("authIdFilter").addEventListener("input", function() {
      renderCards(filterData());
    });

    doRefresh();
    setInterval(doRefresh, 15000);
  </script>
</body>
</html>`
