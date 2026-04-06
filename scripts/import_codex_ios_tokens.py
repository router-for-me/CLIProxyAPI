#!/usr/bin/env python3
"""
批量导入 iOS ChatGPT App 的 Codex refresh_token，生成 auth JSON 文件。

输入文件格式（每行一条，---- 分隔，取首段和末段）：
  email----忽略----忽略----refresh_token

用法：
  python3 import_codex_ios_tokens.py codex.txt
  python3 import_codex_ios_tokens.py codex.txt --plan plus --auth-dir /home/iec/deploy/auths
"""

import argparse
import base64
import json
import subprocess
import sys
from datetime import datetime, timezone, timedelta

IOS_CLIENT_ID   = "app_LlGpXReQgckcGGUo2JrYvtJK"
IOS_REDIRECT_URI = "com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback"
TOKEN_URL        = "https://auth.openai.com/oauth/token"


def refresh_ios_token(refresh_token: str) -> dict:
    payload = json.dumps({
        "client_id":     IOS_CLIENT_ID,
        "grant_type":    "refresh_token",
        "redirect_uri":  IOS_REDIRECT_URI,
        "refresh_token": refresh_token,
    })
    r = subprocess.run(
        ["curl", "-sf", "--request", "POST", TOKEN_URL,
         "--header", "Content-Type: application/json",
         "--data-raw", payload],
        capture_output=True, text=True, timeout=30,
    )
    return json.loads(r.stdout)


def decode_id_token(id_token: str) -> dict:
    p = id_token.split(".")[1]
    pad = 4 - len(p) % 4
    return json.loads(base64.urlsafe_b64decode(p + "=" * (pad % 4)))


def process(input_file: str, auth_dir: str, plan: str):
    lines = open(input_file, encoding="utf-8").read().strip().splitlines()
    ok, fail = [], []

    for line in lines:
        line = line.strip()
        if not line:
            continue
        parts = line.split("----")
        if len(parts) < 4:
            fail.append((line, "格式错误"))
            continue
        email, refresh_token = parts[0], parts[-1]

        try:
            resp = refresh_ios_token(refresh_token)
        except Exception as e:
            fail.append((email, f"请求失败: {e}"))
            continue

        if "error" in resp:
            code = resp["error"].get("code") if isinstance(resp["error"], dict) else str(resp["error"])
            fail.append((email, code))
            continue

        access_token = resp.get("access_token", "")
        id_token     = resp.get("id_token", "")
        new_refresh  = resp.get("refresh_token", refresh_token)
        expires_in   = resp.get("expires_in", 86400)

        try:
            claims      = decode_id_token(id_token)
            auth_info   = claims.get("https://api.openai.com/auth", {})
            account_id  = auth_info.get("user_id") or claims.get("sub", "")
            token_email = claims.get("email", email)
        except Exception:
            account_id, token_email = "", email

        tz8 = timezone(timedelta(hours=8))
        now = datetime.now(tz=tz8)
        expired = now + timedelta(seconds=expires_in)

        auth = {
            "access_token":  access_token,
            "account_id":    account_id,
            "disabled":      False,
            "email":         token_email,
            "expired":       expired.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            "id_token":      id_token,
            "last_refresh":  now.strftime("%Y-%m-%dT%H:%M:%S+08:00"),
            "refresh_token": new_refresh,
            "type":          "codex",
        }

        fname = f"{auth_dir}/codex-{token_email}-{plan}.json"
        with open(fname, "w", encoding="utf-8") as f:
            json.dump(auth, f, indent=2)
            f.write("\n")

        ok.append(token_email)
        print(f"  OK  {token_email}")

    print(f"\n成功: {len(ok)}, 失败: {len(fail)}")
    if fail:
        print("\n失败列表:")
        for e, reason in fail:
            print(f"  FAIL  {e}  ->  {reason}")

    return len(fail)


def main():
    parser = argparse.ArgumentParser(description="批量导入 iOS Codex refresh_token")
    parser.add_argument("input_file", help="输入文件路径（每行 email----...----rt_xxx）")
    parser.add_argument("--plan",     default="plus", help="套餐类型，默认 plus")
    parser.add_argument("--auth-dir", default="/home/iec/deploy/auths", help="输出目录")
    args = parser.parse_args()

    sys.exit(process(args.input_file, args.auth_dir, args.plan))


if __name__ == "__main__":
    main()
