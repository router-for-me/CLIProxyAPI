"""HTTP server: auth, routing, and stable error responses."""
import json
import os
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlparse

from . import collector as col
from . import config as cfg_mod
from . import query as qy
from . import storage as st


def json_response(handler, payload, status=200):
    body = json.dumps(payload, ensure_ascii=False).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json; charset=utf-8")
    handler.send_header("Content-Length", str(len(body)))
    handler.send_header("Cache-Control", "no-store")
    handler.send_header("X-Content-Type-Options", "nosniff")
    handler.end_headers()
    handler.wfile.write(body)


def is_authorized(handler, cfg):
    token = cfg.get("dashboard_token") or ""
    if not token:
        return True
    ah = handler.headers.get("Authorization", "")
    if ah.startswith("Bearer "):
        return ah[len("Bearer "):].strip() == token
    return handler.headers.get("X-Dashboard-Token", "").strip() == token


def is_loopback(host):
    return host in ("127.0.0.1", "::1", "localhost")


def _load_dashboard_html():
    here = os.path.dirname(os.path.abspath(__file__))
    with open(os.path.join(here, "dashboard.html"), encoding="utf-8") as f:
        return f.read()


DASHBOARD_HTML = _load_dashboard_html()
_STATIC_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "static")


def _load_static_bytes(filename):
    """Load a vendored static asset as bytes. Raises FileNotFoundError if missing."""
    path = os.path.join(_STATIC_DIR, filename)
    with open(path, "rb") as f:
        return f.read()


_STATIC_MIME = {
    "chart.4.4.1.min.js": ("application/javascript; charset=utf-8", "public, max-age=31536000, immutable"),
}


def _serve_static(handler, filename):
    try:
        body = _STATIC_MIME_CACHE[filename]
    except KeyError:
        json_response(handler, {"error": "not found"}, 404)
        return
    mime, cache = _STATIC_MIME[filename]
    handler.send_response(200)
    handler.send_header("Content-Type", mime)
    handler.send_header("Content-Length", str(len(body)))
    handler.send_header("Cache-Control", cache)
    handler.send_header("X-Content-Type-Options", "nosniff")
    handler.end_headers()
    handler.wfile.write(body)


_STATIC_MIME_CACHE = {"chart.4.4.1.min.js": _load_static_bytes("chart.4.4.1.min.js")}
_PUBLIC_PATHS = {"/", "/index.html", "/api/v1/auth/check", "/static/chart.js"}


def make_handler(cfg):
    class DashboardHandler(BaseHTTPRequestHandler):
        def log_message(self, fmt, *args):
            return

        def _gate(self):
            host = cfg.get("dashboard_host") or "127.0.0.1"
            if not is_loopback(host) and not cfg.get("dashboard_token"):
                json_response(self, {"error": "non-loopback bind requires dashboard_token"}, 503)
                return False
            if not is_authorized(self, cfg):
                json_response(self, {"error": "unauthorized"}, 401)
                return False
            return True

        def do_GET(self):
            parsed = urlparse(self.path)
            if parsed.path not in _PUBLIC_PATHS:
                if not self._gate():
                    return
            qs = parse_qs(parsed.query, keep_blank_values=True)
            try:
                if parsed.path in ("/", "/index.html"):
                    self._serve_html()
                elif parsed.path == "/api/v1/auth/check":
                    required = bool(cfg.get("dashboard_token"))
                    valid = (not required) or is_authorized(self, cfg)
                    json_response(self, {"auth_required": required, "valid": valid})
                elif parsed.path == "/api/v1/health":
                    snap = col.snapshot(cfg)
                    snap["management_configured"] = bool(cfg.get("management_key"))
                    json_response(self, snap)
                elif parsed.path == "/api/v1/summary":
                    json_response(self, qy.query_summary(cfg, qs))
                elif parsed.path == "/api/v1/timeseries":
                    json_response(self, qy.query_timeseries(cfg, qs))
                elif parsed.path == "/api/v1/models":
                    json_response(self, qy.query_models(cfg, qs))
                elif parsed.path == "/api/v1/accounts":
                    json_response(self, qy.query_accounts(cfg, qs))
                elif parsed.path == "/api/v1/errors":
                    json_response(self, qy.query_errors(cfg, qs))
                elif parsed.path == "/api/v1/prices":
                    json_response(self, qy.query_prices(cfg))
                elif parsed.path == "/api/v1/requests":
                    json_response(self, qy.query_requests(cfg, qs))
                elif parsed.path == "/api/health":
                    json_response(self, {"ok": True, "db_path": cfg_mod.db_path_for(cfg)})
                elif parsed.path == "/api/summary":
                    json_response(self, qy.query_summary(cfg, qs))
                elif parsed.path == "/api/requests":
                    legacy = {"limit": [str(qy.max_limit(cfg, qy._first(qs, "limit")))]}
                    legacy.update({k: v for k, v in qs.items() if k in ("range", "from", "to", "model")})
                    json_response(self, qy.query_requests(cfg, legacy))
                elif parsed.path == "/static/chart.js":
                    _serve_static(self, "chart.4.4.1.min.js")
                elif parsed.path.startswith("/static/"):
                    json_response(self, {"error": "not found"}, 404)
                else:
                    json_response(self, {"error": "not found"}, 404)
            except ValueError as exc:
                msg = str(exc)
                if any(kw in msg.lower() for kw in ("pricing", "interval", "negative", "rate")):
                    json_response(self, {"error": "pricing configuration error"}, 500)
                else:
                    json_response(self, {"error": msg}, 400)
            except Exception:
                json_response(self, {"error": "internal error"}, 500)

        def _serve_html(self):
            body = DASHBOARD_HTML.encode("utf-8")
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(body)))
            self.send_header("X-Content-Type-Options", "nosniff")
            self.end_headers()
            self.wfile.write(body)

    return DashboardHandler


def serve(cfg, ready=None):
    st.init_schema(cfg)
    host = cfg.get("dashboard_host") or "127.0.0.1"
    port = int(cfg.get("dashboard_port") or 8320)
    if not is_loopback(host) and not cfg.get("dashboard_token"):
        raise SystemExit(
            "Refusing to bind non-loopback address without dashboard_token. "
            "Set dashboard_token (or DASHBOARD_TOKEN) or use 127.0.0.1."
        )
    server = ThreadingHTTPServer((host, port), make_handler(cfg))
    print(f"dashboard listening on http://{host}:{port}", flush=True)
    if ready is not None:
        ready.set()
    try:
        server.serve_forever()
    finally:
        server.shutdown()