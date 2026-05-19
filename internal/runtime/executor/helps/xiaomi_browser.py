#!/usr/bin/env python3
"""Xiaomi MiMo 平台自动登录 — stdin/stdout JSON 协议子进程

协议:
  stdin  ← {"action":"login","email":"...","password":"..."}
  stdout → {"type":"status","message":"..."}
  stdout → {"type":"need_verification","session_id":"<uuid>"}
  (阻塞等待 stdin)
  stdin  ← {"action":"verify","code":"123456"}
  stdout → {"type":"cookies","platform":"...","all":"..."}
  stdout → {"type":"done","status":"success"}
"""

import asyncio
import glob
import json
import os
import sys
import traceback
import uuid


def log_stderr(msg):
    """输出到 stderr 以便 Go 进程捕获"""
    print(msg, file=sys.stderr, flush=True)


def emit(obj):
    """输出 JSON 行并立即 flush"""
    print(json.dumps(obj, ensure_ascii=False), flush=True)


def read_command():
    """从 stdin 读取一行 JSON 命令"""
    line = sys.stdin.readline()
    if not line:
        return None
    return json.loads(line)


log_stderr("xiaomi_browser.py: 启动中...")
log_stderr(f"Python: {sys.version}")
log_stderr(f"工作目录: {os.getcwd()}")

try:
    from playwright.async_api import async_playwright
    log_stderr("playwright 导入成功")
except ImportError as e:
    log_stderr(f"playwright 导入失败: {e}")
    log_stderr("请安装: pip install playwright && playwright install chromium")
    emit({"type": "error", "message": f"playwright 未安装: {e}"})
    emit({"type": "done", "status": "error"})
    sys.exit(1)


