import test from 'node:test'
import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import {validateConfig,parseCompletion,transport,reportFor,validateReport,cost,digest,loadSlice} from './release-quality.mjs'
const config=()=>({kind:'release-quality-config',authorized:true,model:'offline-test-model',credentialEnv:'QUALITY_TEST_KEY',endpoint:'https://example.invalid/v1/chat/completions',priorRef:'v0.9.1',priorCommit:'a'.repeat(40),candidateCommit:'b'.repeat(40),sliceSha256:'c'.repeat(64),maxCallsPerCase:4,maxInputTokens:32768,maxOutputTokens:512,maxTotalTokens:532480,maxNanoUsd:1000000000,inputNanoUsdPerToken:100,cachedInputNanoUsdPerToken:50,outputNanoUsdPerToken:200,timeoutMs:1000,maxResponseBytes:10000,changeThreshold:0})
const completion=()=>({model:'offline-test-model',choices:[{finish_reason:'stop',message:{role:'assistant',content:'Public fixture answer.'}}],usage:{prompt_tokens:12,completion_tokens:4,total_tokens:16}})
const request=()=>({suffix:'chat/completions',body:{model:'offline-test-model',messages:[{role:'user',content:'Public test fixture.'}]}})
const response=body=>new Response(JSON.stringify(body),{headers:{'content-type':'application/json'}})
const key='offline-fixture-key'
const identity=c=>({prior:{commit:c.priorCommit,version:'0.9.1',artifactSha256:'d'.repeat(64)},candidate:{commit:c.candidateCommit,version:'0.10.0',artifactSha256:'e'.repeat(64)}})
function evidence(c,provenance='offline-test'){
 const u={input:12,output:4,cached:0,nanoUsd:2000,provenance}
 const cases=['model-policy-refusal','model-recorded-tool-result'].map(id=>({id,answers:['Public fixture answer.'],usage:[{...u}]}))
 return reportFor(c,identity(c),cases,structuredClone(cases),provenance)
}
test('concrete authorization, identities, bounded rates and threshold are mandatory',()=>{
 const c=config();assert.equal(validateConfig(c),c)
 for(const change of [{authorized:false},{candidateCommit:c.priorCommit},{endpoint:'http://example.invalid/chat/completions'},{endpoint:'https://user:secret@example.invalid/chat/completions'},{endpoint:'https://example.invalid/chat/completions?key=secret'},{model:'SET_MODEL'},{maxCallsPerCase:5},{inputNanoUsdPerToken:0},{changeThreshold:NaN},{maxNanoUsd:Infinity}])assert.throws(()=>validateConfig({...c,...change}))
})
test('rejects missing, partial, truncated, wrong-model and invalid usage',()=>{
 for(const change of [{model:'other'},{choices:[]},{choices:[{finish_reason:'length',message:{}}]},{usage:undefined},{usage:{prompt_tokens:12,completion_tokens:4,total_tokens:17}},{usage:{prompt_tokens:12,completion_tokens:4,total_tokens:16,prompt_tokens_details:{cached_tokens:13}}}])assert.throws(()=>parseCompletion(config(),{...completion(),...change}))
 assert.deepEqual(parseCompletion(config(),completion()),{input:12,output:4,cached:0})
})
test('reserves before send and accounts both releases and cache rate exactly',async()=>{
 const c=config();let sends=0
 const t=transport(c,key,{provenance:'offline-test',fetchImpl:async(_url,init)=>{sends++;const b=JSON.parse(init.body);assert.equal(b.stream,false);assert.equal(b.max_completion_tokens,512);return response(completion())}})
 await t.call(request());await t.call(request());assert.equal(sends,2);assert.equal(t.summary().nanoUsd,4000);assert.equal(t.rows[0].provenance,'offline-test');assert.equal(cost(c,{input:12,cached:2,output:4}),1900)
 for(const bounds of [{maxTotalTokens:33279},{maxNanoUsd:1},{maxInputTokens:10}]){
  let sent=false;const bounded=transport({...c,...bounds},key,{fetchImpl:async()=>{sent=true;return response(completion())}})
  await assert.rejects(bounded.call(request()));assert.equal(sent,false)
 }
})
test('global reservation is shared across all case and release calls',async()=>{
 const c={...config(),maxTotalTokens:33280+16};let calls=0
 const t=transport(c,key,{fetchImpl:async()=>{calls++;return response(completion())}})
 await t.call(request());await t.call(request());await assert.rejects(t.call(request()),/budget/);assert.equal(calls,2)
})
test('malformed, oversized, reflected-secret and HTTP failures retain uncertain usage without raw details',async()=>{
 for(const fetchImpl of [async()=>response({...completion(),model:'wrong'}),async()=>new Response('raw-secret',{status:401}),async()=>response({...completion(),padding:'x'.repeat(10001)}),async()=>response({...completion(),secret:key}),async()=>{throw Error('private endpoint/key')}]){
  const t=transport(config(),key,{fetchImpl})
  await assert.rejects(t.call(request()),e=>!e.message.includes('raw-secret')&&!e.message.includes(key)&&!e.message.includes('private endpoint'))
  assert.equal(t.summary().uncertain,true);await assert.rejects(t.call(request()),/incomplete/)
 }
})
test('changed answer pairs/share require decision; offline provenance cannot satisfy gate',()=>{
 const c=config(),r=evidence(c)
 assert.throws(()=>validateReport(c,r),/live evidence/)
 const live=evidence(c,'live') // Hand-authored validator fixture; never a saved live report.
 assert.equal(validateReport(c,live).status,'within-threshold')
 live.rows[1].candidate.answers=['Changed public fixture answer.']
 const changed=reportFor(c,live.identities,live.rows.map(r=>r.prior),live.rows.map(r=>r.candidate))
 assert.equal(validateReport(c,changed).share,0.5);assert.equal(changed.status,'decision-required');assert.equal(changed.rows[1].prior.answers[0],'Public fixture answer.')
 for(const mutate of [r=>r.totalTokens++,r=>r.nanoUsd++,r=>r.rows.pop(),r=>r.rows[0].prior.usage=[],r=>r.identities.candidate.commit='f'.repeat(40),r=>r.rows[0].id='extra',r=>r.rows[0].prior.usage[0].provenance='offline-test',r=>r.rows[0].prior.usage[0].output=-1]){const bad=structuredClone(changed);mutate(bad);assert.throws(()=>validateReport(c,bad))}
})
test('reviewed fixed slice pins all corpus bytes',()=>{
 const path=new URL('../quality/release-slice.json',import.meta.url)
 const root=new URL('..',import.meta.url).pathname
 assert.equal(loadSlice(root,path,digest(readFileSync(path))).cases.length,2)
 assert.throws(()=>loadSlice(root,path,'0'.repeat(64)),/digest/)
})
