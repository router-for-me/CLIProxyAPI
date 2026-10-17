#!/usr/bin/env python3
"""Copy claw-master's metric history from its names before the rename to its names after.

Every stream in the source namespace (ClaudeMaster) is read minute by minute and written to the
target namespace (ClawMaster) with its original timestamps, as statistic sets (sample count, sum,
minimum, maximum), so every statistic of the copy equals the original's:

- the metric name claude_master.* becomes claw_master.* (the host's mem_used_percent and
  swap_used_percent keep theirs);
- the dimension service.name=claude-master becomes service.name=claw-master, and a
  service.instance.id starting claude-master (the blue-green slots) starts claw-master;
- provider=claude is added to measurements about Claude; the pool's own (proxy connections, TLS
  handshake errors, active connections, the process, the host) get none, as the server now
  exports them.

CloudWatch keeps one-minute data for 15 days and accepts datapoints up to two weeks old, so the
copy must run within two weeks of the oldest data (it reads the last 14 days less an hour). Each
stream keeps its unit. It reads by default (a dry run that says what it would write); --write
writes, up to --until (the moment the server switched to the new names, so no minute is counted
twice), and records in --state the last minute written of each stream after every batch, so a
rerun resumes after it and never writes a minute twice.
--verify compares each copied stream's total sample count and sum with its source.

Needs boto3 and cloudwatch:ListMetrics and GetMetricData on the source; PutMetricData on the
target to write.
"""

from __future__ import annotations

import argparse
import datetime as dt
import json
import os
import sys
import time

import boto3

PERIOD = 60
# CloudWatch takes datapoints up to two weeks old; an hour's margin keeps a slow run inside it.
LOOKBACK = dt.timedelta(days=14) - dt.timedelta(hours=1)
READ_TRIES = 3
# Pool-level measurements: no provider dimension (internal/clawmaster/metrics.go).
POOL_LEVEL = {
    "claude_master.proxy.connections",
    "claude_master.proxy.tls_handshake_errors",
    "claude_master.proxy.active_connections",
    "claude_master.process.uptime_seconds",
    "claude_master.process.goroutines",
    "claude_master.process.heap_bytes",
    "claude_master.selftest.counter",
    "claude_master.selftest.gauge",
    "claude_master.selftest.hist",
    "mem_used_percent",
    "swap_used_percent",
}
STATS = ("SampleCount", "Sum", "Minimum", "Maximum")


def target_of(metric: dict) -> dict:
    """The stream's name and dimensions after the rename."""
    name = metric["MetricName"]
    dims = []
    for d in metric.get("Dimensions", []):
        value = d["Value"]
        if d["Name"] == "service.name" and value == "claude-master":
            value = "claw-master"
        if d["Name"] == "service.instance.id" and value.startswith("claude-master"):
            value = "claw-master" + value[len("claude-master"):]
        dims.append({"Name": d["Name"], "Value": value})
    if name not in POOL_LEVEL and name.startswith("claude_master.") and not any(
        d["Name"] == "provider" for d in dims
    ):
        dims.append({"Name": "provider", "Value": "claude"})
    if name.startswith("claude_master."):
        name = "claw_master." + name[len("claude_master."):]
    return {"MetricName": name, "Dimensions": sorted(dims, key=lambda d: d["Name"])}


def key_of(metric: dict) -> str:
    dims = sorted((d["Name"], d["Value"]) for d in metric.get("Dimensions", []))
    return json.dumps([metric["MetricName"], dims])


def list_metrics(cw, namespace: str) -> list[dict]:
    out: list[dict] = []
    for page in cw.get_paginator("list_metrics").paginate(Namespace=namespace):
        out.extend(page["Metrics"])
    return out


def read_minutes(cw, namespace: str, metric: dict, start, end) -> dict[dt.datetime, dict]:
    """Every minute of the stream in [start, end): {timestamp: {stat: value}}."""
    queries = [
        {
            "Id": f"s{i}",
            "MetricStat": {
                "Metric": {"Namespace": namespace, **metric},
                "Period": PERIOD,
                "Stat": stat,
            },
            "ReturnData": True,
        }
        for i, stat in enumerate(STATS)
    ]
    for attempt in range(READ_TRIES):
        minutes: dict[dt.datetime, dict] = {}
        token = None
        failed = None
        while True:
            kwargs = dict(MetricDataQueries=queries, StartTime=start, EndTime=end, ScanBy="TimestampAscending")
            if token:
                kwargs["NextToken"] = token
            resp = cw.get_metric_data(**kwargs)
            for result in resp["MetricDataResults"]:
                # Complete, or PartialData with more pages to come; anything else lost data.
                if result.get("StatusCode", "Complete") not in ("Complete", "PartialData"):
                    failed = result.get("StatusCode")
                stat = STATS[int(result["Id"][1:])]
                for t, v in zip(result["Timestamps"], result["Values"]):
                    minutes.setdefault(t, {})[stat] = v
            token = resp.get("NextToken")
            if not token:
                break
        if failed is None:
            incomplete = [t for t, s in minutes.items() if not all(k in s for k in STATS)]
            if incomplete:
                raise RuntimeError(f"{metric['MetricName']}: {len(incomplete)} minutes lack a statistic")
            return {t: s for t, s in minutes.items() if s["SampleCount"] > 0}
        time.sleep(2**attempt)
    raise RuntimeError(f"{metric['MetricName']}: reading failed ({failed}) after {READ_TRIES} tries")


