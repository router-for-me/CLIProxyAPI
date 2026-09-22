// Live Playwright regression; requires NODE_PATH for Playwright, CPA_TEST_BASE_URL,
// CPA_TEST_PASSWORD_FILE (0600) and optional CPA_TEST_USER (defaults to admin).
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const base=process.env.CPA_TEST_BASE_URL;
const passwordFile=process.env.CPA_TEST_PASSWORD_FILE;
if(!base||!passwordFile)throw new Error('Set CPA_TEST_BASE_URL and CPA_TEST_PASSWORD_FILE');
(async()=>{
 const browser=await chromium.launch();const page=await browser.newPage();
 const errors=[];let recovered=0;
 page.on('pageerror',e=>errors.push(e.message));
 page.on('response',r=>{if(new URL(r.url()).pathname==='/v0/management/auth-files'&&r.status()===200)recovered++});
 try{
  await page.goto(base+'/accounts.html');
  await page.locator('#username').fill(process.env.CPA_TEST_USER||'admin');
  await page.locator('#password').fill(fs.readFileSync(passwordFile,'utf8').trim());
  await page.locator('#loginForm button[type=submit]').click();
  await page.waitForURL('**/management.html#/');
  await page.waitForTimeout(1500);recovered=0;
  const failures=new Map();
  await page.route('**/v0/management/**',async route=>{
   const pathname=new URL(route.request().url()).pathname;
   if(route.request().method()==='GET'&&['/auth-files','/oauth-excluded-models','/oauth-model-alias'].some(p=>pathname==='/v0/management'+p)){
    const n=failures.get(pathname)||0;
    if(n<2){failures.set(pathname,n+1);return route.abort('connectionreset')}
   }
   await route.continue();
  });
  await page.locator('a[href="#/auth-files"]').first().click();
  await page.waitForTimeout(5000);
  assert.equal(failures.size,3);assert.ok([...failures.values()].every(n=>n===2));
  assert.ok(recovered>0,'credential list must recover without manual refresh');
  assert.equal(await page.getByText('Network Error',{exact:false}).count(),0);
  assert.deepEqual(errors,[]);
  console.log('PASS: credential page recovers after two connection resets per read endpoint');
 }finally{
  await page.evaluate(async()=>{
   const s=JSON.parse(localStorage.getItem('cpa-account-session')||'null');
   if(s?.token)await fetch('/v0/management/accounts/logout',{method:'POST',headers:{Authorization:'Bearer '+s.token}});
  }).catch(()=>{});
  await browser.close();
 }
})().catch(e=>{console.error(e.message);process.exitCode=1});
