// Wails 桌面端前端入口 — 自动跳转到后端管理面板
document.getElementById('app').innerHTML = '<h2>CLIProxyAPI Desktop</h2><p>Loading...</p>';

// 后端路由为 /management.html，由 managementasset 包自动从 GitHub Releases 下载
const port = 8317;
window.location.href = `http://127.0.0.1:${port}/management.html`;
