"""CLI entrypoint for the usage dashboard."""
import argparse
import datetime as dt
import json
import threading

from . import config as cfg_mod
from . import storage as st
from . import collector as col
from . import server as srv
from . import query as qy
from .collector import COLLECTOR_STATE


def _init(cfg):
    version = st.init_schema(cfg)
    with COLLECTOR_STATE.lock:
        COLLECTOR_STATE.schema_version = version
    return version


def cmd_init(cfg):
    version = _init(cfg)
    print(cfg_mod.db_path_for(cfg))
    print(f"schema_version={version}")


def cmd_collect(cfg):
    _init(cfg)
    with col.CollectorLock(cfg):
        col.collect_forever(cfg)


def cmd_serve(cfg):
    srv.serve(cfg)


def cmd_run(cfg):
    _init(cfg)
    lock = col.CollectorLock(cfg)
    lock.acquire()
    try:
        stop = threading.Event()
        t = threading.Thread(target=col.collect_forever, args=(cfg, stop), daemon=True)
        t.start()
        try:
            srv.serve(cfg)
        finally:
            stop.set()
            t.join(timeout=5)
    finally:
        lock.release()


def cmd_report(cfg, args):
    _init(cfg)
    if args.from_ts or args.to_ts:
        qs = {}
        if args.from_ts:
            qs["from"] = [args.from_ts]
        if args.to_ts:
            qs["to"] = [args.to_ts]
    else:
        qs = {"range": [args.range]}
    print(json.dumps(qy.query_summary(cfg, qs), ensure_ascii=False, indent=2))


def cmd_compact(cfg, args):
    _init(cfg)
    cutoff = dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=args.days)
    with st.db_connect(cfg) as conn:
        cur = conn.execute("DELETE FROM usage_events WHERE ts_epoch < ?", (cutoff.timestamp(),))
    print(f"deleted {cur.rowcount} rows older than {args.days} days")


def build_parser():
    parser = argparse.ArgumentParser(description="CLIProxyAPI usage dashboard")
    sub = parser.add_subparsers(dest="cmd", required=True)
    sub.add_parser("init")
    sub.add_parser("collect")
    sub.add_parser("serve")
    sub.add_parser("run")
    rep = sub.add_parser("report")
    rep.add_argument("range", nargs="?", default="today", choices=sorted(qy.PRESETS))
    rep.add_argument("--from", dest="from_ts", metavar="FROM")
    rep.add_argument("--to", dest="to_ts", metavar="TO")
    comp = sub.add_parser("compact")
    comp.add_argument("--days", type=int, default=90)
    sub.add_parser("quota")
    return parser


def main(argv=None):
    parser = build_parser()
    args = parser.parse_args(argv)
    cfg = cfg_mod.load_config()
    if args.cmd == "init":
        cmd_init(cfg)
    elif args.cmd == "collect":
        cmd_collect(cfg)
    elif args.cmd == "serve":
        cmd_serve(cfg)
    elif args.cmd == "run":
        cmd_run(cfg)
    elif args.cmd == "report":
        cmd_report(cfg, args)
    elif args.cmd == "compact":
        cmd_compact(cfg, args)
    elif args.cmd == "quota":
        print(json.dumps({"note": "quota feature disabled by default; set quota_enabled=true"}))


if __name__ == "__main__":
    main()