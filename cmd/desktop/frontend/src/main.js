import { BrowserOpenURL } from '../wailsjs/runtime/runtime';

// Wails 桌面端前端入口 — 自动跳转到后端管理面板
document.getElementById('app').innerHTML = '<h2>CLIProxyAPI Desktop</h2><p>Loading...</p>';

// 使用固定端口：默认配置即 8317。若用户改端口，可直接手动访问对应地址。
BrowserOpenURL('http://127.0.0.1:8317/management.html');
