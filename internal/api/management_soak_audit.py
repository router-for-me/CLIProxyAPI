#!/usr/bin/env python3
"""Read-only companion audit. Writes only private test artifacts, never app state."""
import argparse
import datetime
import hashlib
import json
import os
import pathlib
import re
import signal
import subprocess
import time

parser = argparse.ArgumentParser()
parser.add_argument('--root', required=True)
parser.add_argument('--baseline', required=True)
parser.add_argument('--output', required=True)
parser.add_argument('--once', action='store_true')
args = parser.parse_args()
root, output = pathlib.Path(args.root), pathlib.Path(args.output)
output.mkdir(parents=True, exist_ok=True, mode=0o700)
os.umask(0o077)
baseline = json.loads(pathlib.Path(args.baseline).read_text())['files']
running = True

def stop(*_):
    global running
    running = False

signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)

def emit(name, data):
    with (output / name).open('a') as file:
        file.write(json.dumps(data) + '\n')

def now():
    return datetime.datetime.now(datetime.timezone.utc).isoformat()

def snapshot():
    hashes = {}
    for name in baseline:
        p = pathlib.Path(name)
        hashes[name] = hashlib.sha256(p.read_bytes()).hexdigest() if p.is_file() else None
    result = subprocess.run(['systemctl', 'show', 'cliproxyapi.service', '-p', 'MainPID', '-p', 'NRestarts', '-p', 'ActiveState'], capture_output=True, text=True, timeout=10)
    service = dict(line.split('=', 1) for line in result.stdout.splitlines() if '=' in line)
    processes = subprocess.run(['ps', '-eo', 'rss,comm'], capture_output=True, text=True, timeout=10)
    browser_rss_kb = sum(int(line.split()[0]) for line in processes.stdout.splitlines() if re.search(r'chrome|chromium', line, re.I) and line.split()[0].isdigit())
    data = {'at': now(), 'hashes': hashes, 'changedFiles': [p for p, h in hashes.items() if baseline[p] != h], 'service': service, 'browserRssKB': browser_rss_kb}
    emit('state-audit.jsonl', data)
    (output / 'audit-final.json').write_text(json.dumps(data, indent=2))

log = root / 'logs' / 'main.log'
inode = None
position = 0
buffer = ''
first = True
pattern = re.compile(r'\]\s+(\d{3})\s*\|[^|]*\|\s*([^|]+)\|\s*(GET|POST|PUT|PATCH|DELETE)\s+"([^"?]+)')

def audit_requests():
    global inode, position, buffer, first
    if not log.exists():
        return
    stat = log.stat()
    if inode != stat.st_ino or stat.st_size < position:
        inode = stat.st_ino
        position = stat.st_size if first else 0
        buffer = ''
        first = False
    with log.open(errors='replace') as file:
        file.seek(position)
        chunk = file.read()
        position = file.tell()
    parts = (buffer + chunk).split('\n')
    buffer = parts.pop()
    for line in parts:
        if '[gin_logger.go:' not in line:
            continue
        match = pattern.search(line)
        if not match:
            continue
        status, peer, method, route = match.groups()
        if route in ('/v0/management/accounts/login', '/v0/management/accounts/logout'):
            continue
        unsafe_get = method == 'GET' and (route.endswith('-auth-url') or route.endswith('/get-auth-status') or route.endswith('/oauth-callback') or route.startswith('/v0/resource/plugins/'))
        if method == 'GET' and not unsafe_get:
            continue
        emit('mutation-audit.jsonl', {'at': line[1:20], 'status': int(status), 'source': 'test-host' if peer.strip() in ('15.175.34.60', '127.0.0.1', '::1') else 'other-client', 'method': method, 'path': route})

last = 0
try:
    while running:
        audit_requests()
        if time.monotonic() - last >= 60:
            snapshot()
            last = time.monotonic()
        if args.once:
            break
        time.sleep(1)
finally:
    audit_requests()
    snapshot()
