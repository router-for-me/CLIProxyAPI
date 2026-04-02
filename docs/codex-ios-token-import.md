# Codex iOS Token 导入与续期

## 背景

项目支持两套 Codex OAuth 客户端：

| 类型 | client_id | 来源 |
|---|---|---|
| Codex CLI | `app_EMoamEEZ73f0CkXaXp7hrann` | 项目自带登录流（`codex-login`） |
| iOS ChatGPT App | `app_LlGpXReQgckcGGUo2JrYvtJK` | 从 iOS App 导出的 refresh_token |

两者签发的 token 格式不同，需要用各自的 client_id 续期，否则会失败。

## 区分方式

解码 `id_token` 的 JWT payload，查看 `aud` 字段：

- Codex CLI token：`aud = ["app_EMoamEEZ73f0CkXaXp7hrann"]`，`https://api.openai.com/auth` 中含 `chatgpt_account_id`
- iOS token：`aud = ["app_LlGpXReQgckcGGUo2JrYvtJK"]`，`https://api.openai.com/auth` 中只有 `user_id`

程序通过 `IsIOSToken(idToken string) bool` 自动判断，`Executor.Refresh()` 会据此选择对应续期流程。

## 输入格式

从 iOS App 导出的凭据格式（`----` 分隔，取首段和末段）：

```
email----忽略----忽略----refresh_token
```

示例：
```
alice@hotmail.com----H1xf1pWwjpg4----jovofrkts30853----rt_JejqXyx7PG...
```

## 批量导入脚本

将多行凭据写入一个文本文件（每行一条），然后运行：

```bash
python3 << 'EOF'
import json, base64, subprocess
from datetime import datetime, timezone, timedelta

AUTH_DIR = "/home/iec/deploy/auths"
PLAN = "plus"   # 按实际套餐修改

lines = open("codex.txt").read().strip().splitlines()
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

    payload = json.dumps({
        "client_id": "app_LlGpXReQgckcGGUo2JrYvtJK",
        "grant_type": "refresh_token",
        "redirect_uri": "com.openai.chat://auth0.openai.com/ios/com.openai.chat/callback",
        "refresh_token": refresh_token
    })
    r = subprocess.run([
        "curl", "-sf", "--request", "POST",
        "https://auth.openai.com/oauth/token",
        "--header", "Content-Type: application/json",
        "--data-raw", payload
    ], capture_output=True, text=True, timeout=30)

    try:
        resp = json.loads(r.stdout)
    except Exception:
        fail.append((email, f"响应解析失败: {r.stdout[:100]}"))
        continue

    if "error" in resp:
        fail.append((email, resp.get("error", {}).get("code") or str(resp["error"])))
        continue

    access_token = resp.get("access_token", "")
    id_token     = resp.get("id_token", "")
    new_refresh  = resp.get("refresh_token", refresh_token)
    expires_in   = resp.get("expires_in", 86400)

    try:
        p = id_token.split(".")[1]
        pad = 4 - len(p) % 4
        claims = json.loads(base64.urlsafe_b64decode(p + "=" * (pad % 4)))
        auth_info  = claims.get("https://api.openai.com/auth", {})
        account_id = auth_info.get("user_id") or claims.get("sub", "")
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
        "type":          "codex"
    }

    fname = f"{AUTH_DIR}/codex-{token_email}-{PLAN}.json"
    with open(fname, "w") as f:
        json.dump(auth, f, indent=2)
        f.write("\n")

    ok.append(token_email)
    print(f"  OK  {token_email}")

print(f"\n成功: {len(ok)}, 失败: {len(fail)}")
if fail:
    print("\n失败列表:")
    for e, reason in fail:
        print(f"  FAIL  {e}  ->  {reason}")
EOF
```

## auth JSON 文件格式

```json
{
  "access_token":  "eyJ...",
  "account_id":    "user-C0AlW40Fl1ViVpdGuxTnoJck",
  "disabled":      false,
  "email":         "alice@hotmail.com",
  "expired":       "2026-04-12T23:48:50+08:00",
  "id_token":      "eyJ...",
  "last_refresh":  "2026-04-02T23:48:50+08:00",
  "refresh_token": "rt_s-6Niy...",
  "type":          "codex"
}
```

iOS token 与 Codex CLI token 的字段差异：

| 字段 | Codex CLI | iOS |
|---|---|---|
| `account_id` | UUID 格式（`chatgpt_account_id`） | `user-` 前缀（`user_id`） |
| id_token 中的套餐信息 | 有（`chatgpt_plan_type` 等） | 无 |

## 自动续期

程序启动后会根据 `expired` 字段自动触发续期。`Executor.Refresh()` 逻辑：

1. 读取 `auth.Metadata["id_token"]`，调用 `IsIOSToken()` 检测 aud
2. iOS token → `RefreshTokensIOS()`（JSON + iOS client_id + redirect_uri）
3. Codex token → `RefreshTokensWithRetry()`（form-urlencoded + Codex client_id）

## 注意事项

- `codex.txt` 等包含原始 refresh_token 的文件**不要提交到 git**
- auth JSON 文件存放在 `~/deploy/auths/`，同样不在 git 追踪范围内
- iOS refresh_token 会在每次续期后轮换，程序会自动写回新值
