(function () {
  'use strict';

  var INSTALL_FLAG = '__cpaLogQAInstalled';
  var MANAGEMENT_PREFIX = '/v0/management';
  var STATUS_ENDPOINT = '/v0/management/log-qa/status';
  var SUMMARY_ENDPOINT = '/v0/management/log-qa/summary';
  var SESSIONS_ENDPOINT = '/v0/management/log-qa/sessions';
  var RUNS_ENDPOINT = '/v0/management/log-qa/runs';
  var RUN_ENDPOINT = '/v0/management/log-qa/run';
  var SESSION_LOGS_ENDPOINT = '/v0/management/log-qa/sessions/logs';
  var AUTHORIZATION = 'authorization';
  var MANAGEMENT_KEY = 'x-management-key';
  var POLL_INTERVAL_MS = 2000;

  if (window[INSTALL_FLAG]) {
    return;
  }
  window[INSTALL_FLAG] = true;

  var nativeFetch = typeof window.fetch === 'function' ? window.fetch : null;
  var capturedAuth = null;
  var ui = null;
  var previousBodyOverflow = '';
  var pollTimer = null;
  var runInProgress = false;

  function managementTarget(input) {
    var raw = input;
    if (typeof Request !== 'undefined' && input instanceof Request) {
      raw = input.url;
    }
    if (raw === null || raw === undefined) {
      return null;
    }
    try {
      var url = new URL(String(raw), window.location.href);
      var prefixIndex = url.pathname.lastIndexOf(MANAGEMENT_PREFIX);
      if (prefixIndex < 0) {
        return null;
      }
      var suffix = url.pathname.slice(prefixIndex + MANAGEMENT_PREFIX.length);
      if (suffix && suffix.charAt(0) !== '/') {
        return null;
      }
      return {
        url: url,
        apiRoot: url.origin + url.pathname.slice(0, prefixIndex) + MANAGEMENT_PREFIX,
      };
    } catch (_) {
      return null;
    }
  }

  function normalizeHeader(name, value) {
    var lowered = String(name || '').trim().toLowerCase();
    if (lowered !== AUTHORIZATION && lowered !== MANAGEMENT_KEY) {
      return null;
    }
    var text = String(value || '').trim();
    if (!text) {
      return null;
    }
    return {
      name: lowered === AUTHORIZATION ? 'Authorization' : 'X-Management-Key',
      value: text,
    };
  }

  function authFromHeaders(headers) {
    if (!headers) {
      return null;
    }
    if (typeof Headers !== 'undefined') {
      try {
        var normalized = headers instanceof Headers ? headers : new Headers(headers);
        var authorization = normalizeHeader('Authorization', normalized.get('Authorization'));
        if (authorization) {
          return authorization;
        }
        return normalizeHeader('X-Management-Key', normalized.get('X-Management-Key'));
      } catch (_) {}
    }
    if (Array.isArray(headers)) {
      for (var i = 0; i < headers.length; i += 1) {
        var pair = headers[i];
        if (Array.isArray(pair) && pair.length >= 2) {
          var h = normalizeHeader(pair[0], pair[1]);
          if (h) {
            return h;
          }
        }
      }
    }
    if (typeof headers === 'object') {
      var names = Object.keys(headers);
      for (var j = 0; j < names.length; j += 1) {
        var oh = normalizeHeader(names[j], headers[names[j]]);
        if (oh) {
          return oh;
        }
      }
    }
    return null;
  }

  function rememberSuccessfulAuth(target, auth, status) {
    if (!target || !auth || status < 200 || status >= 400) {
      return;
    }
    capturedAuth = {
      apiRoot: target.apiRoot,
      headerName: auth.name,
      headerValue: auth.value,
    };
    ensureUI();
    if (ui) {
      ui.launcher.hidden = false;
    }
  }

  function installFetchInterceptor() {
    if (!nativeFetch) {
      return;
    }
    window.fetch = function (input, init) {
      var target = managementTarget(input);
      var auth = null;
      if (target) {
        auth = authFromHeaders(init && init.headers);
        if (!auth && typeof Request !== 'undefined' && input instanceof Request) {
          auth = authFromHeaders(input.headers);
        }
      }
      var responsePromise = nativeFetch.apply(window, arguments);
      if (target && auth) {
        Promise.resolve(responsePromise).then(
          function (response) {
            rememberSuccessfulAuth(target, auth, Number(response && response.status));
          },
          function () {}
        );
      }
      return responsePromise;
    };
  }

  function installXHRInterceptor() {
    if (typeof XMLHttpRequest === 'undefined') {
      return;
    }
    var prototype = XMLHttpRequest.prototype;
    var nativeOpen = prototype.open;
    var nativeSetRequestHeader = prototype.setRequestHeader;
    var nativeSend = prototype.send;
    var metadata = new WeakMap();
    prototype.open = function () {
      var target = managementTarget(arguments[1]);
      metadata.set(this, { target: target, auth: null });
      return nativeOpen.apply(this, arguments);
    };
    prototype.setRequestHeader = function (name, value) {
      var current = metadata.get(this);
      if (current && current.target) {
        var auth = normalizeHeader(name, value);
        if (auth) {
          current.auth = auth;
        }
      }
      return nativeSetRequestHeader.apply(this, arguments);
    };
    prototype.send = function () {
      var xhr = this;
      var current = metadata.get(xhr);
      if (current && current.target && current.auth) {
        xhr.addEventListener(
          'loadend',
          function () {
            rememberSuccessfulAuth(current.target, current.auth, Number(xhr.status || 0));
          },
          { once: true }
        );
      }
      return nativeSend.apply(xhr, arguments);
    };
  }

  function element(tag, className, text) {
    var node = document.createElement(tag);
    if (className) {
      node.className = className;
    }
    if (text !== undefined && text !== null) {
      node.textContent = String(text);
    }
    return node;
  }

  function addStyles() {
    if (document.getElementById('cpa-log-qa-style')) {
      return;
    }
    var style = document.createElement('style');
    style.id = 'cpa-log-qa-style';
    style.textContent = [
      '#cpa-log-qa-button{position:fixed;right:0;top:calc(50% + 90px);z-index:2147483000;border:1px solid color-mix(in srgb,var(--primary-color,#2563eb) 72%,#fff);border-right:0;border-radius:12px 0 0 12px;padding:13px 9px;background:#0f766e;color:#fff;font:650 12px/1.2 system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;letter-spacing:.08em;writing-mode:vertical-rl;box-shadow:0 10px 28px rgba(0,0,0,.2);cursor:pointer;transform:translateY(-50%)}',
      '#cpa-log-qa-overlay[hidden]{display:none!important}',
      '#cpa-log-qa-overlay{position:fixed;inset:0;z-index:2147483001;display:flex;align-items:center;justify-content:center;padding:20px;background:rgba(8,12,20,.58);backdrop-filter:blur(3px);font-family:system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif}',
      '.cpa-lqa-panel{display:flex;flex-direction:column;width:min(1100px,100%);max-height:min(88vh,900px);overflow:hidden;border:1px solid var(--border-color,#d8dee9);border-radius:16px;background:var(--bg-primary,#fff);color:var(--text-primary,#172033);box-shadow:0 28px 80px rgba(0,0,0,.32)}',
      '.cpa-lqa-header{display:flex;align-items:flex-start;justify-content:space-between;gap:16px;padding:20px 22px;border-bottom:1px solid var(--border-color,#d8dee9)}',
      '.cpa-lqa-title{margin:0;font-size:21px;font-weight:750}',
      '.cpa-lqa-subtitle{margin:5px 0 0;color:var(--text-secondary,#64748b);font-size:13px;line-height:1.5}',
      '.cpa-lqa-actions{display:flex;gap:8px}',
      '.cpa-lqa-button,.cpa-lqa-close,.cpa-lqa-link{border:1px solid var(--border-color,#d8dee9);border-radius:9px;background:var(--bg-secondary,#f5f7fa);color:var(--text-primary,#172033);font:600 13px/1.2 inherit;cursor:pointer}',
      '.cpa-lqa-button{padding:8px 12px}.cpa-lqa-close{width:34px;height:34px;font-size:21px}',
      '.cpa-lqa-button-primary{background:#0f766e;border-color:#0f766e;color:#fff}',
      '.cpa-lqa-button:disabled,.cpa-lqa-link:disabled{opacity:.55;cursor:not-allowed}',
      '.cpa-lqa-link{padding:4px 8px;font-size:12px;white-space:nowrap}',
      '.cpa-lqa-body{overflow:auto;padding:18px 22px 24px}',
      '.cpa-lqa-status{min-height:20px;margin-bottom:12px;color:var(--text-secondary,#64748b);font-size:13px}',
      '.cpa-lqa-status[data-kind="error"]{padding:10px 12px;border:1px solid #ef444466;border-radius:9px;background:#ef444414;color:#b91c1c}',
      '.cpa-lqa-status[data-kind="running"]{padding:10px 12px;border:1px solid #0f766e66;border-radius:9px;background:#0f766e14;color:#0f766e}',
      '.cpa-lqa-note{margin:0 0 14px;padding:10px 12px;border-left:3px solid #0f766e;border-radius:6px;background:color-mix(in srgb,#0f766e 10%,transparent);color:var(--text-secondary,#64748b);font-size:12px;line-height:1.6}',
      '.cpa-lqa-summary{display:grid;grid-template-columns:repeat(auto-fit,minmax(140px,1fr));gap:10px;margin-bottom:14px}',
      '.cpa-lqa-card{padding:12px 14px;border:1px solid var(--border-color,#d8dee9);border-radius:12px;background:var(--bg-secondary,#f8fafc)}',
      '.cpa-lqa-card span{display:block}.cpa-lqa-card .label{color:var(--text-secondary,#64748b);font-size:12px}.cpa-lqa-card .value{margin-top:5px;font-size:18px;font-weight:750;font-variant-numeric:tabular-nums}',
      '.cpa-lqa-filters{display:flex;flex-wrap:wrap;gap:8px;margin:0 0 12px}',
      '.cpa-lqa-filters select,.cpa-lqa-filters input{border:1px solid var(--border-color,#d8dee9);border-radius:8px;padding:7px 10px;background:var(--bg-primary,#fff);color:inherit}',
      '.cpa-lqa-table{width:100%;border-collapse:collapse;font-size:12px}',
      '.cpa-lqa-table th,.cpa-lqa-table td{border-bottom:1px solid var(--border-color,#d8dee9);padding:8px 6px;text-align:left;vertical-align:top}',
      '.cpa-lqa-table th{color:var(--text-secondary,#64748b);font-weight:650}',
      '.cpa-lqa-fail{color:#b91c1c}.cpa-lqa-pass{color:#047857}',
      '.cpa-lqa-hold{color:#b45309;font-weight:650}.cpa-lqa-eligible{color:#047857;font-weight:650}.cpa-lqa-orphan{color:#64748b;font-weight:650}',
      '.cpa-lqa-mono{font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;word-break:break-all}',
      '.cpa-lqa-title-cell{max-width:220px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;cursor:default}',
      '.cpa-lqa-time{white-space:nowrap;font-variant-numeric:tabular-nums;color:var(--text-secondary,#64748b)}',
    ].join('');
    document.head.appendChild(style);
  }

  function ensureUI() {
    if (ui) {
      return ui;
    }
    addStyles();
    var launcher = element('button', '', '日志质检');
    launcher.id = 'cpa-log-qa-button';
    launcher.type = 'button';
    launcher.setAttribute('aria-label', '日志质检');
    launcher.hidden = true;
    launcher.addEventListener('click', openOverlay);

    var overlay = element('div', '');
    overlay.id = 'cpa-log-qa-overlay';
    overlay.hidden = true;

    var panel = element('div', 'cpa-lqa-panel');
    var header = element('div', 'cpa-lqa-header');
    var titles = element('div', '');
    titles.appendChild(element('h2', 'cpa-lqa-title', '日志质检'));
    titles.appendChild(
      element(
        'p',
        'cpa-lqa-subtitle',
        '本地未上传请求日志质检（不拦截上传）。「上传」列对照 session-gate：Hold / 可上传 / 无 Session'
      )
    );
    var actions = element('div', 'cpa-lqa-actions');
    var runNow = element('button', 'cpa-lqa-button cpa-lqa-button-primary', '立即质检');
    runNow.type = 'button';
    runNow.addEventListener('click', triggerRun);
    var refresh = element('button', 'cpa-lqa-button', '刷新');
    refresh.type = 'button';
    refresh.addEventListener('click', function () {
      loadData();
    });
    var close = element('button', 'cpa-lqa-close', '×');
    close.type = 'button';
    close.setAttribute('aria-label', '关闭');
    close.addEventListener('click', closeOverlay);
    actions.appendChild(runNow);
    actions.appendChild(refresh);
    actions.appendChild(close);
    header.appendChild(titles);
    header.appendChild(actions);

    var body = element('div', 'cpa-lqa-body');
    var status = element('div', 'cpa-lqa-status');
    var content = element('div', '');
    body.appendChild(status);
    body.appendChild(content);
    panel.appendChild(header);
    panel.appendChild(body);
    overlay.appendChild(panel);
    overlay.addEventListener('click', function (event) {
      if (event.target === overlay) {
        closeOverlay();
      }
    });

    document.body.appendChild(launcher);
    document.body.appendChild(overlay);
    ui = {
      launcher: launcher,
      overlay: overlay,
      status: status,
      content: content,
      refresh: refresh,
      runNow: runNow,
      statusFilter: 'fail',
      eligibilityFilter: 'all',
      reasonFilter: '',
      query: '',
      runId: '',
      // Empty = always follow latest; set when user picks a historical batch.
      selectedRunId: '',
      availableRuns: [],
    };
    setRunControls(false);
    return ui;
  }

  function openOverlay() {
    ensureUI();
    previousBodyOverflow = document.body.style.overflow;
    document.body.style.overflow = 'hidden';
    ui.overlay.hidden = false;
    loadData();
  }

  function closeOverlay() {
    if (!ui) {
      return;
    }
    ui.overlay.hidden = true;
    document.body.style.overflow = previousBodyOverflow;
    stopPolling();
  }

  function apiURL(path) {
    if (!capturedAuth) {
      return path;
    }
    return capturedAuth.apiRoot.replace(/\/v0\/management$/, '') + path;
  }

  function authHeaders() {
    var headers = {};
    if (capturedAuth) {
      headers[capturedAuth.headerName] = capturedAuth.headerValue;
    }
    return headers;
  }

  function authedFetch(path, options) {
    var init = options || {};
    var headers = authHeaders();
    if (init.headers) {
      Object.keys(init.headers).forEach(function (key) {
        headers[key] = init.headers[key];
      });
    }
    return nativeFetch(apiURL(path), {
      method: init.method || 'GET',
      headers: headers,
      body: init.body,
    }).then(function (response) {
      if (!response.ok) {
        var status = Number(response.status || 0);
        if (status === 401 || status === 403) {
          throw new Error('认证已失效，请刷新管理页并重新登录。');
        }
        return response
          .json()
          .catch(function () {
            return null;
          })
          .then(function (payload) {
            var message =
              (payload && (payload.error || payload.message)) ||
              '请求失败（HTTP ' + status + '）。';
            var err = new Error(message);
            err.status = status;
            err.payload = payload;
            throw err;
          });
      }
      var contentType = String(response.headers.get('content-type') || '');
      if (init.raw) {
        return response;
      }
      if (contentType.indexOf('application/json') >= 0 || contentType.indexOf('+json') >= 0) {
        return response.json();
      }
      return response;
    });
  }

  function setRunControls(running) {
    runInProgress = !!running;
    if (!ui) {
      return;
    }
    if (ui.runNow) {
      ui.runNow.disabled = runInProgress;
      ui.runNow.textContent = runInProgress ? '质检中…' : '立即质检';
    }
  }

  function stopPolling() {
    if (pollTimer) {
      clearInterval(pollTimer);
      pollTimer = null;
    }
  }

  function startPolling() {
    stopPolling();
    pollTimer = setInterval(function () {
      if (!ui || ui.overlay.hidden) {
        stopPolling();
        return;
      }
      authedFetch(STATUS_ENDPOINT)
        .then(function (statusPayload) {
          var running = !!(statusPayload && statusPayload.running);
          setRunControls(running);
          if (running) {
            ui.status.textContent = (statusPayload && statusPayload.message) || '质检进行中…';
            ui.status.dataset.kind = 'running';
            return;
          }
          stopPolling();
          loadData();
        })
        .catch(function () {
          // Keep polling; transient errors should not unlock the button early.
        });
    }, POLL_INTERVAL_MS);
  }

  function triggerRun() {
    if (!capturedAuth) {
      ui.status.textContent = '请先登录管理页，再打开日志质检。';
      ui.status.dataset.kind = 'error';
      return;
    }
    if (runInProgress) {
      return;
    }
    setRunControls(true);
    // New run becomes latest; clear pin so the panel follows the new report.
    ui.selectedRunId = '';
    ui.status.textContent = '正在启动质检…';
    ui.status.dataset.kind = 'running';
    authedFetch(RUN_ENDPOINT, { method: 'POST' })
      .then(function (payload) {
        ui.status.textContent = (payload && payload.message) || '质检已开始';
        ui.status.dataset.kind = 'running';
        startPolling();
      })
      .catch(function (err) {
        var status = Number(err && err.status ? err.status : 0);
        if (status === 409) {
          setRunControls(true);
          ui.status.textContent = String(err && err.message ? err.message : '质检正在进行中');
          ui.status.dataset.kind = 'running';
          startPolling();
          return;
        }
        setRunControls(false);
        ui.status.textContent = String(err && err.message ? err.message : err);
        ui.status.dataset.kind = 'error';
      });
  }

  function downloadFailedSession(sessionId, button) {
    if (!sessionId || !capturedAuth) {
      return;
    }
    if (button) {
      button.disabled = true;
      button.textContent = '下载中…';
    }
    var path =
      SESSION_LOGS_ENDPOINT +
      '?session_id=' +
      encodeURIComponent(sessionId) +
      (ui.runId ? '&run_id=' + encodeURIComponent(ui.runId) : '');
    authedFetch(path, { raw: true })
      .then(function (response) {
        var disposition = String(response.headers.get('content-disposition') || '');
        var filename = 'log-qa-fail-' + sessionId + '.zip';
        var match = /filename="?([^"]+)"?/i.exec(disposition);
        if (match && match[1]) {
          filename = match[1];
        }
        return response.blob().then(function (blob) {
          var url = URL.createObjectURL(blob);
          var anchor = document.createElement('a');
          anchor.href = url;
          anchor.download = filename;
          document.body.appendChild(anchor);
          anchor.click();
          anchor.remove();
          setTimeout(function () {
            URL.revokeObjectURL(url);
          }, 1000);
        });
      })
      .catch(function (err) {
        ui.status.textContent = String(err && err.message ? err.message : err);
        ui.status.dataset.kind = 'error';
      })
      .then(function () {
        if (button) {
          button.disabled = false;
          button.textContent = '下载日志';
        }
      });
  }

  function pct(rate) {
    if (typeof rate !== 'number' || isNaN(rate)) {
      return '-';
    }
    return (rate * 100).toFixed(1) + '%';
  }

  function reasonLabel(code) {
    switch (String(code || '')) {
      case 'prompt_rounds':
        return '有效提问轮次不足';
      case 'no_tool_call':
        return '无工具调用';
      case 'duplicate_assistant':
        return '助手回复重复';
      default:
        return code || '';
    }
  }

  function eligibilityLabel(code) {
    switch (String(code || '').toLowerCase()) {
      case 'eligible':
        return '可上传';
      case 'hold':
        return 'Hold';
      case 'orphan':
        return '无 Session';
      default:
        return code || '-';
    }
  }

  function eligibilityClass(code) {
    switch (String(code || '').toLowerCase()) {
      case 'eligible':
        return 'cpa-lqa-eligible';
      case 'hold':
        return 'cpa-lqa-hold';
      case 'orphan':
        return 'cpa-lqa-orphan';
      default:
        return '';
    }
  }

  function formatFailReasons(reasons) {
    if (!reasons || !reasons.length) {
      return '';
    }
    return reasons
      .map(function (reason) {
        var text = String(reason || '');
        if (text.indexOf('prompt_rounds') === 0) {
          return '有效提问轮次不足';
        }
        if (text === 'no_tool_call') {
          return '无工具调用';
        }
        if (text.indexOf('duplicate_assistant') === 0) {
          return '助手回复重复';
        }
        return text;
      })
      .join('，');
  }

  // Format an RFC3339 timestamp as Asia/Shanghai wall clock for the table.
  function formatSessionTime(value) {
    if (!value) {
      return '-';
    }
    var d = new Date(value);
    if (isNaN(d.getTime())) {
      return String(value);
    }
    // Convert to Beijing by shifting UTC then reading UTC fields.
    var bj = new Date(d.getTime() + 8 * 60 * 60 * 1000);
    function pad(n) {
      return n < 10 ? '0' + n : String(n);
    }
    return (
      bj.getUTCFullYear() +
      '-' +
      pad(bj.getUTCMonth() + 1) +
      '-' +
      pad(bj.getUTCDate()) +
      ' ' +
      pad(bj.getUTCHours()) +
      ':' +
      pad(bj.getUTCMinutes()) +
      ':' +
      pad(bj.getUTCSeconds())
    );
  }

  function sessionTimeTooltip(row) {
    var first = formatSessionTime(row && row.first_ts);
    var last = formatSessionTime(row && row.last_ts);
    if (first === '-' && last === '-') {
      return '';
    }
    if (first === last || last === '-') {
      return '开始：' + first + '（北京时间）';
    }
    return '开始：' + first + '（北京时间）\n最近活动：' + last + '（北京时间）';
  }

  // Display run batch in Asia/Shanghai (北京时间). Accepts both legacy UTC ids
  // (…Z) and new offset ids (…+0800 / …-0700 layout).
  function formatRunBatch(runId) {
    var raw = String(runId || '').trim();
    if (!raw || raw === '-') {
      return '-';
    }
    var m = /^(\d{4})-(\d{2})-(\d{2})T(\d{2})-(\d{2})-(\d{2})(Z|[+-]\d{4})?$/.exec(raw);
    if (!m) {
      return raw;
    }
    var year = Number(m[1]);
    var month = Number(m[2]);
    var day = Number(m[3]);
    var hour = Number(m[4]);
    var minute = Number(m[5]);
    var second = Number(m[6]);
    var zone = m[7] || 'Z';
    var utcMs;
    if (zone === 'Z') {
      utcMs = Date.UTC(year, month - 1, day, hour, minute, second);
    } else {
      var sign = zone.charAt(0) === '-' ? -1 : 1;
      var offHour = Number(zone.slice(1, 3));
      var offMin = Number(zone.slice(3, 5));
      var offsetMin = sign * (offHour * 60 + offMin);
      utcMs = Date.UTC(year, month - 1, day, hour, minute, second) - offsetMin * 60 * 1000;
    }
    // Format as Beijing wall time (UTC+8).
    var bj = new Date(utcMs + 8 * 60 * 60 * 1000);
    function pad(n) {
      return n < 10 ? '0' + n : String(n);
    }
    return (
      bj.getUTCFullYear() +
      '-' +
      pad(bj.getUTCMonth() + 1) +
      '-' +
      pad(bj.getUTCDate()) +
      ' ' +
      pad(bj.getUTCHours()) +
      ':' +
      pad(bj.getUTCMinutes()) +
      ':' +
      pad(bj.getUTCSeconds()) +
      '（北京时间）'
    );
  }

  function renderEmpty(message) {
    ui.content.replaceChildren();
    ui.content.appendChild(
      element('p', 'cpa-lqa-note', message || '尚无质检报告。请先启动 log-qa 服务。')
    );
  }

  function render(summaryPayload, sessionsPayload, runsPayload) {
    ui.content.replaceChildren();
    ui.content.appendChild(
      element(
        'p',
        'cpa-lqa-note',
        '仅检查本地尚未上传的 .log 文件。已上传或已删除的日志不会纳入。质检不会拦截或修改上传服务。' +
          '「上传」列对照 session-gate（Hold / 可上传 / 无 Session）；「助手回复重复」只影响质检状态，不标为 Hold。' +
          '判定标准：有效提问轮次默认需 ≥ 4（非「不足 8 轮」）；下方数字是失败会话个数，不是轮次阈值。' +
          '历史批次保存在 work-dir/reports（默认最多 48 轮），可用下方「历史批次」切换查看。'
      )
    );

    var runs = (runsPayload && runsPayload.runs) || ui.availableRuns || [];
    ui.availableRuns = runs;

    if (!summaryPayload || !summaryPayload.has_report || !summaryPayload.summary) {
      // Still show run picker if historical reports exist but selected one is missing.
      if (runs.length) {
        var emptyFilters = element('div', 'cpa-lqa-filters');
        emptyFilters.appendChild(buildRunFilterRow(runs, null));
        ui.content.appendChild(emptyFilters);
      }
      ui.content.appendChild(
        element(
          'p',
          'cpa-lqa-note',
          (summaryPayload && summaryPayload.message) || '尚无质检报告。'
        )
      );
      return;
    }

    var s = summaryPayload.summary;
    var cards = element('div', 'cpa-lqa-summary');
    function card(label, value) {
      var node = element('div', 'cpa-lqa-card');
      node.appendChild(element('span', 'label', label));
      node.appendChild(element('span', 'value', value));
      cards.appendChild(node);
    }
    card('合格率', pct(s.pass_rate));
    card('会话数', String(s.sessions_total || 0));
    card('通过', String(s.sessions_pass || 0));
    card('失败', String(s.sessions_fail || 0));
    card('扫描文件数', String(s.files_scanned || 0));
    card('部分扫描', s.partial ? '是' : '否');
    ui.content.appendChild(cards);

    var hist = s.fail_reason_hist || {};
    ui.content.appendChild(
      element(
        'p',
        'cpa-lqa-status',
        '失败会话数（按原因）— 有效提问轮次不足：' +
          (hist.prompt_rounds || 0) +
          ' 个，无工具调用：' +
          (hist.no_tool_call || 0) +
          ' 个，助手回复重复：' +
          (hist.duplicate_assistant || 0) +
          ' 个 | 当前查看批次：' +
          formatRunBatch(s.run_id) +
          (runs.length ? '（共 ' + runs.length + ' 个历史批次）' : '')
      )
    );

    var filters = element('div', 'cpa-lqa-filters');
    filters.appendChild(buildRunFilterRow(runs, s.run_id));

    var statusSelect = document.createElement('select');
    ;[
      ['fail', '失败'],
      ['pass', '通过'],
      ['all', '全部'],
    ].forEach(function (pair) {
      var opt = document.createElement('option');
      opt.value = pair[0];
      opt.textContent = pair[1];
      if (pair[0] === ui.statusFilter) {
        opt.selected = true;
      }
      statusSelect.appendChild(opt);
    });
    statusSelect.addEventListener('change', function () {
      ui.statusFilter = statusSelect.value;
      loadData();
    });
    var eligibilitySelect = document.createElement('select');
    ;[
      ['all', '全部上传状态'],
      ['hold', 'Hold'],
      ['eligible', '可上传'],
      ['orphan', '无 Session'],
    ].forEach(function (pair) {
      var opt = document.createElement('option');
      opt.value = pair[0];
      opt.textContent = pair[1];
      if (pair[0] === (ui.eligibilityFilter || 'all')) {
        opt.selected = true;
      }
      eligibilitySelect.appendChild(opt);
    });
    eligibilitySelect.addEventListener('change', function () {
      ui.eligibilityFilter = eligibilitySelect.value;
      loadData();
    });
    var reasonSelect = document.createElement('select');
    ;[
      ['', '全部原因'],
      ['prompt_rounds', reasonLabel('prompt_rounds')],
      ['no_tool_call', reasonLabel('no_tool_call')],
      ['duplicate_assistant', reasonLabel('duplicate_assistant')],
    ].forEach(function (pair) {
      var opt = document.createElement('option');
      opt.value = pair[0];
      opt.textContent = pair[1];
      if (pair[0] === ui.reasonFilter) {
        opt.selected = true;
      }
      reasonSelect.appendChild(opt);
    });
    reasonSelect.addEventListener('change', function () {
      ui.reasonFilter = reasonSelect.value;
      loadData();
    });
    var search = document.createElement('input');
    search.type = 'search';
    search.placeholder = '会话 / 标题 / Key';
    search.value = ui.query || '';
    search.addEventListener('change', function () {
      ui.query = search.value;
      loadData();
    });
    filters.appendChild(statusSelect);
    filters.appendChild(eligibilitySelect);
    filters.appendChild(reasonSelect);
    filters.appendChild(search);
    ui.content.appendChild(filters);

    var table = element('table', 'cpa-lqa-table');
    var thead = document.createElement('thead');
    var headRow = document.createElement('tr');
    ;['状态', '上传', '会话 ID', '标题', '开始时间', '提问轮次', '工具调用', '重复回复', '失败原因', 'Key', '操作'].forEach(function (h) {
      headRow.appendChild(element('th', '', h));
    });
    thead.appendChild(headRow);
    table.appendChild(thead);
    var tbody = document.createElement('tbody');
    var sessions = (sessionsPayload && sessionsPayload.sessions) || [];
    if (!sessions.length) {
      var emptyRow = document.createElement('tr');
      var td = element('td', '', '当前筛选条件下无会话');
      td.colSpan = 11;
      emptyRow.appendChild(td);
      tbody.appendChild(emptyRow);
    }
    sessions.forEach(function (row) {
      var tr = document.createElement('tr');
      tr.appendChild(element('td', row.ok ? 'cpa-lqa-pass' : 'cpa-lqa-fail', row.ok ? '通过' : '失败'));
      var eligibility = row.upload_eligibility || '';
      var eligibilityCell = element('td', eligibilityClass(eligibility), eligibilityLabel(eligibility));
      if (eligibility === 'eligible' && !row.ok) {
        eligibilityCell.title = '质检未通过（如助手回复重复），但不阻挡 session-gate 上传';
      } else if (eligibility === 'hold') {
        eligibilityCell.title = '对照 session-gate：预计 Hold，暂不上传';
      } else if (eligibility === 'orphan') {
        eligibilityCell.title = '无真实 session_id，按 orphan 处理';
      } else if (eligibility === 'eligible') {
        eligibilityCell.title = '对照 session-gate：预计可上传';
      }
      tr.appendChild(eligibilityCell);
      tr.appendChild(element('td', 'cpa-lqa-mono', row.session_id || ''));

      var titleText = row.title || '-';
      var titleCell = element('td', 'cpa-lqa-title-cell', titleText);
      var titleTips = [];
      if (row.title) {
        titleTips.push(row.title);
      }
      if (row.title_source === 'user_prompt') {
        titleTips.push('来源：首条有效用户提问（未找到 Codex 标题生成结果）');
      } else if (row.title_source === 'codex_title') {
        titleTips.push('来源：Codex 会话标题生成请求');
      }
      if (titleTips.length) {
        titleCell.title = titleTips.join('\n\n');
      }
      tr.appendChild(titleCell);

      var timeCell = element('td', 'cpa-lqa-time', formatSessionTime(row.first_ts));
      var timeTip = sessionTimeTooltip(row);
      if (timeTip) {
        timeCell.title = timeTip;
      }
      tr.appendChild(timeCell);

      tr.appendChild(element('td', '', String(row.prompt_rounds)));
      tr.appendChild(element('td', '', String(row.tool_calls)));
      tr.appendChild(element('td', '', String(row.dup_assistant_groups)));
      tr.appendChild(element('td', '', formatFailReasons(row.fail_reasons)));
      tr.appendChild(element('td', '', (row.key_names || []).join('，')));
      var actionCell = document.createElement('td');
      if (!row.ok && row.session_id) {
        var downloadBtn = element('button', 'cpa-lqa-link', '下载日志');
        downloadBtn.type = 'button';
        downloadBtn.title = '下载该失败会话的完整源日志（zip）';
        downloadBtn.addEventListener('click', function () {
          downloadFailedSession(row.session_id, downloadBtn);
        });
        actionCell.appendChild(downloadBtn);
      } else {
        actionCell.textContent = '-';
      }
      tr.appendChild(actionCell);
      tbody.appendChild(tr);
    });
    table.appendChild(tbody);
    ui.content.appendChild(table);
    if (sessionsPayload && typeof sessionsPayload.total === 'number') {
      ui.content.appendChild(
        element(
          'p',
          'cpa-lqa-status',
          '显示 ' + sessions.length + ' / ' + sessionsPayload.total + ' 条匹配会话'
        )
      );
    }
  }

  function buildRunFilterRow(runs, currentRunId) {
    var wrap = document.createElement('span');
    wrap.style.display = 'inline-flex';
    wrap.style.alignItems = 'center';
    wrap.style.gap = '6px';

    var label = element('span', '', '历史批次');
    label.style.fontSize = '12px';
    label.style.color = 'var(--text-secondary,#64748b)';

    var runSelect = document.createElement('select');
    runSelect.title = '切换查看历史质检报告（磁盘 work-dir/reports，默认最多保留 48 轮）';

    var latestOpt = document.createElement('option');
    latestOpt.value = '';
    latestOpt.textContent = runs.length
      ? '最新（' + formatRunBatch((runs[0] && runs[0].run_id) || currentRunId || '') + '）'
      : '最新';
    if (!ui.selectedRunId) {
      latestOpt.selected = true;
    }
    runSelect.appendChild(latestOpt);

    runs.forEach(function (run, index) {
      var id = run && run.run_id ? run.run_id : '';
      if (!id) {
        return;
      }
      var opt = document.createElement('option');
      opt.value = id;
      opt.textContent =
        formatRunBatch(id) + (index === 0 ? ' · 最新' : '') + ' · ' + id;
      if (ui.selectedRunId && ui.selectedRunId === id) {
        opt.selected = true;
      }
      runSelect.appendChild(opt);
    });

    runSelect.addEventListener('change', function () {
      ui.selectedRunId = runSelect.value || '';
      loadData();
    });

    wrap.appendChild(label);
    wrap.appendChild(runSelect);
    return wrap;
  }

  function loadData() {
    if (!capturedAuth) {
      ui.status.textContent = '请先登录管理页，再打开日志质检。';
      ui.status.dataset.kind = 'error';
      return;
    }
    ui.status.textContent = '加载中…';
    delete ui.status.dataset.kind;
    ui.refresh.disabled = true;

    var runQuery = ui.selectedRunId
      ? 'run_id=' + encodeURIComponent(ui.selectedRunId)
      : '';
    var summaryQuery = SUMMARY_ENDPOINT + (runQuery ? '?' + runQuery : '');
    var sessionsQuery =
      SESSIONS_ENDPOINT +
      '?status=' +
      encodeURIComponent(ui.statusFilter || 'fail') +
      '&limit=50' +
      (ui.eligibilityFilter && ui.eligibilityFilter !== 'all'
        ? '&eligibility=' + encodeURIComponent(ui.eligibilityFilter)
        : '') +
      (ui.reasonFilter ? '&reason=' + encodeURIComponent(ui.reasonFilter) : '') +
      (ui.query ? '&q=' + encodeURIComponent(ui.query) : '') +
      (runQuery ? '&' + runQuery : '');

    Promise.all([
      authedFetch(summaryQuery),
      authedFetch(sessionsQuery),
      authedFetch(STATUS_ENDPOINT),
      authedFetch(RUNS_ENDPOINT),
    ])
      .then(function (parts) {
        ui.refresh.disabled = false;
        var statusPayload = parts[2] || {};
        var runsPayload = parts[3] || {};
        var running = !!statusPayload.running;
        setRunControls(running);
        if (parts[1] && parts[1].run_id) {
          ui.runId = parts[1].run_id;
        } else if (statusPayload.latest_run_id) {
          ui.runId = statusPayload.latest_run_id;
        }
        // If user pinned a run that was GC'd, fall back to latest.
        if (ui.selectedRunId) {
          var stillThere = ((runsPayload.runs || []) || []).some(function (r) {
            return r && r.run_id === ui.selectedRunId;
          });
          if (!stillThere && (runsPayload.runs || []).length) {
            ui.selectedRunId = '';
          }
        }
        if (running) {
          ui.status.textContent = statusPayload.message || '质检进行中…';
          ui.status.dataset.kind = 'running';
          if (!pollTimer) {
            startPolling();
          }
        } else {
          ui.status.textContent = statusPayload.message || '正常';
          delete ui.status.dataset.kind;
        }
        render(parts[0], parts[1], runsPayload);
      })
      .catch(function (err) {
        ui.refresh.disabled = false;
        ui.status.textContent = String(err && err.message ? err.message : err);
        ui.status.dataset.kind = 'error';
      });
  }

  installFetchInterceptor();
  installXHRInterceptor();
})();
