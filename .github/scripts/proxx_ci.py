#!/usr/bin/env python3
"""proxx CI helper: test selection, shard planning and shard verification.

Subcommands
  select   --base REF            print the packages a diff can reach ("ALL" for everything)
  plan     --shards N --index I  print this shard's plan as JSON (packages and -run/-skip parts)
  run      --shards N --index I  run this shard with go test -json, verify it ran exactly its plan
  timings  FILES...              merge go test -json outputs into a timings file

Design rules (see docs/proxx/ci.md):
  * Fail safe: a changed path that cannot be mapped to Go packages selects everything.
  * Every shard computes the same full plan and proves that the shards together cover every
    selected package and test exactly once; then it proves it ran exactly its own part.
  * Packages too slow for one shard are split by top-level test name. The last part of a split
    uses -skip instead of -run, so a test added after the timings were recorded is never lost.
"""

import argparse
import fnmatch
import json
import os
import subprocess
import sys
import time

MODULE = "github.com/router-for-me/CLIProxyAPI/v8"

# Changes here rerun everything. Anything else that is not Go source inside a package directory
# and not in SAFE_PATHS also reruns everything (fail safe).
RERUN_ALL = [
    "go.mod",
    "go.sum",
    ".github/**",
    "hk.pkl",
]

# Paths no test or build reads. Checked by `inventory`: tests must not reference these by name.
SAFE_PATHS = [
    "*.md",
    "**/*.md",
    "LICENSE",
    "assets/**",
]

# Packages whose tests read the whole module (build cmd/server with `go build`, or walk the
# source tree). They run whenever any Go file or embedded file changes. `inventory` fails CI when
# a test uses one of WHOLE_MODULE_MARKERS outside these packages, so this list cannot rot.
WHOLE_MODULE = [
    MODULE + "/internal/util",
    MODULE + "/test/proxx/decisions_e2e",
    MODULE + "/test/proxx/groups_e2e",
    MODULE + "/test/proxx/install_e2e",
    MODULE + "/test/proxx/pricing_e2e",
    MODULE + "/test/proxx/requestlogs_e2e",
    MODULE + "/test/proxx/selfupdate_e2e",
]
WHOLE_MODULE_MARKERS = ['exec.Command("go"', "filepath.WalkDir(", "runtime.Caller(", '"../../..', '"..", ".."']

# Extra non-import inputs: tests that read files outside their own package directory.
# Key: path glob (repo relative). Value: packages that read it. These ADD to the normal mapping;
# a matching file with no owning package (repo root, docs/) still reruns everything.
# `inventory` fails CI when a test reads outside its package and its package is not listed here.
EXTRA_INPUTS = {
    "config.example.yaml": [
        MODULE + "/internal/config",
        MODULE + "/internal/runtime/executor/helps",
    ],
    "install.sh": [MODULE + "/test/proxx/install_e2e"],
    "auths/**": [MODULE + "/internal/auth/devin"],
    "docs/native-prompt-capture/**": [MODULE + "/internal/signature"],
    "internal/translator/*/interactions/**": [MODULE + "/internal/translator/interactions"],
    "internal/registry/models/**": [
        MODULE + "/internal/api/handlers/management",
        MODULE + "/sdk/cliproxy",
    ],
    "internal/home/testdata/**": [MODULE + "/sdk/cliproxy/auth"],
    # Reads a developer's local request log when present; never present in CI.
    "logs/**": [MODULE + "/internal/translator/openai/interactions/responses"],
}

# Seconds of shard time a package costs before its tests run (link and process start).
PACKAGE_OVERHEAD = 1.0


def sh(*args, **kw):
    return subprocess.run(args, check=True, capture_output=True, text=True, **kw).stdout


def match(path, globs):
    return any(fnmatch.fnmatchcase(path, g) for g in globs)