async def main():
    # 读取启动命令
    cmd = read_command()
    if not cmd or cmd.get("action") != "login":
        emit({"type": "error", "message": "期望 login 动作"})
        emit({"type": "done", "status": "error"})
        return

    email = cmd["email"]
    password = cmd["password"]
    executable_path = cmd.get("executable_path", "")

    # 自动检测可用的 chromium 版本
    if not executable_path:
        # 1. 检查环境变量
        executable_path = os.environ.get("CHROMIUM_PATH", "")

    if not executable_path:
        # 2. 检查常见 Linux 路径
        linux_paths = [
            "/usr/bin/chromium",
            "/usr/bin/chromium-browser",
            "/usr/bin/google-chrome",
            "/usr/bin/google-chrome-stable",
        ]
        for path in linux_paths:
            if os.path.isfile(path) and os.access(path, os.X_OK):
                executable_path = path
                break

    if not executable_path:
        # 3. macOS Playwright 缓存
        playwright_cache = os.path.expanduser("~/Library/Caches/ms-playwright")
        if os.path.isdir(playwright_cache):
            candidates = sorted(
                glob.glob(f"{playwright_cache}/chromium_headless_shell-*/chrome-headless-shell-mac-arm64/chrome-headless-shell"),
                reverse=True,
            )
            if candidates:
                executable_path = candidates[0]
            else:
                candidates = sorted(
                    glob.glob(f"{playwright_cache}/chromium-*/chrome-mac-arm64/Chromium.app/Contents/MacOS/Chromium"),
                    reverse=True,
                )
                if candidates:
                    executable_path = candidates[0]

    log_stderr(f"chromium 路径: {executable_path or '使用默认'}")

    async with async_playwright() as p:
        launch_kwargs = {
            "headless": True,
            "args": ["--no-sandbox", "--disable-setuid-sandbox"],
        }
        if executable_path:
            launch_kwargs["executable_path"] = executable_path

        log_stderr("启动 chromium...")
        try:
            browser = await p.chromium.launch(**launch_kwargs)
            log_stderr("chromium 启动成功")
        except Exception as e:
            log_stderr(f"chromium 启动失败: {e}")
            emit({"type": "error", "message": f"chromium 启动失败: {e}"})
            emit({"type": "done", "status": "error"})
            return

        context = await browser.new_context(
            user_agent="Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36"
        )
        page = await context.new_page()

        try:
            # Step 1: 访问 SSO 入口
            emit({"type": "status", "message": "访问 SSO 入口..."})
            log_stderr("访问 SSO 入口...")
            await page.goto(
                "https://account.xiaomi.com/pass/serviceLogin?sid=api-platform",
                wait_until="networkidle",
                timeout=60000,
            )
            log_stderr(f"SSO 页面加载完成: {page.url[:100]}")
            await page.wait_for_timeout(2000)

            # Step 2: 填写表单
            emit({"type": "status", "message": "填写登录表单..."})
            log_stderr("填写登录表单...")

            email_locator = page.locator('input[type="text"]')
            await email_locator.wait_for(state="visible", timeout=15000)
            await email_locator.click(force=True)
            await page.wait_for_timeout(300)
            await email_locator.fill(email)
            log_stderr(f"已填写邮箱: {email}")
            await page.wait_for_timeout(500)

            password_locator = page.locator('input[type="password"]')
            await password_locator.wait_for(state="visible", timeout=10000)
            await password_locator.click(force=True)
            await page.wait_for_timeout(300)
            await password_locator.fill(password)
            log_stderr("已填写密码")
            await page.wait_for_timeout(1000)

            # Step 3: 关闭弹窗 + 勾选协议
            emit({"type": "status", "message": "处理弹窗和协议..."})
            log_stderr("处理弹窗和协议...")
            try:
                btn = page.locator('button:has-text("同意")').first
                await btn.click(force=True, timeout=3000)
                log_stderr("已点击同意按钮")
                await page.wait_for_timeout(500)
            except Exception:
                log_stderr("无同意按钮或已处理")

            try:
                cb = page.locator(".ant-checkbox-input").first
                if not await cb.is_checked(timeout=1000):
                    await cb.check(force=True)
                    log_stderr("已勾选协议")
                    await page.wait_for_timeout(300)
            except Exception:
                log_stderr("无协议复选框或已处理")

            # Step 4: 提交登录
            emit({"type": "status", "message": "提交登录表单..."})
            log_stderr("提交登录表单...")
            await page.locator('button[type="submit"]').click(force=True)
            await page.wait_for_timeout(5000)
            log_stderr(f"提交后 URL: {page.url[:100]}")

            # Step 5: 检查是否需要邮箱验证
            page_url = page.url
            page_text = ""
            try:
                page_text = await page.evaluate("document.body.innerText")
            except Exception:
                pass

            need_verify = (
                "verifyEmail" in page_url
                or "identity" in page_url
                or "验证" in page_text
                or "请输入验证码" in page_text
                or "安全验证" in page_text
                or "verification" in page_text.lower()
            )
            log_stderr(f"验证检测: url={page_url[:80]}, need_verify={need_verify}")
            log_stderr(f"页面文本片段: {page_text[:200]}")

            if need_verify:
                emit({"type": "status", "message": "需要邮箱安全验证"})
                log_stderr("检测到需要邮箱验证")

                # 发送验证邮件
                send_selectors = [
                    'button:has-text("发送")',
                    'button:has-text("Send")',
                    'button:has-text("获取验证码")',
                    'button:has-text("获取")',
                    'button:has-text("发送验证码")',
                ]
                send_clicked = False
                for sel in send_selectors:
                    try:
                        btn = page.locator(sel).first
                        if await btn.is_visible(timeout=2000):
                            await btn.click(force=True)
                            log_stderr(f"已点击发送按钮: {sel}")
                            send_clicked = True
                            break
                    except Exception as e:
                        log_stderr(f"发送按钮 {sel} 未找到: {e}")
                if not send_clicked:
                    log_stderr("警告: 未找到发送验证码按钮，可能已自动发送")
                await page.wait_for_timeout(1000)

                session_id = str(uuid.uuid4())
                emit({"type": "need_verification", "session_id": session_id})
                log_stderr(f"等待用户输入验证码, session_id={session_id}")

                # 阻塞等待验证码
                verify_cmd = read_command()
                if not verify_cmd or verify_cmd.get("action") != "verify":
                    emit({"type": "error", "message": "期望 verify 动作"})
                    emit({"type": "done", "status": "error"})
                    return

                code = verify_cmd["code"]
                log_stderr(f"收到验证码: {code}")
                emit({"type": "status", "message": "输入验证码..."})

                # 截图调试
                try:
                    await page.screenshot(path="/tmp/xiaomi_before_code.png")
                    log_stderr("已保存验证码输入前截图: /tmp/xiaomi_before_code.png")
                except Exception:
                    pass

                # 填写验证码 — 尝试多种选择器
                code_filled = False
                code_selectors = [
                    'input[name="code"]',
                    'input[placeholder*="验证码"]',
                    'input[placeholder*="code"]',
                    'input[placeholder*="Code"]',
                    'input[type="tel"]',
                    'input[type="text"]',
                    'input[type="number"]',
                ]
                for sel in code_selectors:
                    try:
                        inp = page.locator(sel).first
                        if await inp.is_visible(timeout=2000):
                            await inp.click(force=True)
                            await page.wait_for_timeout(200)
                            await inp.fill(code)
                            log_stderr(f"验证码已填入: {sel}")
                            code_filled = True
                            break
                    except Exception as e:
                        log_stderr(f"验证码输入 {sel} 失败: {e}")

                # 回退: 逐字符键入
                if not code_filled:
                    log_stderr("尝试键盘逐字符输入验证码...")
                    try:
                        await page.keyboard.type(code, delay=100)
                        log_stderr("键盘输入验证码完成")
                        code_filled = True
                    except Exception as e:
                        log_stderr(f"键盘输入失败: {e}")

                if not code_filled:
                    log_stderr("错误: 无法填写验证码")
                    emit({"type": "error", "message": "无法找到验证码输入框"})
                    emit({"type": "done", "status": "error"})
                    return

                await page.wait_for_timeout(500)

                # 截图调试
                try:
                    await page.screenshot(path="/tmp/xiaomi_after_code.png")
                    log_stderr("已保存验证码输入后截图: /tmp/xiaomi_after_code.png")
                except Exception:
                    pass

                # 提交验证
                submit_code_selectors = [
                    'button:has-text("验证")',
                    'button:has-text("确认")',
                    'button:has-text("Verify")',
                    'button:has-text("Submit")',
                    'button:has-text("确定")',
                    'button:has-text("提交")',
                    'button[type="submit"]',
                    'input[type="submit"]',
                ]
                submit_clicked = False
                for sel in submit_code_selectors:
                    try:
                        btn = page.locator(sel).first
                        if await btn.is_visible(timeout=2000):
                            await btn.click(force=True)
                            log_stderr(f"已点击提交按钮: {sel}")
                            submit_clicked = True
                            break
                    except Exception as e:
                        log_stderr(f"提交按钮 {sel} 未找到: {e}")

                if not submit_clicked:
                    log_stderr("警告: 未找到提交按钮，尝试按 Enter")
                    await page.keyboard.press("Enter")
                    log_stderr("已按 Enter")

                log_stderr("验证码已提交，等待响应...")
                await page.wait_for_timeout(3000)

                # 检查提交后是否有错误提示
                try:
                    after_text = await page.evaluate("document.body.innerText")
                    after_url = page.url
                    log_stderr(f"提交后 URL: {after_url[:100]}")
                    log_stderr(f"提交后文本: {after_text[:200]}")
                except Exception:
                    pass

            # Step 6: 直接导航到 balance 页面（不等 STS 重定向）
            emit({"type": "status", "message": "导航到 balance 页面..."})
            log_stderr("跳过 STS 重定向，直接导航到 balance 页面...")
            await page.goto(
                "https://platform.xiaomimimo.com/console/balance",
                wait_until="networkidle",
                timeout=60000,
            )
            await page.wait_for_timeout(3000)
            log_stderr(f"balance 页面 URL: {page.url[:100]}")

            # Step 7: 提取 Cookies
            emit({"type": "status", "message": "提取 Cookies..."})
            all_cookies = await context.cookies()

            platform_cookies = []
            for c in all_cookies:
                if "xiaomimimo.com" in c.get("domain", ""):
                    platform_cookies.append(f"{c['name']}={c['value']}")

            all_xiaomi_cookies = []
            for c in all_cookies:
                domain = c.get("domain", "")
                if "xiaomimimo.com" in domain or "xiaomi.com" in domain:
                    all_xiaomi_cookies.append(f"{c['name']}={c['value']}")

            platform_str = "; ".join(platform_cookies)
            all_str = "; ".join(all_xiaomi_cookies)

            emit({
                "type": "cookies",
                "platform": platform_str,
                "all": all_str,
            })

            # 检查关键 cookie
            key_cookies = ["api-platform_serviceToken", "userId", "api-platform_slh", "api-platform_ph"]
            found = [k for k in key_cookies if k in platform_str]
            if found:
                emit({"type": "status", "message": f"关键 cookie: {', '.join(found)}"})
            else:
                emit({"type": "status", "message": f"警告: 平台 cookie 中缺少关键字段，platform={platform_str[:100]}"})

            emit({"type": "done", "status": "success"})

        except Exception as e:
            import traceback
            emit({"type": "error", "message": f"{e}\n{traceback.format_exc()}"})
            emit({"type": "done", "status": "error"})
        finally:
            await browser.close()


if __name__ == "__main__":
    asyncio.run(main())