def unit_of(cw, namespace: str, metric: dict, start, end) -> str | None:
    """The unit the stream was published with (GetMetricData does not say; GetMetricStatistics does)."""
    resp = cw.get_metric_statistics(
        Namespace=namespace,
        MetricName=metric["MetricName"],
        Dimensions=metric["Dimensions"],
        StartTime=start,
        EndTime=end,
        Period=86400,
        Statistics=["SampleCount"],
    )
    units = {p["Unit"] for p in resp.get("Datapoints", []) if p.get("Unit")}
    return units.pop() if len(units) == 1 else None


def put_minutes(cw, namespace: str, target: dict, minutes: dict, unit, after, written) -> None:
    """Writes the minutes after `after` (the last one written before), calling written(t) with the
    last minute of each batch once that batch is stored."""
    datums = [
        {
            **target,
            **({"Unit": unit} if unit else {}),
            "Timestamp": t,
            "StatisticValues": {
                "SampleCount": s["SampleCount"],
                "Sum": s["Sum"],
                "Minimum": s["Minimum"],
                "Maximum": s["Maximum"],
            },
            "StorageResolution": 60,
        }
        for t, s in sorted(minutes.items())
        if after is None or t > after
    ]
    # At most 1000 datums (and well under 1 MB) per call.
    for i in range(0, len(datums), 500):
        batch = datums[i : i + 500]
        cw.put_metric_data(Namespace=namespace, MetricData=batch)
        written(batch[-1]["Timestamp"])


def totals(minutes: dict) -> tuple[float, float]:
    return (sum(s["SampleCount"] for s in minutes.values()), sum(s["Sum"] for s in minutes.values()))


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--region", default="us-west-1")
    parser.add_argument("--source", default="ClaudeMaster")
    parser.add_argument("--target", default="ClawMaster")
    parser.add_argument("--until", help="ISO time the server switched names (required with --write)")
    parser.add_argument("--write", action="store_true", help="write the copy (default: dry run)")
    parser.add_argument("--verify", action="store_true", help="compare copied streams with their source")
    parser.add_argument("--state", default=".scratch/migrate-metrics-state.json")
    parser.add_argument("--limit", type=int, default=0, help="only the first N streams (testing)")
    args = parser.parse_args()

    now = dt.datetime.now(dt.timezone.utc)
    start = (now - LOOKBACK).replace(second=0, microsecond=0)
    if args.write and not args.until:
        parser.error("--write needs --until: the moment the server switched to the new names")
    end = dt.datetime.fromisoformat(args.until) if args.until else now
    if end.tzinfo is None:
        end = end.replace(tzinfo=dt.timezone.utc)

    cw = boto3.client("cloudwatch", region_name=args.region)
    # Each stream's last minute written, as ISO text; "done" once the stream is complete.
    progress: dict[str, str] = {}
    if os.path.exists(args.state):
        with open(args.state) as f:
            progress = json.load(f)

    def save() -> None:
        os.makedirs(os.path.dirname(args.state) or ".", exist_ok=True)
        tmp = args.state + ".tmp"
        with open(tmp, "w") as f:
            json.dump(progress, f)
        os.replace(tmp, args.state)

    metrics = list_metrics(cw, args.source)
    if args.limit:
        metrics = metrics[: args.limit]
    finished = sum(1 for v in progress.values() if v == "done")
    print(f"{len(metrics)} streams in {args.source}, {finished} already copied", file=sys.stderr)
    points = streams = mismatches = 0
    for n, metric in enumerate(metrics, 1):
        source = {"MetricName": metric["MetricName"], "Dimensions": metric.get("Dimensions", [])}
        target = target_of(source)
        key = key_of(source)
        minutes = read_minutes(cw, args.source, source, start, end)
        if args.verify:
            copied = read_minutes(cw, args.target, target, start, end)
            if totals(copied) != totals(minutes):
                mismatches += 1
                print(f"MISMATCH {key}: source {totals(minutes)} copy {totals(copied)}")
            continue
        if not minutes:
            continue
        streams += 1
        points += len(minutes)
        if not args.write:
            if n <= 3:
                print(f"would copy {len(minutes)} minutes: {key} -> {key_of(target)}")
            continue
        if progress.get(key) == "done":
            continue
        after = dt.datetime.fromisoformat(progress[key]) if key in progress else None

        def written(t: dt.datetime, key: str = key) -> None:
            progress[key] = t.isoformat()
            save()

        put_minutes(cw, args.target, target, minutes, unit_of(cw, args.source, source, start, end), after, written)
        progress[key] = "done"
        save()
        if n % 25 == 0 or n == len(metrics):
            print(f"{n}/{len(metrics)} streams, {points} minutes", file=sys.stderr)
        time.sleep(0.05)
    if args.verify:
        print(f"verified {len(metrics)} streams, {mismatches} mismatched")
        return 1 if mismatches else 0
    print(f"{'copied' if args.write else 'would copy'} {points} minutes in {streams} streams")
    return 0


if __name__ == "__main__":
    sys.exit(main())
