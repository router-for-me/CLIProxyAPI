import { GetServerPort } from '../wailsjs/go/main/App';

// Wails 桌面端前端入口 — 在窗口内加载后端管理面板
document.getElementById('app').innerHTML =
  '<h2 style="text-align:center;margin-top:40vh;color:#ccc">CLIProxyAPI Desktop</h2>' +
  '<p style="text-align:center;color:#888">正在等待服务启动…</p>';

async function waitForServerAndNavigate() {
  const port = await GetServerPort();
  const url = `http://127.0.0.1:${port}/management.html`;

  // 轮询后端直到可用，最多重试 30 次（约 15 秒）
  for (let i = 0; i < 30; i++) {
    try {
      const resp = await fetch(url, { method: 'HEAD', mode: 'no-cors' });
      // mode: no-cors 下 resp.type 为 "opaque"，status 为 0，但不抛异常就说明服务已启动
      break;
    } catch {
      await new Promise((r) => setTimeout(r, 500));
    }
  }

  window.location.href = url;
}

waitForServerAndNavigate();
