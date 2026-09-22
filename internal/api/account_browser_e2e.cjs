// Real-browser regression. Run with NODE_PATH pointing to an installed Playwright,
// CPA_TEST_BASE_URL, CPA_TEST_USER and CPA_TEST_PASSWORD_FILE (0600). No secrets logged.
const {chromium}=require('playwright');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const path=require('node:path');
const base=process.env.CPA_TEST_BASE_URL;
const passwordFile=process.env.CPA_TEST_PASSWORD_FILE;
if(!base||!passwordFile)throw new Error('Set CPA_TEST_BASE_URL and CPA_TEST_PASSWORD_FILE');
const username=process.env.CPA_TEST_USER||'user';
const password=fs.readFileSync(passwordFile,'utf8').trim();
const artifacts=process.env.CPA_TEST_ARTIFACTS;
async function run(){
 const browser=await chromium.launch({headless:true});
 try{
 for(const viewport of [{width:1440,height:1000},{width:390,height:844}]){
  const context=await browser.newContext({viewport});const page=await context.newPage();
  const errors=[];const failed=[];
  page.on('pageerror',e=>errors.push(e.message));
  page.on('response',r=>{if(r.status()>=400)failed.push({path:new URL(r.url()).pathname,status:r.status()})});
  await page.goto(base+'/management.html#/login');
  await page.waitForURL('**/accounts.html');
  await page.locator('#username').fill(username);await page.locator('#password').fill(password);
  await page.locator('#loginForm button[type=submit]').click();
  await page.waitForURL('**/management.html#/');
  await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
  await page.waitForTimeout(1200);
  assert.equal(new URL(page.url()).hash,'#/','login must not bounce to accounts');
  assert.equal(await page.locator('input[type=password]:visible').count(),0);
  const saved=await page.evaluate(()=>JSON.parse(localStorage.getItem('cpa-account-session')));
  assert.equal(saved.user.username,username);assert.equal(saved.user.role,'user');
  const bb=await page.locator('#cpa-account-link').boundingBox();
  assert.ok(bb.x>=0&&bb.x+bb.width<=viewport.width+1&&bb.y<120,'account entry is in visible header');
  await page.reload();await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
  assert.equal(new URL(page.url()).hash,'#/');
  console.log('PASS',viewport.width,'login, session restore, refresh, header layout');
  if(viewport.width>1000){
   for(const [route,title] of [['auth-files','Auth Files Management'],['oauth','OAuth Login'],['ai-providers','AI Providers'],['config','Config Panel'],['logs','Logs Viewer'],['system','Management Center Info']]){
    await page.locator('a[href="#/'+route+'"]').first().click();
    await page.getByRole('heading',{name:title,exact:true,level:1}).waitFor();
    await page.waitForTimeout(300);
   }
   console.log('PASS desktop allowed pages: credentials, OAuth, providers, config, logs, info');
   assert.equal(await page.locator('a[href="#/quota"]:visible').count(),0);
   assert.equal(await page.locator('a[href="/billing.html"]:visible').count(),0);
  }
  await page.locator('#cpa-account-link').click();await page.waitForURL('**/accounts.html');
  await page.locator('#appCards').waitFor({state:'visible'});
  assert.equal(await page.locator('#adminPanel').isVisible(),false);
  assert.ok((await page.locator('#profile').innerText()).includes(username));
  await page.getByRole('link',{name:'主控制台',exact:true}).click();
  await page.locator('.header-actions #cpa-account-link').waitFor({state:'visible'});
  assert.equal(errors.length,0,'browser JS errors: '+JSON.stringify(errors));
  assert.equal(failed.length,0,'unexpected HTTP failures: '+JSON.stringify(failed));
  console.log('PASS',viewport.width,'account page and return; no JS/HTTP errors');
  // Direct requests verify role enforcement, independent of hidden navigation.
  for(const route of ['/billing/api-tokens','/quota/providers','/accounts/users']){
   const response=await context.request.get(base+'/v0/management'+route,{headers:{Authorization:'Bearer '+saved.token}});
   assert.equal(response.status(),403,route);
  }
  if(artifacts){fs.mkdirSync(artifacts,{recursive:true,mode:0o700});await page.screenshot({path:path.join(artifacts,'console-'+viewport.width+'.png'),fullPage:true})}
  await page.getByTitle('Logout',{exact:true}).click();
  await page.waitForURL('**/accounts.html');await page.locator('#loginCard').waitFor({state:'visible'});
  let status;
  for(let i=0;i<20;i++){
   status=(await context.request.get(base+'/v0/management/accounts/me',{headers:{Authorization:'Bearer '+saved.token}})).status();
   if(status===401)break;await page.waitForTimeout(100);
  }
  assert.equal(status,401,'console logout revokes account session');
  assert.equal(await page.evaluate(()=>localStorage.getItem('cpa-account-session')),null);
  console.log('PASS',viewport.width,'billing/quota/user-admin denied; console logout revokes session');
  await context.close();
 }
 }finally{await browser.close()}
}
run().catch(e=>{console.error(e.message);process.exitCode=1});
