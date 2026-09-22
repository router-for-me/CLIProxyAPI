const {test}=require('node:test');const assert=require('node:assert/strict');
const {permitted,safeRead,installReadOnlyGuard}=require('./management_readonly_guard.cjs');
test('allowlisted management reads and only POST login/logout are permitted',()=>{
 for(const p of ['/auth-files','/config.yaml','/billing/api-tokens','/plugins/test/config','/model-definitions/codex'])assert.ok(permitted('GET','/v0/management'+p));
 for(const p of ['/accounts/login','/accounts/logout'])assert.ok(permitted('POST','/v0/management'+p));
});
test('unsafe GETs, plugin resources and all platform writes fail closed',()=>{
 for(const p of ['/v0/management/get-auth-status','/v0/management/oauth-callback','/v0/management/codex-auth-url','/v0/management/plugin-start','/v0/resource/plugins/test/run','/callback','/unknown'])assert.equal(permitted('GET',p),false,p);
 for(const verb of ['POST','PUT','PATCH','DELETE'])for(const p of ['/config.yaml','/auth-files','/accounts/password','/accounts/users','/plugin-store/test/install','/billing/api-tokens/test'])assert.equal(permitted(verb,'/v0/management'+p),false);
 assert.equal(permitted('GET','/v0/management/accounts/login'),false);
 assert.equal(safeRead('/v0/management//config'),false);
});
test('guard blocks off-origin requests and returns explicit no-write response',async()=>{
 let handle,blocked=[],continued=0,fulfilled=[];
 await installReadOnlyGuard({route:async(_,fn)=>handle=fn},'http://target:8317',e=>blocked.push(e));
 const request=(url,method)=>({request:()=>({url:()=>url,method:()=>method}),continue:async()=>continued++,fulfill:async x=>fulfilled.push(x)});
 await handle(request('http://target:8317/v0/management/config','GET'));
 await handle(request('http://other/v0/management/config','GET'));
 await handle(request('http://target:8317/v0/management/config.yaml','PUT'));
 assert.equal(continued,1);assert.equal(blocked.length,2);assert.ok(fulfilled.every(x=>x.status===409));
});