def go_packages():
    """All packages in the module with their directories and test deps."""
    out = sh("go", "list", "-test", "-json=ImportPath,ForTest,Dir,Deps,TestGoFiles,XTestGoFiles", "./...")
    dec = json.JSONDecoder()
    pkgs, deps, i = {}, {}, 0
    while i < len(out):
        while i < len(out) and out[i].isspace():
            i += 1
        if i >= len(out):
            break
        obj, i = dec.raw_decode(out, i)
        path = obj["ImportPath"]
        if path.endswith(".test"):
            continue
        base = obj.get("ForTest") or path.split(" ")[0]
        if " " not in path:
            pkgs[base] = obj
        deps.setdefault(base, set()).update(d for d in obj.get("Deps", []) if d.startswith(MODULE))
        deps[base].add(base)
    root = os.getcwd()
    dirs = {os.path.relpath(p["Dir"], root): ip for ip, p in pkgs.items()}
    return pkgs, deps, dirs


def owning_package(path, dirs):
    d = os.path.dirname(path)
    while True:
        if d in dirs:
            return dirs[d]
        if d in ("", "."):
            return None
        d = os.path.dirname(d)


def select(base, files=None):
    changed = files if files is not None else [
        p for p in sh("git", "diff", "--name-only", f"{base}...HEAD").splitlines() if p]
    pkgs, deps, dirs = go_packages()
    reasons, touched, whole = [], set(), False
    for path in changed:
        if match(path, RERUN_ALL):
            return "ALL", [f"{path}: listed in RERUN_ALL"]
        extra = sum((v for g, v in EXTRA_INPUTS.items() if match(path, [g])), [])
        if extra:
            touched.update(extra)
            reasons.append(f"{path}: extra input of {', '.join(extra)}")
        if match(path, SAFE_PATHS):
            reasons.append(f"{path}: safe, no test reads it")
            continue
        owner = owning_package(path, dirs)
        if owner is None:
            return "ALL", [f"{path}: not inside a Go package and not listed as safe"]
        touched.add(owner)
        whole = True
        reasons.append(f"{path}: package {owner}")
    selected = {p for p, d in deps.items() if d & touched}
    if whole and selected:
        selected.update(p for p in WHOLE_MODULE if p in pkgs)
    return sorted(selected), reasons


def load_timings(path):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return {"packages": {}, "tests": {}}


