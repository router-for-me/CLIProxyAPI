// API 基础配置
const API_BASE = '/api';

// 管理 API Key（可通过 localStorage.managementApiKey 注入）
function getManagementHeaders() {
  const apiKey = localStorage.getItem('managementApiKey') || '';
  if (!apiKey) return {};
  return { 'X-API-Key': apiKey };
}

// API 调用封装
async function fetchAPI(endpoint, options = {}) {
  try {
    const response = await fetch(`${API_BASE}${endpoint}`, {
      headers: { 'Content-Type': 'application/json', ...getManagementHeaders() },
      ...options
    });
    return await response.json();
  } catch (error) {
    console.error('API Error:', error);
    return null;
  }
}

// 获取服务状态
async function getStatus() {
  return await fetchAPI('/status');
}

// 获取账号列表
async function getAccounts() {
  return await fetchAPI('/accounts');
}

// 启动登录流程
async function startLoginAPI() {
  return await fetchAPI('/auth/login', { method: 'POST' });
}

// 刷新账号 token
async function refreshAccountAPI(email) {
  return await fetchAPI(`/accounts/${encodeURIComponent(email)}/refresh`, {
    method: 'POST'
  });
}

// 检查 token 是否过期
function isExpired(expireStr) {
  if (!expireStr) return true;
  return new Date(expireStr) < new Date();
}

// 格式化日期
function formatDate(dateStr) {
  if (!dateStr) return '--';
  const date = new Date(dateStr);
  return date.toLocaleString('zh-CN');
}

// 更新 Dashboard 状态
async function updateDashboard() {
  const status = await getStatus();
  const accounts = await getAccounts();

  if (status) {
    document.getElementById('status-text').textContent =
      status.status === 'running' ? '运行中' : '已停止';
    document.getElementById('accounts-count').textContent =
      status.accounts_count || 0;
  }

  if (accounts && accounts.accounts) {
    const list = accounts.accounts;
    let active = 0, expired = 0;

    list.forEach(acc => {
      if (isExpired(acc.expire)) expired++;
      else active++;
    });

    document.getElementById('active-count').textContent = active;
    document.getElementById('expired-count').textContent = expired;
    renderAccountsTable(list);
  }
}

// 渲染账号表格
function renderAccountsTable(accounts) {
  const tbody = document.getElementById('accounts-table');
  if (!tbody) return;

  if (!accounts || accounts.length === 0) {
    tbody.innerHTML = `<tr><td colspan="5" style="text-align:center;color:var(--text-secondary)">
      暂无账号，请添加账号
    </td></tr>`;
    return;
  }

  tbody.innerHTML = accounts.map(acc => {
    const expired = isExpired(acc.expire);
    const statusBadge = expired
      ? '<span class="badge badge-danger">已过期</span>'
      : '<span class="badge badge-success">有效</span>';

    return `<tr>
      <td>${acc.email || '--'}</td>
      <td>${acc.account_id || '--'}</td>
      <td>${statusBadge}</td>
      <td>${formatDate(acc.expire)}</td>
      <td>
        <button class="btn btn-sm btn-outline" onclick="refreshAccount('${acc.email}')">
          刷新
        </button>
      </td>
    </tr>`;
  }).join('');
}

// 渲染账号卡片列表
async function renderAccountCards() {
  const container = document.getElementById('accounts-list');
  if (!container) return;

  const data = await getAccounts();
  if (!data || !data.accounts) {
    container.innerHTML = '<p style="color:var(--text-secondary)">加载失败</p>';
    return;
  }

  const accounts = data.accounts;
  if (accounts.length === 0) {
    container.innerHTML = `<div class="glass-card" style="grid-column:1/-1;text-align:center">
      <p style="color:var(--text-secondary)">暂无账号，点击上方按钮添加</p>
    </div>`;
    return;
  }

  container.innerHTML = accounts.map(acc => {
    const expired = isExpired(acc.expire);
    const statusClass = expired ? 'danger' : 'success';
    const statusText = expired ? '已过期' : '有效';

    return `<div class="glass-card">
      <div style="display:flex;justify-content:space-between;align-items:start;margin-bottom:12px">
        <span class="badge badge-${statusClass}">${statusText}</span>
      </div>
      <h3 style="font-size:1rem;margin-bottom:8px">${acc.email || '未知邮箱'}</h3>
      <p style="color:var(--text-secondary);font-size:0.875rem;margin-bottom:4px">
        ID: ${acc.account_id || '--'}
      </p>
      <p style="color:var(--text-secondary);font-size:0.75rem;margin-bottom:16px">
        过期: ${formatDate(acc.expire)}
      </p>
      <button class="btn btn-sm btn-outline" style="width:100%" onclick="refreshAccount('${acc.email}')">
        🔄 刷新 Token
      </button>
    </div>`;
  }).join('');
}

// 打开登录模态框
function startLogin() {
  const modal = document.getElementById('login-modal');
  if (modal) modal.classList.add('active');
}

// 关闭模态框
function closeModal() {
  const modal = document.getElementById('login-modal');
  if (modal) modal.classList.remove('active');
  const urlDiv = document.getElementById('login-url');
  if (urlDiv) urlDiv.style.display = 'none';
}

// 获取授权链接
async function getAuthUrl() {
  const btn = document.getElementById('get-url-btn');
  btn.textContent = '获取中...';
  btn.disabled = true;

  const data = await startLoginAPI();
  if (data && data.auth_url) {
    document.getElementById('auth-link').href = data.auth_url;
    document.getElementById('login-url').style.display = 'block';
    btn.style.display = 'none';
  } else {
    alert('获取授权链接失败');
    btn.textContent = '获取授权链接';
    btn.disabled = false;
  }
}

// 刷新账号 token
async function refreshAccount(email) {
  if (!confirm(`确定要刷新 ${email} 的 Token 吗？`)) return;

  const result = await refreshAccountAPI(email);
  if (result && result.message) {
    alert('刷新成功');
    refreshData();
  } else {
    alert('刷新失败: ' + (result?.error || '未知错误'));
  }
}

// 刷新数据
function refreshData() {
  if (document.getElementById('accounts-table')) {
    updateDashboard();
  }
  if (document.getElementById('accounts-list')) {
    renderAccountCards();
  }
}

// 页面初始化
document.addEventListener('DOMContentLoaded', () => {
  refreshData();
});
