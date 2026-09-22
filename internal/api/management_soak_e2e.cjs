// Non-destructive live browser soak. Default duration: 7200 real elapsed seconds.
// Env: NODE_PATH, CPA_TEST_BASE_URL, CPA_TEST_ADMIN_PASSWORD_FILE,
// CPA_TEST_USER_PASSWORD_FILE, CPA_SOAK_DIR. Optional CPA_SOAK_SECONDS (pilot only).
const {chromium}=require('playwright');
const {installReadOnlyGuard,safeRead}=require('./management_readonly_guard.cjs');
const fs=require('node:fs'),path=require('node:path'),assert=require('node:assert/strict');
const {performance}=require('node:perf_hooks');
const base=process.env.CPA_TEST_BASE_URL,dir=process.env.CPA_SOAK_DIR;
if(!base||!dir)throw new Error('Set CPA_TEST_BASE_URL and CPA_SOAK_DIR');
fs.mkdirSync(dir,{recursive:true,mode:0o700});
const duration=Number(process.env.CPA_SOAK_SECONDS||7200);
if(!Number.isFinite(duration)||duration<30)throw new Error('Duration must be >=30 seconds');
const report={state:'starting',targetSeconds:duration,startedAt:null,elapsedSeconds:0,rounds:0,checks:0,requests:0,responses:0,blocked:0,injectedFaults:0,recoveredFaultScenarios:0,cancelledRequests:0,pageErrors:[],unexpectedHTTP:[],unexpectedNetwork:[],failures:[],coverage:{},httpCounts:{},sessions:[],memorySamples:[]};
let epoch=0,current='startup';const live=[];let browser;
const wait=ms=>new Promise(r=>setTimeout(r,ms));
function event(type,data={}){fs.appendFileSync(path.join(dir,'events.jsonl'),JSON.stringify({at:new Date().toISOString(),elapsed:Math.round(epoch?(performance.now()-epoch)/1000:0),type,...data})+'\n',{mode:0o600})}
function snapshot(){report.elapsedSeconds=epoch?Math.floor((performance.now()-epoch)/1000):0;fs.writeFileSync(path.join(dir,'status.tmp'),JSON.stringify(report,null,2),{mode:0o600});fs.renameSync(path.join(dir,'status.tmp'),path.join(dir,'status.json'))}
function ok(name){report.checks++;report.coverage[name]=(report.coverage[name]||0)+1}
function pw(){return process.env.CPA_TEST_ADMIN_PASSWORD_FILE&&process.env.CPA_TEST_USER_PASSWORD_FILE}
async function makeSession(role,width){
 const context=await browser.newContext({viewport:{width,height:900},serviceWorkers:'block'});
 context.setDefaultTimeout(12000);context.setDefaultNavigationTimeout(20000);
 const s={context,role,width,id:role+'-'+width,token:null,expectedAbort:new WeakSet(),faults:null,portal:null,expectedHTTP:new WeakSet()};
 await installReadOnlyGuard(context,base,e=>{report.blocked++;event('blocked',{session:s.id,...e})});
 await context.route('**/v0/management/**',async route=>{
  const req=route.request(),p=new URL(req.url()).pathname;
  if(s.faults&&s.faults.has(p)&&s.faults.get(p)>0){s.faults.set(p,s.faults.get(p)-1);s.expectedAbort.add(req);report.injectedFaults++;return route.abort('connectionreset')}
  return route.fallback();
 });
 context.on('page',p=>{
  p.on('pageerror',e=>{report.pageErrors.push({session:s.id,scenario:current,message:e.message.slice(0,250)});event('pageerror',{session:s.id,scenario:current})});
  p.on('crash',()=>report.pageErrors.push({session:s.id,message:'renderer crashed'}));
  p.on('request',()=>report.requests++);
  p.on('response',r=>{
   const p=new URL(r.url()).pathname;report.responses++;report.httpCounts[r.status()]=(report.httpCounts[r.status()]||0)+1;
   if(r.status()>=400&&r.status()!==409&&!s.expectedHTTP.has(r.request())){
    const expected=s.role==='user'&&(p.includes('/billing/')||p.includes('/quota/')||p==='/v0/management/routing/quota-aware'||p.endsWith('/accounts/users'))&&r.status()===403;
    if(!expected)report.unexpectedHTTP.push({session:s.id,path:p,status:r.status(),scenario:current});
   }
  });
  p.on('requestfailed',r=>{
   if(s.expectedAbort.has(r))return;
   const error=r.failure()?.errorText||'';
   if(error.includes('ERR_ABORTED')){report.cancelledRequests++;return}
   report.unexpectedNetwork.push({session:s.id,path:new URL(r.url()).pathname,error,scenario:current});
  });
 });
 s.page=await context.newPage();
 await login(s);return s;
}
async function login(s){
 const p=s.page;await p.goto(base+'/accounts.html');
 await p.locator('#username').fill(s.role);
 const file=process.env[s.role==='admin'?'CPA_TEST_ADMIN_PASSWORD_FILE':'CPA_TEST_USER_PASSWORD_FILE'];
 await p.locator('#password').fill(fs.readFileSync(file,'utf8').trim());
 await p.locator('#loginForm button[type=submit]').click();await p.waitForURL('**/management.html#/');
 await p.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
 const saved=await p.evaluate(()=>JSON.parse(localStorage.getItem('cpa-account-session')));
 assert.equal(saved.user.role,s.role);s.token=saved.token;ok('login/'+s.id);
}
async function read(s,suffix){
 const full='/v0/management'+suffix;assert.ok(safeRead(full),'API helper path must be allowlisted');
 const r=await s.context.request.get(base+full,{headers:{Authorization:'Bearer '+s.token},timeout:15000});
 return r;
}
async function closeSession(s){
 if(s.token)await s.context.request.post(base+'/v0/management/accounts/logout',{headers:{Authorization:'Bearer '+s.token},timeout:15000}).catch(()=>{});
 await s.context.close().catch(()=>{});
}
const headings={'':'', 'quick-start':'Quick Start','auth-files':'Auth Files Management','oauth':'OAuth Login','ai-providers':'AI Providers','config':'Config Panel','logs':'Logs Viewer','system':'Management Center Info'};
async function navigate(s,route){
 const p=s.page,href='#/'+route;
 if(new URL(p.url()).pathname!=='/management.html')await p.goto(base+'/management.html');
 if(new URL(p.url()).hash!==href){
  const link=p.locator('a[href="'+href+'"]').first();
  const bounds=await link.boundingBox();
  if(!(await link.isVisible())||!bounds||bounds.x<0||bounds.x+bounds.width>p.viewportSize().width){
   const expand=p.getByRole('button',{name:'Expand sidebar',exact:true});
   if(await expand.isVisible())await expand.click();
  }
  if(await link.isVisible())await link.click();else{await p.goto(base+'/management.html'+href);await p.reload()}
 }
 await p.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
 if(headings[route])await p.getByRole('heading',{name:headings[route],exact:true,level:1}).waitFor({state:'visible'});
 assert.equal(new URL(p.url()).hash,href);
 await p.waitForTimeout(150);
 assert.equal(await p.getByText('Network Error',{exact:false}).count(),0,'No persistent Network Error');
 assert.equal(await p.locator('input[type=password]:visible').count(),0,'Legacy password form must remain hidden');
 const bb=await p.locator('#cpa-account-link').boundingBox();const v=p.viewportSize();
 assert.ok(bb&&bb.x>=0&&bb.x+bb.width<=v.width+2&&bb.y<120,'Account action stays in header viewport');
}
async function authControls(s){
 const p=s.page;await navigate(s,'auth-files');
 const input=p.getByRole('textbox',{name:'Search configs',exact:true});
 await input.fill('__readonly_soak_no_match__');await p.waitForTimeout(450);
 assert.equal(await p.locator('input[aria-label^="Select credential "]').count(),0);
 await input.fill('');await p.waitForTimeout(450);
 const model=p.getByTitle('Models',{exact:true}).first();
 if(await model.isVisible()){
  await model.click();const modal=p.locator('.modal-overlay');await modal.waitFor({state:'visible'});
  await modal.getByRole('button',{name:'Close',exact:true}).first().click();await modal.waitFor({state:'hidden'});
 }
 await p.getByRole('button',{name:'Refresh',exact:true}).click();
 await p.waitForTimeout(450);assert.equal(await p.getByText('Network Error',{exact:false}).count(),0);
 ok('credential-filter-models-refresh/'+s.id);
}
async function portalCheck(s){
 if(!s.portal||s.portal.isClosed())s.portal=await s.context.newPage();
 await s.portal.goto(base+'/accounts.html');await s.portal.getByText('资料已更新。',{exact:true}).waitFor();
 assert.equal(await s.portal.locator('#adminPanel').isVisible(),s.role==='admin');
 if(s.role==='admin'){
  await s.portal.getByText('用户列表已更新。',{exact:true}).waitFor();
  const count=await s.portal.locator('#usersBody tr').count();
  await s.page.reload();await s.page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
  await s.portal.waitForTimeout(250);assert.equal(await s.portal.locator('#usersBody tr').count(),count,'same-token update preserves admin rows');
 }
 ok('parallel-portal/'+s.id);
}
async function billingCheck(s){
 const p=await s.context.newPage();try{
  await p.goto(base+'/billing.html');
  if(s.role==='user')await p.waitForURL('**/accounts.html');
  else{await p.locator('header').waitFor({state:'visible'});await p.waitForTimeout(350)}
 }finally{await p.close()}
 ok('billing-role-navigation/'+s.id);
}
async function quotaCheck(s){
 const p=await s.context.newPage();try{
  await p.goto(base+'/management.html#/quota');
  if(s.role==='user')await p.waitForURL('**/accounts.html');else await p.locator('#cpa-account-link').waitFor({state:'visible'});
 }finally{await p.close()}
 ok('quota-role-navigation/'+s.id);
}
async function historyCheck(s){
 await navigate(s,'auth-files');await navigate(s,'quick-start');await s.page.goBack();
 await s.page.getByRole('heading',{name:'Auth Files Management',exact:true,level:1}).waitFor();
 await s.page.goForward();await s.page.getByRole('heading',{name:'Quick Start',exact:true,level:1}).waitFor();
 ok('browser-history/'+s.id);
}
async function permissionsAndMetrics(s){
 for(const p of ['/billing/api-tokens','/quota/providers','/accounts/users'])assert.equal((await read(s,p)).status(),s.role==='admin'?200:403,p);
 assert.equal((await read(s,'/accounts/me')).status(),200,'long-running session remains valid');
 assert.equal((await read(s,'/plugins')).status(),200);
 const cfg=await read(s,'/config');assert.equal(cfg.status(),200);
 if(s.role==='user'){const obj=await cfg.json();assert.equal(Object.hasOwn(obj,'billing'),false);assert.equal(Object.hasOwn(obj,'remote-management'),false)}
 const cdp=await s.context.newCDPSession(s.page);let heap;
 try{await cdp.send('Performance.enable');const m=await cdp.send('Performance.getMetrics');heap=m.metrics.find(x=>x.name==='JSHeapUsedSize')?.value||0}finally{await cdp.detach()}
 report.memorySamples.push({elapsed:Math.floor((performance.now()-epoch)/1000),session:s.id,heapBytes:heap,nodes:await s.page.locator('*').count()});
 ok('permissions-session-heap/'+s.id);
}
async function networkCheck(s){
 await navigate(s,'auth-files');
 s.faults=new Map(['/auth-files','/oauth-excluded-models','/oauth-model-alias'].map(p=>['/v0/management'+p,2]));
 try{
  await s.page.reload();await s.page.getByRole('heading',{name:'Auth Files Management',exact:true,level:1}).waitFor();
  await s.page.waitForTimeout(2500);
  assert.ok([...s.faults.values()].every(n=>n===0),'all three read endpoints exercised');
  assert.equal(await s.page.getByText('Network Error',{exact:false}).count(),0);
  assert.equal((await read(s,'/accounts/me')).status(),200);
  report.recoveredFaultScenarios++;ok('two-reset-recovery/'+s.id);
 }finally{s.faults=null}
}
async function churn(role){
 const s=await makeSession(role,390);try{
  const portal=await s.context.newPage();await portal.goto(base+'/accounts.html');await portal.getByText('资料已更新。',{exact:true}).waitFor();
  await portal.locator('#logoutBtn').click();await s.page.waitForURL('**/accounts.html');await portal.close();
  let rejected=false;for(let i=0;i<20;i++){const r=await read(s,'/accounts/me');if(r.status()===401||r.status()===403){rejected=true;break}await wait(100)}
  assert.ok(rejected,'server rejects the logged-out token');s.token=null;
  ok('fresh-context-cross-tab-logout/'+role);
 }finally{await closeSession(s)}
}
async function guardChecks(s){
 const before=report.blocked;
 for(const [method,p] of [['PUT','/config.yaml'],['POST','/auth-files/refresh'],['PATCH','/billing/api-tokens/soak-guard-probe'],['POST','/plugin-store/soak-guard-probe/install'],['GET','/get-auth-status'],['GET','/codex-auth-url'],['GET','/oauth-callback'],['GET','/unknown-plugin-operation']]){
  const status=await s.page.evaluate(async({method,p})=>(await fetch('/v0/management'+p,{method})).status,{method,p});assert.equal(status,409);
 }
 assert.equal(report.blocked-before,8);ok('guard-rejects-platform-writes-and-unsafe-GET');
}
async function main(){
 if(!pw())throw new Error('Password files are required');browser=await chromium.launch({headless:true});
 try{
  for(const role of ['admin','user'])for(const width of [1440,390]){const s=await makeSession(role,width);live.push(s);report.sessions.push({role,width});}
  await guardChecks(live[0]);
  epoch=performance.now();report.startedAt=new Date().toISOString();report.state='running';snapshot();event('soak-start',{duration});
  let nextMetrics=0,nextFault=0,nextChurn=0,nextHeartbeat=0;
  while(performance.now()-epoch<duration*1000){
   const roundStart=performance.now(),round=report.rounds;
   for(const s of live){
    current=s.id+'/round-'+round;const phase=round%12;
    if(phase<8){const route=['','quick-start','auth-files','oauth','ai-providers','config','logs','system'][phase];await navigate(s,route);ok('navigation/'+s.id+'/'+(route||'dashboard'));if(route==='auth-files')await authControls(s);}
    else if(phase===8)await portalCheck(s);
    else if(phase===9)await billingCheck(s);
    else if(phase===10)await quotaCheck(s);
    else await historyCheck(s);
    if(round%16===4){await s.page.emulateMedia({colorScheme:round%32===4?'dark':'light'});ok('theme-media/'+s.id)}
    if(round%24===12){await s.page.setViewportSize({width:s.width===390?320:1024,height:900});await navigate(s,'auth-files');await s.page.setViewportSize({width:s.width,height:900});ok('responsive-resize/'+s.id)}
   }
   const elapsed=(performance.now()-epoch)/1000;
   if(elapsed>=nextFault){current='fault-injection';await networkCheck(live[Math.floor(nextFault/600)%live.length]);nextFault+=600;}
   if(elapsed>=nextMetrics){current='session-and-memory';for(const s of live)await permissionsAndMetrics(s);nextMetrics+=300;}
   if(elapsed>=nextChurn){current='session-churn';await churn(Math.floor(nextChurn/900)%2?'user':'admin');nextChurn+=900;}
   assert.equal(report.pageErrors.length,0,'No unhandled browser errors');
   assert.equal(report.unexpectedNetwork.length,0,'No unexpected transport failures');
   assert.equal(report.unexpectedHTTP.length,0,'No unexpected HTTP failures');
   report.rounds++;snapshot();
   if(elapsed>=nextHeartbeat){console.log(JSON.stringify({state:report.state,elapsedSeconds:report.elapsedSeconds,rounds:report.rounds,checks:report.checks,requests:report.requests,recoveredFaultScenarios:report.recoveredFaultScenarios,blocked:report.blocked}));nextHeartbeat+=60;}
   const remaining=duration*1000-(performance.now()-epoch);
   await wait(Math.max(0,Math.min(15000-(performance.now()-roundStart),remaining)));
  }
  current='final-long-session-readback';for(const s of live)await permissionsAndMetrics(s);
  report.state='complete';report.finishedAt=new Date().toISOString();snapshot();event('soak-complete',{checks:report.checks,elapsedSeconds:report.elapsedSeconds});
 }catch(e){
  report.state='failed';report.failures.push({scenario:current,error:e.message.slice(0,2000)});snapshot();event('failure',{scenario:current,error:e.message.slice(0,2000)});process.exitCode=1;
  const affected=live.find(s=>current.startsWith(s.id));if(affected)await affected.page.screenshot({path:path.join(dir,'failure.png'),fullPage:true}).catch(()=>{});
 }finally{
  for(const s of live)await closeSession(s);if(browser)await browser.close();snapshot();
  console.log(JSON.stringify({state:report.state,elapsedSeconds:report.elapsedSeconds,checks:report.checks,failures:report.failures,unexpectedHTTP:report.unexpectedHTTP.slice(-5),unexpectedNetwork:report.unexpectedNetwork.slice(-5)}));
 }
}
main().catch(e=>{report.state='failed';report.failures.push({scenario:current,error:e.message.split('\n')[0]});snapshot();console.error(e.message.split('\n')[0]);process.exitCode=1});