def plan(shards, timings, packages, target):
    """Deterministic plan for all shards. Returns a list of shard item lists."""
    pk_t, ts_t = timings.get("packages", {}), timings.get("tests", {})
    default = sorted(pk_t.values())[len(pk_t) // 2] if pk_t else 1.0
    items = []
    for p in packages:
        cost = pk_t.get(p, default)
        tests = ts_t.get(p, {})
        if cost > target and len(tests) > 1:
            # First fit decreasing over tests that matter (>= 1s); small and unknown tests stay
            # in the -skip remainder. A test longer than target gets a part of its own.
            buckets, rest = [], cost - sum(tests.values())
            for name, t in sorted(tests.items(), key=lambda kv: (-kv[1], kv[0])):
                if t < 1.0 or sum(tests.values()) - sum(b[0] for b in buckets) <= target:
                    rest += t
                    continue
                b = next((b for b in buckets if b[0] + t <= target), None)
                if b is None:
                    b = [0.0, []]
                    buckets.append(b)
                b[0] += t
                b[1].append(name)
            rest = max(rest, 0.0)
            if not buckets:
                items.append({"package": p, "cost": cost + PACKAGE_OVERHEAD})
                continue
            named = sorted(n for _, ns in buckets for n in ns)
            for t, ns in buckets:
                items.append({"package": p, "run": sorted(ns), "cost": t + PACKAGE_OVERHEAD})
            items.append({"package": p, "skip": named, "cost": rest + PACKAGE_OVERHEAD})
        else:
            items.append({"package": p, "cost": cost + PACKAGE_OVERHEAD})
    out = [[0.0, []] for _ in range(shards)]
    for it in sorted(items, key=lambda x: (-x["cost"], x["package"], x.get("run", [""])[:1])):
        s = min(range(shards), key=lambda i: (out[i][0], i))
        out[s][0] += it["cost"]
        out[s][1].append(it)
    prove_cover(packages, [s[1] for s in out])
    return [{"cost": round(c, 1), "longest": round(max((i["cost"] for i in its), default=0), 1), "items": its}
            for c, its in out]


def prove_cover(packages, shard_items):
    """Every package is planned, nothing is planned twice, and each split package has exactly one
    -skip remainder whose skip list is exactly the union of its -run parts."""
    seen = {}
    for its in shard_items:
        for it in its:
            seen.setdefault(it["package"], []).append(it)
    missing = set(packages) - set(seen)
    extra = set(seen) - set(packages)
    if missing or extra:
        sys.exit(f"plan does not cover the selection: missing={sorted(missing)} extra={sorted(extra)}")
    for p, its in seen.items():
        if len(its) == 1:
            if "run" in its[0] or "skip" in its[0]:
                sys.exit(f"plan: lone partial item for {p}")
            continue
        runs = [n for it in its if "run" in it for n in it["run"]]
        skips = [it for it in its if "skip" in it]
        whole = [it for it in its if "run" not in it and "skip" not in it]
        if whole or len(skips) != 1 or len(runs) != len(set(runs)) or sorted(runs) != skips[0]["skip"]:
            sys.exit(f"plan: split of {p} is not an exact partition")


def anchored(names):
    return "^(" + "|".join(names) + ")$"


def run_shard(shards, index, timings, packages, target, race, out_path):
    full = plan(shards, timings, packages, target)
    mine = full[index]["items"]
    print(f"shard {index + 1}/{shards}: {len(mine)} items, planned {full[index]['cost']}s "
          f"(all shards: {[s['cost'] for s in full]})", flush=True)
    flags = ["-count=1", "-shuffle=on", "-json"] + (["-race"] if race else [])
    whole = [it["package"] for it in mine if "run" not in it and "skip" not in it]
    cmds = []  # (label, command, plan item or None)
    if whole:
        cmds.append(("whole", ["go", "test", *flags, *whole], None))
    for it in mine:
        if "run" in it:
            cmds.append((it["package"], ["go", "test", *flags, "-run", anchored(it["run"]), it["package"]], it))
        elif "skip" in it:
            extra = ["-skip", anchored(it["skip"])] if it["skip"] else []
            cmds.append((it["package"], ["go", "test", *flags, *extra, it["package"]], it))
    ran, failed, ok = set(), [], True
    logs = {}  # (package, test) -> output lines, printed only when that test fails
    with open(out_path, "w") as out:
        for label, cmd, item in cmds:
            tests_ran = {}  # per invocation, so a -run part and the -skip part of one package stay apart
            start = time.time()
            p = subprocess.Popen(cmd, stdout=subprocess.PIPE, text=True)
            for line in p.stdout:
                out.write(line)
                try:
                    e = json.loads(line)
                except ValueError:
                    continue
                pkg, test, act = e.get("Package"), e.get("Test"), e.get("Action")
                if act in ("pass", "fail", "skip") and not test:
                    ran.add(pkg)
                if act in ("pass", "fail", "skip") and test and "/" not in test:
                    tests_ran.setdefault(pkg, set()).add(test)
                if act == "output":
                    logs.setdefault((pkg, test), []).append(e.get("Output", ""))
                if act == "fail":
                    failed.append(f"{pkg} {test or '(package)'}")
                    if test is None or "/" not in test:
                        sys.stdout.write(f"::group::FAIL {pkg} {test or '(package)'}\n")
                        lines = [l for (p_, t_), ls in logs.items() if p_ == pkg and (
                            test is None or t_ == test or (t_ or "").startswith(test + "/")) for l in ls]
                        sys.stdout.write("".join(lines[-400:]))
                        sys.stdout.write("::endgroup::\n")
                if act in ("pass", "skip") and test:
                    logs.pop((pkg, test), None)
                if act == "output" and test is None and pkg and e.get("Output", "").startswith(("FAIL", "# ")):
                    sys.stdout.write(e["Output"])
            ok = (p.wait() == 0) and ok
            print(f"  {label}: {time.time() - start:.1f}s", flush=True)
            if item is not None:
                got = tests_ran.get(item["package"], set())
                if "run" in item and not got <= set(item["run"]):
                    failed.append(f"{item['package']}: -run part ran unplanned tests {sorted(got - set(item['run']))}")
                    ok = False
                if "skip" in item and got & set(item["skip"]):
                    failed.append(f"{item['package']}: -skip part ran skipped tests {sorted(got & set(item['skip']))}")
                    ok = False
    planned = {it["package"] for it in mine}
    if ran != planned:
        failed.append(f"shard ran {sorted(ran ^ planned)} outside its plan (or never ran them)")
        ok = False
    summary = os.environ.get("GITHUB_STEP_SUMMARY")
    lines = [f"### shard {index + 1}/{shards}: {'pass' if ok else 'FAIL'}", ""]
    lines += [f"- `{f}`" for f in sorted(set(failed))] or ["no failures"]
    print("\n".join(lines))
    if summary:
        with open(summary, "a") as f:
            f.write("\n".join(lines) + "\n")
    return ok


def merge_timings(files):
    pk, ts = {}, {}
    for f in files:
        with open(f) as fh:
            for line in fh:
                try:
                    e = json.loads(line)
                except ValueError:
                    continue
                if e.get("Action") not in ("pass", "fail"):
                    continue
                p, t = e.get("Package"), e.get("Test")
                if t is None:
                    pk[p] = pk.get(p, 0.0) + e.get("Elapsed", 0.0)
                elif "/" not in t:
                    ts.setdefault(p, {})[t] = e.get("Elapsed", 0.0)
    return {"packages": {k: round(v, 2) for k, v in sorted(pk.items())},
            # Only tests worth naming in a split; the planner charges the rest to the -skip part.
            "tests": {k: {n: round(x, 2) for n, x in sorted(v.items()) if x >= 0.5}
                      for k, v in sorted(ts.items()) if any(x >= 0.5 for x in v.values())}}


def inventory():
    """Fail when a test reads the whole module or a path outside its package without being listed."""
    pkgs, _, dirs = go_packages()
    problems = []
    for ip, p in pkgs.items():
        for f in (p.get("TestGoFiles") or []) + (p.get("XTestGoFiles") or []):
            src = open(os.path.join(p["Dir"], f)).read()
            listed = ip in WHOLE_MODULE or any(ip in v for v in EXTRA_INPUTS.values())
            if any(m in src for m in WHOLE_MODULE_MARKERS) and not listed:
                problems.append(f"{ip}/{f}: reads files outside its package; list them in EXTRA_INPUTS "
                                "or the package in WHOLE_MODULE")
    if problems:
        sys.exit("\n".join(problems))
    print(f"inventory ok: {len(WHOLE_MODULE)} whole-module packages, {len(EXTRA_INPUTS)} extra inputs")


def main():
    ap = argparse.ArgumentParser()
    sub = ap.add_subparsers(dest="cmd", required=True)
    s = sub.add_parser("select")
    s.add_argument("--base", default="")
    s.add_argument("--files", nargs="*", help="changed paths (instead of a git diff; for dry runs)")
    for name in ("plan", "run"):
        r = sub.add_parser(name)
        r.add_argument("--shards", type=int, required=True)
        r.add_argument("--index", type=int, required=True)
        r.add_argument("--timings", default=".github/proxx-ci/timings.json")
        r.add_argument("--packages", default="", help="file with one package per line, or empty for all")
        r.add_argument("--target", type=float, default=20.0, help="seconds per split part")
        r.add_argument("--race", action="store_true")
        r.add_argument("--out", default="test-output.json")
    t = sub.add_parser("timings")
    t.add_argument("files", nargs="+")
    sub.add_parser("inventory")
    a = ap.parse_args()

    if a.cmd == "select":
        sel, reasons = select(a.base, a.files)
        print("\n".join(reasons), file=sys.stderr)
        print("ALL" if sel == "ALL" else "\n".join(sel))
        return
    if a.cmd == "timings":
        json.dump(merge_timings(a.files), sys.stdout, indent=1, sort_keys=True)
        return
    if a.cmd == "inventory":
        inventory()
        return
    if a.packages and os.path.exists(a.packages) and open(a.packages).read().strip() != "ALL":
        packages = sorted({l.strip() for l in open(a.packages) if l.strip()})
    else:
        packages = sorted(sh("go", "list", "./...").split())
    timings = load_timings(a.timings)
    if a.cmd == "plan":
        json.dump(plan(a.shards, timings, packages, a.target)[a.index], sys.stdout, indent=1)
        return
    if not packages:
        print("no packages selected")
        return
    if not run_shard(a.shards, a.index, timings, packages, a.target, a.race, a.out):
        sys.exit(1)


if __name__ == "__main__":
    main()
