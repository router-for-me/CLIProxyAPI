// Live, configuration-preserving Playwright regression. Never sends platform writes.
// Required: NODE_PATH=.../node_modules, CPA_TEST_BASE_URL,
// CPA_TEST_ADMIN_PASSWORD_FILE, CPA_TEST_USER_PASSWORD_FILE. Optional CPA_TEST_REPORT.
const {chromium}=require('playwright');
const {installReadOnlyGuard}=require('./management_readonly_guard.cjs');
const fs=require('node:fs');
const assert=require('node:assert/strict');
const base=process.env.CPA_TEST_BASE_URL;
if(!base)throw new Error('CPA_TEST_BASE_URL is required');
const report={cases:[],blockedWrites:[],pageErrors:[]};
async function check(name,fn){try{await fn();report.cases.push({name,pass:true});console.log('PASS',name)}catch(e){report.cases.push({name,pass:false,error:e.message.split('\n')[0]});console.log('FAIL',name,e.message.split('\n')[0])}}
async function run(){
 const browser=await chromium.launch();
 try{
 for(const role of ['admin','user'])for(const width of [1440,390]){
  const prefix=role+'/'+width;
  const context=await browser.newContext({viewport:{width,height:900},serviceWorkers:'block'});
  const tokens=new Set();
  await installReadOnlyGuard(context,base,e=>report.blockedWrites.push(e));
  const page=await context.newPage();
  context.on('page',p=>p.on('pageerror',e=>report.pageErrors.push(e.message)));
  page.on('pageerror',e=>report.pageErrors.push(e.message));
  async function login(){
   await page.goto(base+'/accounts.html');
   await page.locator('#username').fill(role);
   const file=process.env[role==='admin'?'CPA_TEST_ADMIN_PASSWORD_FILE':'CPA_TEST_USER_PASSWORD_FILE'];
   await page.locator('#password').fill(fs.readFileSync(file,'utf8').trim());
   await page.locator('#loginForm button[type=submit]').click();
   await page.waitForURL('**/management.html#/',{timeout:15000});
   await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
   const s=await page.evaluate(()=>JSON.parse(localStorage.getItem('cpa-account-session')));
   assert.equal(s.user.role,role);tokens.add(s.token);return s.token;
  }
  try{
   await check(prefix+' anonymous old-login entry',async()=>{await page.goto(base+'/management.html#/login');await page.waitForURL('**/accounts.html');assert.ok(await page.locator('#loginCard').isVisible())});
   let token;
   await check(prefix+' login and header layout',async()=>{token=await login();const b=await page.locator('#cpa-account-link').boundingBox();assert.ok(b.x>=0&&b.x+b.width<=width+1&&b.y<120);assert.equal(await page.locator('input[type=password]:visible').count(),0)});
   if(!token)continue;
   await check(prefix+' reload and authenticated old-login entry',async()=>{await page.reload();await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});await page.goto(base+'/management.html#/login');await page.waitForURL('**/management.html#/');assert.equal((await context.request.get(base+'/v0/management/accounts/me',{headers:{Authorization:'Bearer '+token}})).status(),200)});
   for(const route of ['quick-start','auth-files','oauth','ai-providers','config','logs','system']){
    await check(prefix+' page '+route,async()=>{
     await page.goto(base+'/management.html#/'+route);await page.reload();
     await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
     await page.waitForTimeout(400);
     assert.equal(new URL(page.url()).hash,'#/'+route);
     assert.ok(await page.locator('main h1,h1').first().isVisible());
     assert.equal(await page.getByText('Network Error',{exact:false}).count(),0);
    });
   }
   await check(prefix+' billing navigation',async()=>{await page.goto(base+'/billing.html');if(role==='user'){await page.waitForURL('**/accounts.html');await page.locator('#appCards').waitFor({state:'visible'})}else{await page.waitForTimeout(800);assert.equal(new URL(page.url()).pathname,'/billing.html');assert.ok(await page.locator('header').isVisible())}});
   await check(prefix+' direct quota navigation',async()=>{await page.goto(base+'/management.html#/quota');if(role==='user')await page.waitForURL('**/accounts.html');else{await page.locator('#cpa-account-link').waitFor({state:'visible'});assert.equal(new URL(page.url()).hash,'#/quota')}});
   await check(prefix+' server role boundaries',async()=>{for(const endpoint of ['/billing/api-tokens','/quota/providers','/accounts/users']){const r=await context.request.get(base+'/v0/management'+endpoint,{headers:{Authorization:'Bearer '+token}});assert.equal(r.status(),role==='admin'?200:403,endpoint)}});
   await check(prefix+' allowed read APIs and protected config redaction',async()=>{
    for(const endpoint of ['/config','/auth-files','/plugins','/api-key-usage','/oauth-excluded-models','/oauth-model-alias']){
     const r=await context.request.get(base+'/v0/management'+endpoint,{headers:{Authorization:'Bearer '+token}});assert.equal(r.status(),200,endpoint);
     if(endpoint==='/config'&&role==='user'){const cfg=await r.json();assert.equal(Object.hasOwn(cfg,'billing'),false);assert.equal(Object.hasOwn(cfg,'remote-management'),false)}
    }
   });
   await check(prefix+' account portal role and return',async()=>{await page.goto(base+'/accounts.html');await page.getByText('资料已更新。',{exact:true}).waitFor();assert.equal(await page.locator('#adminPanel').isVisible(),role==='admin');await page.getByRole('link',{name:'主控制台',exact:true}).click();await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'})});
   if(width===1440){
    await check(prefix+' config edit/save intercepted, no write',async()=>{
     await page.goto(base+'/management.html#/config');await page.reload();await page.getByLabel('Host Address',{exact:true}).fill('127.0.0.2');
     const before=report.blockedWrites.length;
     await page.getByRole('button',{name:'Save changes',exact:true}).click();await page.getByRole('button',{name:'Confirm Save',exact:true}).click();
     await page.waitForTimeout(400);assert.ok(report.blockedWrites.slice(before).some(r=>r.method==='PUT'&&r.path.endsWith('/config.yaml')));
     await page.reload();
    });
   }
   if(width===1440){
    await check(prefix+' password change intercepted, session unaffected',async()=>{
     await page.goto(base+'/accounts.html');await page.getByText('资料已更新。',{exact:true}).waitFor();
     const before=report.blockedWrites.length;
     await page.locator('#currentPassword').fill('read-only-test-not-a-real-password');await page.locator('#newPassword').fill('read-only-test-not-persisted');
     await page.locator('#passwordForm button[type=submit]').click();await page.waitForTimeout(200);
     assert.ok(report.blockedWrites.slice(before).some(r=>r.method==='PUT'&&r.path.endsWith('/accounts/password')));
     assert.equal((await context.request.get(base+'/v0/management/accounts/me',{headers:{Authorization:'Bearer '+token}})).status(),200);
    });
    if(role==='admin')await check(prefix+' user creation intercepted, no account created',async()=>{
     const before=report.blockedWrites.length;
     await page.locator('#newUsername').fill('readonly-e2e-not-created');await page.locator('#newUserPassword').fill('read-only-test-not-persisted');
     await page.locator('#createUserForm button[type=submit]').click();await page.waitForTimeout(200);
     assert.ok(report.blockedWrites.slice(before).some(r=>r.method==='POST'&&r.path.endsWith('/accounts/users')));
     const r=await context.request.get(base+'/v0/management/accounts/users',{headers:{Authorization:'Bearer '+token}});
     assert.ok(!(await r.json()).users.some(u=>u.username==='readonly-e2e-not-created'));
    });
   }
   await check(prefix+' cross-tab logout',async()=>{
    await page.goto(base+'/management.html#/');await page.reload();await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
    const other=await context.newPage();try{await other.goto(base+'/accounts.html');await other.getByText('资料已更新。',{exact:true}).waitFor();await other.locator('#logoutBtn').click();
    await page.waitForURL('**/accounts.html',{timeout:5000});await page.locator('#loginCard').waitFor({state:'visible'});}finally{await other.close();}
    let status;for(let i=0;i<20;i++){status=(await context.request.get(base+'/v0/management/accounts/me',{headers:{Authorization:'Bearer '+token}})).status();if(status===401||status===403)break;await page.waitForTimeout(100)}assert.ok(status===401||status===403,'logged-out token must be rejected');
   });
   if(width===1440)await check(prefix+' delayed profile response cannot restore logout',async()=>{
    token=await login();
    const other=await context.newPage();let release;const gate=new Promise(r=>release=r);let seen;const started=new Promise(r=>seen=r);
    await other.route('**/v0/management/accounts/me',async route=>{const response=await route.fetch();seen();await gate;await route.fulfill({response})});
    try{
     await other.goto(base+'/accounts.html');await started;await other.locator('#logoutBtn').click();release();
     await other.locator('#loginCard').waitFor({state:'visible'});await other.waitForTimeout(300);
     assert.equal(await other.evaluate(()=>localStorage.getItem('cpa-account-session')),null);
     await page.waitForURL('**/accounts.html',{timeout:5000});
    }finally{release();await other.close()}
   });
   await check(prefix+' expired account portal session',async()=>{
    await page.goto(base+'/accounts.html');await page.evaluate(()=>localStorage.setItem('cpa-account-session',JSON.stringify({token:'expired-browser-regression',user:{username:'expired',role:'admin'}})));
    await page.route('**/v0/management/accounts/me',r=>r.fulfill({status:role==='admin'?401:403,contentType:'application/json',body:'{"error":"session is no longer authorized"}'}));await page.route('**/v0/management/accounts/logout',r=>r.fulfill({status:200,contentType:'application/json',body:'{}'}));await page.reload();await page.locator('#loginCard').waitFor({state:'visible',timeout:5000});assert.equal(await page.evaluate(()=>localStorage.getItem('cpa-account-session')),null);
   });
  }finally{
   for(const token of tokens)await context.request.post(base+'/v0/management/accounts/logout',{headers:{Authorization:'Bearer '+token}}).catch(()=>{});
   await context.close();
  }
 }
 }finally{await browser.close()}
 report.summary={passed:report.cases.filter(c=>c.pass).length,failed:report.cases.filter(c=>!c.pass).length,pageErrors:report.pageErrors.length,interceptedWrites:report.blockedWrites.length};
 if(process.env.CPA_TEST_REPORT)fs.writeFileSync(process.env.CPA_TEST_REPORT,JSON.stringify(report,null,2),{mode:0o600});
 console.log(JSON.stringify(report.summary));
 if(report.summary.failed||report.pageErrors.length)process.exitCode=1;
}
run().catch(e=>{console.error(e.message.split('\n')[0]);process.exitCode=1});
