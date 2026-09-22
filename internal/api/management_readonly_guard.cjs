// Default-deny browser request guard: GET alone does NOT imply side-effect free.
const paths=new Set([
 '/accounts/me','/accounts/users','/config','/config.yaml','/latest-version',
 '/plugins','/plugin-store','/debug','/logging-to-file','/logs-max-total-size-mb',
 '/error-logs-max-files','/usage-statistics-enabled','/proxy-url',
 '/quota-exceeded/switch-project','/quota-exceeded/switch-preview-model','/quota/providers',
 '/api-keys','/api-key-usage','/usage-queue','/gemini-api-key','/interactions-api-key',
 '/claude-api-key','/codex-api-key','/xai-api-key','/meta-api-key','/openai-compatibility',
 '/vertex-api-key','/oauth-excluded-models','/oauth-model-alias','/oauth-request-scoped-errors',
 '/auth-files','/auth-files/models','/logs','/request-error-logs','/request-log','/ws-auth',
 '/request-retry','/max-retry-credentials','/max-retry-interval','/force-model-prefix',
 '/routing/strategy','/routing/quota-aware','/billing/usage','/billing/usage/export',
 '/billing/price-books/current','/billing/api-tokens'
]);
const prefix='/v0/management';
function safeRead(path){
 if(!path.startsWith(prefix+'/'))return false;
 const p=path.slice(prefix.length);
 return paths.has(p)||/^\/plugins\/[^/]+\/(config|quota)$/.test(p)||
 /^\/model-definitions\/[^/]+$/.test(p)||/^\/request-error-logs\/[^/]+$/.test(p)||
 /^\/request-log-by-id\/[^/]+$/.test(p)||/^\/billing\/usage\/[^/]+$/.test(p);
}
function permitted(method,path){
 if(method==='POST'&&['/v0/management/accounts/login','/v0/management/accounts/logout'].includes(path))return true;
 if(method!=='GET')return false;
 if(path.startsWith(prefix+'/'))return safeRead(path);
 if(path.startsWith('/v0/'))return false;
 return ['/accounts.html','/management.html','/billing.html','/account-bridge.js',
 '/billing-token-panel.js','/favicon.ico','/v1/models'].includes(path)||path.startsWith('/assets/');
}
async function installReadOnlyGuard(context,base,onBlocked=()=>{}){
 const origin=new URL(base).origin;
 await context.route('**/*',async route=>{
  const req=route.request(),u=new URL(req.url());
  if(u.origin===origin&&permitted(req.method(),u.pathname))return route.continue();
  onBlocked({method:req.method(),path:u.pathname,reason:u.origin===origin?'not-allowlisted':'other-origin'});
  return route.fulfill({status:409,contentType:'application/json',body:'{"error":"Read-only regression guard: request blocked; no platform state changed"}'});
 });
 if(context.routeWebSocket)await context.routeWebSocket('**/*',socket=>{
  onBlocked({method:'WEBSOCKET',path:new URL(socket.url()).pathname,reason:'not-allowlisted'});socket.close();
 });
}
module.exports={safeRead,permitted,installReadOnlyGuard};
