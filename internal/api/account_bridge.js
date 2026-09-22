/* Account-aware navigation for the upstream single-file management UI.
 * This is presentation only: all authorization is enforced by the server.
 */
(function () {
  'use strict';
  var session;
  try { session = JSON.parse(localStorage.getItem('cpa-account-session') || 'null'); } catch (_) {}
  function redirectLegacyLogin() {
    if (!/\/management\.html$/i.test(location.pathname)) return false;
    var loginRoute = /^#\/?login(?:[/?]|$)/i.test(location.hash);
    // The upstream app briefly visits /login while restoreSession authenticates.
    // Only an actual logout (isLoggedIn removed) should revoke the account token.
    if (session && session.token && localStorage.getItem('isLoggedIn') === 'true') {
      document.documentElement.style.visibility = loginRoute ? 'hidden' : '';
      return false;
    }
    // Retire the shared-key web login, including cached legacy login state.
    document.documentElement.style.visibility = 'hidden';
    if (session && session.token) {
      fetch('/v0/management/accounts/logout', {
        method: 'POST', headers: { Authorization: 'Bearer ' + session.token }, keepalive: true
      }).catch(function () {});
    }
    ['cpa-account-session', 'cli-proxy-auth', 'managementKey', 'isLoggedIn'].forEach(function (key) {
      localStorage.removeItem(key);
    });
    session = null;
    location.replace('/accounts.html');
    return true;
  }
  // Runs from <head> before the upstream bundle can render its old login form.
  if (redirectLegacyLogin()) return;
  function accountLink() {
    if (!document.body) return;
    var host = document.querySelector('.header-actions');
    var link = document.getElementById('cpa-account-link');
    if (!link) {
      link = document.createElement('a');
      link.id = 'cpa-account-link';
      link.href = '/accounts.html';
      link.textContent = '账号管理';
      link.setAttribute('aria-label', '账号管理');
      link.title = '个人资料、修改密码与用户管理';
    }
    if (host) {
      link.style.cssText = 'display:inline-flex;align-items:center;white-space:nowrap;padding:6px 10px;border:1px solid currentColor;border-radius:8px;color:inherit;text-decoration:none;font:600 13px system-ui';
      if (link.parentNode !== host) host.prepend(link);
    } else {
      link.style.cssText = 'position:fixed;right:16px;top:16px;z-index:2147483647;padding:10px 14px;border-radius:999px;background:#172554;color:white;text-decoration:none;font:600 13px system-ui;box-shadow:0 3px 12px #0004';
      if (link.parentNode !== document.body) document.body.appendChild(link);
    }
  }
  var restricted = false;
  function applyRole() {
    if (redirectLegacyLogin()) return;
    accountLink();
    if (!restricted) return;
    document.querySelectorAll('a[href]').forEach(function (link) {
      if (/billing|quota/i.test(link.getAttribute('href') || '')) {
        link.hidden = true;
        link.style.setProperty('display', 'none', 'important');
      }
    });
    if (/billing|quota/i.test(location.hash) || /\/billing\.html$/i.test(location.pathname)) {
      location.replace('/accounts.html');
    }
  }
  function ready() {
    restricted = !!(session && session.user && session.user.role !== 'admin');
    applyRole();
    var pending = false;
    new MutationObserver(function () {
      if (pending) return;
      pending = true;
      requestAnimationFrame(function () { pending = false; applyRole(); });
    }).observe(document.body, { childList: true, subtree: true });
    window.addEventListener('hashchange', applyRole);
    window.addEventListener('popstate', applyRole);
    window.addEventListener('storage', function (event) {
      if (event.key !== null && event.key !== 'cpa-account-session') return;
      var next;
      try { next = JSON.parse(localStorage.getItem('cpa-account-session') || 'null'); } catch (_) {}
      if (next && session && next.token === session.token) return;
      session = next;
      // Other tabs already updated shared storage; never revoke their new token.
      if (!next || !next.token) location.replace('/accounts.html');
      else location.reload();
    });
    if (!session || !session.token) return;
    var requestedToken = session.token;
    function stillCurrent() {
      try {
        var saved = JSON.parse(localStorage.getItem('cpa-account-session') || 'null');
        return !!(session && saved && session.token === requestedToken && saved.token === requestedToken);
      } catch (_) { return false; }
    }
    fetch('/v0/management/accounts/me', {
      headers: { Authorization: 'Bearer ' + requestedToken }, cache: 'no-store'
    }).then(function (response) {
      if (!stillCurrent()) return null;
      if (response.status === 401 || response.status === 403) {
        localStorage.removeItem('cpa-account-session');
        localStorage.removeItem('cli-proxy-auth');
        localStorage.removeItem('managementKey');
        localStorage.removeItem('isLoggedIn');
        location.replace('/accounts.html');
        return null;
      }
      if (!response.ok) throw new Error('account lookup failed');
      return response.json();
    }).then(function (data) {
      if (!data || !data.user || !stillCurrent()) return;
      session.user = data.user;
      localStorage.setItem('cpa-account-session', JSON.stringify(session));
      restricted = data.user.role !== 'admin';
      applyRole();
    }).catch(function () { /* Existing API auth still fails closed. */ });
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', ready);
  else ready();
})();
