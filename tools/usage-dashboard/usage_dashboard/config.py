"""Configuration loading and path helpers for the usage dashboard."""
import datetime as _dt
import json
import os
from zoneinfo import ZoneInfo

DEFAULT_DATA_DIR = os.path.expanduser("~/.cli-proxy-api/usage-dashboard")

DEFAULT_CONFIG = {
    "cliproxy_base_url": "http://127.0.0.1:8317",
    "management_key": "",
    "poll_interval_seconds": 2,
    "batch_size": 100,
    "dashboard_host": "127.0.0.1",
    "dashboard_port": 8320,
    "dashboard_token": "",
    "quota_enabled": False,
    "quota_refresh_seconds": 300,
    "data_dir": DEFAULT_DATA_DIR,
    "default_limit": 100,
    "max_limit": 500,
    "health_stale_seconds": 300,
}

UTC = _dt.timezone.utc
LOCAL_TZ = ZoneInfo(os.environ.get("USAGE_DASHBOARD_TZ", "Asia/Shanghai"))

_ENV_MAP = {
    "CLIPROXY_BASE_URL": "cliproxy_base_url",
    "CLIPROXY_MANAGEMENT_KEY": "management_key",
    "POLL_INTERVAL_SECONDS": "poll_interval_seconds",
    "BATCH_SIZE": "batch_size",
    "DASHBOARD_HOST": "dashboard_host",
    "DASHBOARD_PORT": "dashboard_port",
    "DASHBOARD_TOKEN": "dashboard_token",
    "QUOTA_ENABLED": "quota_enabled",
    "USAGE_DASHBOARD_DATA_DIR": "data_dir",
}

_INT_KEYS = ("poll_interval_seconds", "batch_size", "dashboard_port")


def data_dir_for(cfg):
    return cfg["data_dir"]


def db_path_for(cfg):
    return os.path.join(data_dir_for(cfg), "usage.sqlite")


def config_path_for(cfg):
    return os.path.join(data_dir_for(cfg), "config.json")


def pricing_path_for(cfg):
    return os.path.join(data_dir_for(cfg), "pricing.json")


def ensure_dirs(cfg):
    os.makedirs(data_dir_for(cfg), exist_ok=True)


def _coerce(key, raw):
    if key in _INT_KEYS:
        return int(raw)
    if key == "quota_enabled":
        return raw.lower() in ("1", "true", "yes", "on")
    return raw


def load_config():
    """Merge defaults < config file < environment, validating each layer."""
    cfg = dict(DEFAULT_CONFIG)
    env_dir = os.environ.get("USAGE_DASHBOARD_DATA_DIR")
    if env_dir:
        cfg["data_dir"] = env_dir
    ensure_dirs(cfg)
    path = config_path_for(cfg)
    if not os.path.exists(path):
        with open(path, "w", encoding="utf-8") as f:
            json.dump(DEFAULT_CONFIG, f, indent=2)
        os.chmod(path, 0o600)
    else:
        try:
            with open(path, encoding="utf-8") as f:
                loaded = json.load(f)
            if not isinstance(loaded, dict):
                raise ValueError("config root is not an object")
            cfg.update(loaded)
        except (OSError, json.JSONDecodeError, ValueError) as exc:
            raise SystemExit(f"config file {path} is unreadable or invalid: {exc}") from exc
    for env_key, cfg_key in _ENV_MAP.items():
        val = os.environ.get(env_key)
        if val is None or val == "":
            continue
        try:
            cfg[cfg_key] = _coerce(cfg_key, val)
        except ValueError:
            continue
    return cfg