const {test}=require('node:test');
const assert=require('node:assert/strict');
const fs=require('node:fs');
const vm=require('node:vm');
const path=require('node:path');
const code=fs.readFileSync(path.join(__dirname,'account_bridge.js'),'utf8');
test('account navigation moves into the console header without duplication',()=>{
 const nodes=new Map(); let header=null, changed;
 const container=()=>({children:[],prepend(el){this.attach(el,true)},appendChild(el){this.attach(el,false)},attach(el,first){if(el.parentNode)el.parentNode.children=el.parentNode.children.filter(x=>x!==el);el.parentNode=this;first?this.children.unshift(el):this.children.push(el);nodes.set(el.id,el)}});
 const body=container();
 const document={body,readyState:'complete',querySelector:()=>header,getElementById:id=>nodes.get(id),createElement:()=>({style:{},setAttribute(){}})};
 const sandbox={document,location:{pathname:'/billing.html',hash:''},localStorage:{getItem:()=>null},window:{addEventListener(){}},MutationObserver:class{constructor(fn){changed=fn}observe(){}},requestAnimationFrame:fn=>fn()};
 vm.runInNewContext(code,sandbox);
 const link=nodes.get('cpa-account-link');
 assert.equal(link.textContent,'账号管理');assert.equal(link.href,'/accounts.html');assert.match(link.style.cssText,/top:16px/);assert.doesNotMatch(link.style.cssText,/bottom:/);
 header=container();changed();
 assert.equal(link.parentNode,header);assert.equal(body.children.length,0);assert.equal(header.children.length,1);assert.doesNotMatch(link.style.cssText,/position:fixed/);
 changed();assert.equal(header.children.length,1);
});

function routeSandbox(hash, account, legacy) {
 const storage=new Map();
 if(account) { storage.set('cpa-account-session',JSON.stringify(account)); if(account.loggedIn) storage.set('isLoggedIn','true'); }
 if(legacy) storage.set('managementKey','old-master-key');
 const calls=[];
 const sandbox={
  document:{readyState:'loading',documentElement:{style:{}},addEventListener(){}},
  location:{pathname:'/management.html',hash,replace(p){this.redirect=p}},
  localStorage:{getItem:k=>storage.get(k)||null,removeItem:k=>storage.delete(k)},
  fetch:(url,opts)=>{calls.push({url,opts});return Promise.resolve({ok:true})}
 };
 return {sandbox,storage,calls};
}
for (const hash of ['#/login','#/login?redirect=%2F','#login']) {
 test('legacy login route redirects to account login: '+hash,()=>{
  const {sandbox,storage,calls}=routeSandbox(hash,{token:'account-token'},true);
  vm.runInNewContext(code,sandbox);
  assert.equal(sandbox.location.redirect,'/accounts.html');
  assert.equal(sandbox.document.documentElement.style.visibility,'hidden');
  assert.equal(storage.size,0);
  assert.equal(calls[0].url,'/v0/management/accounts/logout');
  assert.equal(calls[0].opts.keepalive,true);
 });
}
test('cached master-key login cannot bypass account login',()=>{
 const {sandbox,storage}=routeSandbox('',null,true);
 vm.runInNewContext(code,sandbox);
 assert.equal(sandbox.location.redirect,'/accounts.html');assert.equal(storage.size,0);
});
test('successful account login can enter the console without a redirect loop',()=>{
 const {sandbox,storage}=routeSandbox('',{token:'account-token',loggedIn:true,user:{role:'user'}},false);
 vm.runInNewContext(code,sandbox);
 assert.equal(sandbox.location.redirect,undefined);assert.equal(storage.size,2);
});

test('intermediate login route during session restoration does not revoke the token',()=>{
 const {sandbox,storage,calls}=routeSandbox('#/login',{token:'account-token',loggedIn:true,user:{role:'user'}},false);
 vm.runInNewContext(code,sandbox);
 assert.equal(sandbox.location.redirect,undefined);
 assert.equal(sandbox.document.documentElement.style.visibility,'hidden');
 assert.equal(calls.length,0);
 assert.ok(storage.has('cpa-account-session'));
});

