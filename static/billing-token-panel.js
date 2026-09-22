/* CPA Billing API Token Nav Link v3 */
(function(){
  'use strict';
  const NAV_ID='cpa-billing-token-nav-item';
  const NAV_STYLE_ID='cpa-billing-token-nav-style';
  const FALLBACK_ID='cpa-billing-token-floating-link';
  if(window.__cpaBillingTokenNavLoaded) return;
  window.__cpaBillingTokenNavLoaded=true;
  function installStyle(){
    if(document.getElementById(NAV_STYLE_ID)) return;
    const style=document.createElement('style'); style.id=NAV_STYLE_ID;
    style.textContent=`#${NAV_ID}{display:flex;align-items:center;gap:10px;width:100%;box-sizing:border-box;border:0;background:transparent;color:inherit;border-radius:10px;padding:10px 12px;margin:2px 0;cursor:pointer;font:600 14px Inter,ui-sans-serif,system-ui,-apple-system,BlinkMacSystemFont,"Segoe UI",sans-serif;text-align:left;text-decoration:none}#${NAV_ID}:hover{background:rgba(59,130,246,.14);color:inherit}#${NAV_ID}.active{background:rgba(37,99,235,.2);color:#bfdbfe}#${NAV_ID} .cpa-icon{width:20px;text-align:center;flex:0 0 20px}#${NAV_ID} .cpa-label{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}#${FALLBACK_ID}{position:fixed;right:18px;bottom:18px;z-index:2147483647;border:1px solid rgba(148,163,184,.45);background:linear-gradient(135deg,#0f172a,#1d4ed8);color:white;border-radius:999px;padding:10px 14px;font:600 13px system-ui;box-shadow:0 12px 30px rgba(15,23,42,.32);cursor:pointer;text-decoration:none;display:none}#${FALLBACK_ID}.show{display:inline-flex;align-items:center;gap:6px}`;
    document.head.appendChild(style);
  }
  function makeLink(){
    let item=document.getElementById(NAV_ID);
    if(!item){
      item=document.createElement('a'); item.id=NAV_ID; item.href='/billing.html'; item.title='API Token Billing / Quota'; item.innerHTML='<span class="cpa-icon">💳</span><span class="cpa-label">Token Billing</span>';
      item.addEventListener('click',()=>{try{sessionStorage.setItem('cpa-return-management-url', location.href)}catch{}});
    }
    if(location.pathname==='/billing.html') item.classList.add('active'); else item.classList.remove('active');
    return item;
  }
  function ensureFallback(show){
    let link=document.getElementById(FALLBACK_ID);
    if(!link){link=document.createElement('a'); link.id=FALLBACK_ID; link.href='/billing.html'; link.textContent='💳 Token Billing'; document.body.appendChild(link)}
    link.classList.toggle('show', !!show);
  }
  function attachNav(){
    installStyle();
    const item=makeLink();
    const candidates=Array.from(document.querySelectorAll('aside nav, aside [role="navigation"], aside, nav, [role="navigation"], [class*="sidebar"], [class*="Sidebar"], [class*="sideBar"]'));
    let best=null, bestScore=-1;
    for(const el of candidates){
      if(!el) continue;
      if(el.contains(item)){ensureFallback(false); return true;}
      const r=el.getBoundingClientRect();
      if(!r || r.width<80 || r.height<160 || r.left>Math.max(420, window.innerWidth*0.45)) continue;
      const text=(el.textContent||'').toLowerCase();
      const links=el.querySelectorAll('button,a,[role="button"]').length;
      let score=links*3 + Math.min(20, r.height/30) + (r.left<80?20:0) + (/dashboard|providers|logs|config|管理|仪表盘|日志|配置/.test(text)?30:0);
      if(score>bestScore){bestScore=score; best=el;}
    }
    if(!best){ensureFallback(true); return false;}
    const target=best.querySelector('nav') || best;
    target.appendChild(item);
    ensureFallback(false);
    return true;
  }
  function boot(){
    if(!document.body){window.addEventListener('DOMContentLoaded',boot,{once:true}); return;}
    let attached=attachNav();
    let attempts=0;
    const observer=new MutationObserver(()=>{if(attached) return; attached=attachNav(); if(attached) observer.disconnect();});
    observer.observe(document.body,{childList:true,subtree:true});
    const timer=setInterval(()=>{attempts++; attached=attachNav(); if(attached||attempts>40){clearInterval(timer); if(!attached) ensureFallback(true); observer.disconnect();}},500);
  }
  boot();
})();
