"""CLI entrypoint for the usage dashboard."""
import argparse
import asyncio
import datetime as dt
import json
import logging

import uvicorn

from . import config as cfg_mod
from . import collector as ca
from . import query as qy
from . import storage as st
from .api import app

log = logging.getLogger(__name__)


def _init(cfg):
    version = st.init_schema(cfg)
    logging.basicConfig(level=logging.INFO, format="%(asctime)s %(levelname)s %(name)s %(message)s")
    return version


def cmd_init(cfg):
    version = _init(cfg)
    print(cfg_mod.db_path_for(cfg))
    print(f"schema_version={version}")


def cmd_serve(cfg):
    _init(cfg)
    app.state.cfg = cfg
    uvicorn.run(app, host=cfg["dashboard_host"], port=int(cfg["dashboard_port"]),
                log_config=None)


def cmd_run(cfg):
    _init(cfg)
    app.state.cfg = cfg
    uvicorn.run(app, host=cfg["dashboard_host"], port=int(cfg["dashboard_port"]),
                log_config=None)


def cmd_collect(cfg):
    _init(cfg)
    asyncio.run(_collect_standalone(cfg))


async def _collect_standalone(cfg):
    async with ca.AsyncCollectorLock(cfg):
        await ca.collect_forever(cfg)


def cmd_report(cfg, args):
    _init(cfg)
    qs = {"range": [args.range] if args.range else [],
          "from": [args.from_ts] if args.from_ts else [],
          "to": [args.to_ts] if args.to_ts else []}
    print(json.dumps(qy.query_summary(cfg, qs), ensure_ascii=False, indent=2))


def cmd_compact(cfg, args):
    _init(cfg)
    with st.db_connect(cfg) as conn:
        cur = conn.execute(
            "DELETE FROM usage_events WHERE ts_epoch < ?",
            ((dt.datetime.now(dt.timezone.utc) - dt.timedelta(days=args.days)).timestamp(),),
        )
        conn.commit()
        print(f"deleted {cur.rowcount} rows older than {args.days} days")


def cmd_quota(cfg, args):
    if not cfg.get("quota_enabled"):
        print(json.dumps({"note": "quota feature disabled by default; set quota_enabled=true"}))
        return
    # Future: implement async quota refresh


def build_parser():
    p = argparse.ArgumentParser(description="CLIProxyAPI usage dashboard")
    sub = p.add_subparsers(dest="cmd", required=True)
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
    return p


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
        cmd_quota(cfg, args)


if __name__ == "__main__":
    main()