function activeSandbox(){
 const state=routeSandbox('',{token:'old',loggedIn:true,user:{role:'admin'}},false);
 const listeners={};let resolve;
 Object.assign(state.sandbox.document,{readyState:'complete',body:null,querySelectorAll:()=>[]});
 Object.assign(state.sandbox,{window:{addEventListener:(name,fn)=>listeners[name]=fn},MutationObserver:class{observe(){}},fetch:()=>new Promise(r=>resolve=r)});
 state.sandbox.location.reload=function(){this.reloaded=true};
 state.sandbox.localStorage.setItem=(k,v)=>state.storage.set(k,v);
 vm.runInNewContext(code,state.sandbox);
 return {...state,listeners,resolve};
}
test('storage logout redirects another console tab without revoking a new token',()=>{
 const {sandbox,storage,listeners}=activeSandbox();storage.delete('cpa-account-session');listeners.storage({key:'cpa-account-session'});assert.equal(sandbox.location.redirect,'/accounts.html');
});
test('storage account switch reloads console using the new identity',()=>{
 const {sandbox,storage,listeners}=activeSandbox();storage.set('cpa-account-session',JSON.stringify({token:'new',user:{role:'user'}}));listeners.storage({key:'cpa-account-session'});assert.equal(sandbox.location.reloaded,true);
});
test('late account lookup cannot resurrect a logged-out session',async()=>{
 const {storage,resolve}=activeSandbox();storage.delete('cpa-account-session');resolve({status:200,ok:true,json:async()=>({user:{role:'admin'}})});await new Promise(r=>setImmediate(r));assert.equal(storage.has('cpa-account-session'),false);
});
test('late unauthorized response cannot clear a replacement session',async()=>{
 const {storage,resolve}=activeSandbox();storage.set('cpa-account-session',JSON.stringify({token:'new',user:{role:'user'}}));resolve({status:401});await new Promise(r=>setImmediate(r));assert.equal(JSON.parse(storage.get('cpa-account-session')).token,'new');
});

test('user credential page hides only credential deletion controls',()=>{
 const storage=new Map([['cpa-account-session',JSON.stringify({token:'user-token',user:{role:'user'}})],['isLoggedIn','true']]);
 const button=(label,source='title')=>({hidden:false,disabled:false,title:source==='title'?label:'',textContent:source==='text'?label:'',getAttribute:k=>source==='aria'&&k==='aria-label'?label:null,style:{setProperty(k,v){this[k]=v}}});
 const removeOne=button('Delete'),removeAll=button('Delete All','text'),removeChinese=button('删除','aria'),providerDelete=button('Delete Provider','text'),edit=button('Edit','text');
 const buttons=[removeOne,removeAll,removeChinese,providerDelete,edit],body={appendChild(){}};
 const sandbox={
  document:{body,readyState:'complete',documentElement:{style:{}},querySelector:()=>null,querySelectorAll:s=>s==='button'?buttons:[],getElementById:()=>null,createElement:()=>({style:{},setAttribute(){}})},
  location:{pathname:'/management.html',hash:'#/auth-files',replace(p){this.redirect=p}},
  localStorage:{getItem:k=>storage.get(k)||null,removeItem:k=>storage.delete(k),setItem:(k,v)=>storage.set(k,v)},
  window:{addEventListener(){}},MutationObserver:class{observe(){}},requestAnimationFrame:fn=>fn(),fetch:()=>new Promise(()=>{})
 };
 vm.runInNewContext(code,sandbox);
 for(const control of [removeOne,removeAll,removeChinese]){assert.equal(control.hidden,true);assert.equal(control.disabled,true);assert.equal(control.style.display,'none')}
 for(const control of [providerDelete,edit]){assert.equal(control.hidden,false);assert.equal(control.disabled,false)}
});
