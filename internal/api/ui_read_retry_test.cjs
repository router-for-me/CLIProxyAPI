const {test}=require('node:test');const assert=require('node:assert/strict');const fs=require('node:fs');const path=require('node:path');
const go=fs.readFileSync(path.join(__dirname,'ui_read_retry.go'),'utf8');
const body=go.match(/const resilientManagementGET = `([\s\S]*?)`/)[1];
function client(request){return new Function('return ({instance:{get:arguments[0]},'+body+'})')(request)}
test('transient credential reads retry twice and return recovered data',async()=>{let n=0;const c=client(async()=>{if(++n<3)throw {code:'ERR_NETWORK'};return {data:{files:['recovered']}}});assert.deepEqual(await c.get('/auth-files'),{files:['recovered']});assert.equal(n,3)});
test('persistent network failure remains bounded',async()=>{let n=0;const c=client(async()=>{n++;throw {code:'ERR_NETWORK'}});await assert.rejects(c.get('/oauth-model-alias'));assert.equal(n,3)});
test('denials, cancellations, other routes and ordinary errors do not retry',async()=>{for(const [url,error,options] of [['/auth-files',{status:401}],['/auth-files',{status:403}],['/auth-files',{code:'ERR_CANCELED'}],['/auth-files',{code:'ERR_NETWORK'},{signal:{aborted:true}}],['/api-call',{code:'ERR_NETWORK'}],['/auth-files/refresh',{code:'ERR_NETWORK'}],['/auth-files',{status:400}]]){let n=0;const c=client(async()=>{n++;throw error});await assert.rejects(c.get(url,options));assert.equal(n,1,url)}});
